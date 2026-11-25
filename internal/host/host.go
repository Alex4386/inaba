// Package host runs agent threads on this machine and serves the host API.
package host

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/ptyterm"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/provider"
)

const openTimeout = 2 * time.Minute

// Host implements ThreadService and TerminalService.
type Host struct {
	inabav1connect.UnimplementedThreadServiceHandler
	inabav1connect.UnimplementedTerminalServiceHandler
	inabav1connect.UnimplementedProjectServiceHandler
	inabav1connect.UnimplementedGitServiceHandler
	inabav1connect.UnimplementedHousekeepingServiceHandler
	inabav1connect.UnimplementedModuleServiceHandler
	inabav1connect.UnimplementedSystemServiceHandler
	inabav1connect.UnimplementedUsageServiceHandler

	// providers is what threads can be started with, and providerInfo how each is
	// presented; SetProviders replaces both while the host runs.
	pmu          sync.RWMutex
	providers    map[string]provider.Provider
	providerInfo map[string]ProviderInfo

	// projects is nil when the host runs without a project database.
	projects *Projects
	// save is nil when the host runs without a database; threads then live in memory.
	save *saver
	// stopArchive ends the sweep that archives quiet threads; nil until the data is open.
	stopArchive chan struct{}
	// dataDir is the folder beside the database, where thread worktrees are made.
	dataDir string
	dbPath  string
	// offered is what each provider's agent last said a thread can be set to.
	offered map[string][]*inabav1.SessionOption

	bus       *bus
	served    served
	moduleCtl atomic.Pointer[ModuleController]

	mu      sync.Mutex
	threads map[string]*thread
}

// ProviderInfo is how a provider is presented, from the host's config.
type ProviderInfo struct {
	DisplayName string
	// Disabled leaves the provider out of new threads; its threads keep running.
	Disabled bool
	// HiddenModels are model choices left out of what threads offer, by value.
	HiddenModels []string
	// Icon is an image URL to show for it.
	Icon string
}

// shown is what a provider's threads offer of what its agent does: the models the
// config hides are left out, unless one is what the thread is on.
func (h *Host) shown(providerID string, options []*inabav1.SessionOption) []*inabav1.SessionOption {
	_, info := h.provider(providerID)
	if len(info.HiddenModels) == 0 {
		return options
	}
	out := make([]*inabav1.SessionOption, len(options))
	for i, o := range options {
		out[i] = o
		if o.Category != "model" {
			continue
		}
		c := proto.Clone(o).(*inabav1.SessionOption)
		c.Choices = slices.DeleteFunc(c.Choices, func(ch *inabav1.SessionOption_Choice) bool {
			return ch.Value != o.Value && slices.Contains(info.HiddenModels, ch.Value)
		})
		out[i] = c
	}
	return out
}

// Offered is what the provider's agent offered the last time one of its threads ran,
// models the config hides included.
func (h *Host) Offered(providerID string) []*inabav1.SessionOption {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.offered[providerID]
}

// SetProviders replaces what threads can be started with. Threads already made keep
// the provider they started with.
func (h *Host) SetProviders(providers []provider.Provider, info map[string]ProviderInfo) {
	m := map[string]provider.Provider{}
	for _, p := range providers {
		m[p.ID()] = p
	}
	h.pmu.Lock()
	h.providers, h.providerInfo = m, info
	h.pmu.Unlock()
}

// provider returns the provider with the id, nil when there is none.
func (h *Host) provider(id string) (provider.Provider, ProviderInfo) {
	h.pmu.RLock()
	defer h.pmu.RUnlock()
	return h.providers[id], h.providerInfo[id]
}

func New(providers ...provider.Provider) *Host {
	h := &Host{providers: map[string]provider.Provider{}, threads: map[string]*thread{}, offered: map[string][]*inabav1.SessionOption{}, bus: &bus{subs: map[*subscriber]struct{}{}, deleted: map[*int]func(string){}}}
	for _, p := range providers {
		h.providers[p.ID()] = p
	}
	return h
}

type thread struct {
	prov provider.Provider
	// save is nil for a host running without a database.
	save *saver
	// learned is told what the agent offers, each time that changes.
	learned func([]*inabav1.SessionOption)
	// show trims what the agent offers to what the thread shows.
	show func([]*inabav1.SessionOption) []*inabav1.SessionOption
	// bus takes the thread's events to the modules that follow them.
	bus *bus
	// mcp is the MCP servers modules give the thread's agent sessions.
	mcp func(*inabav1.Thread) []provider.MCPServer
	// copying is set while a fork copies events in: history, not news.
	copying bool
	// op serializes whatever starts or stops the thread's process.
	op sync.Mutex
	// deleted is set, under op, once the thread is deleted; a turn ending after that leaves no trace.
	deleted bool

	mu      sync.Mutex
	meta    *inabav1.Thread
	events  []*inabav1.Event
	changed chan struct{}
	sess    provider.Session
	term    *ptyterm.Terminal
}

func (t *thread) snapshot() *inabav1.Thread {
	t.mu.Lock()
	defer t.mu.Unlock()
	return proto.Clone(t.meta).(*inabav1.Thread)
}

// emit appends an event, stamping its sequence number, and wakes subscribers.
func (t *thread) emit(e *inabav1.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// An agent reports its options, commands and usage whenever it likes; only a real change is news.
	switch p := e.Payload.(type) {
	case *inabav1.Event_OptionsChanged:
		full := p.OptionsChanged.Options
		if t.show != nil {
			p.OptionsChanged.Options = t.show(full)
		}
		if proto.Equal(&inabav1.OptionsChanged{Options: t.meta.Options}, p.OptionsChanged) {
			return
		}
		t.meta.Options = p.OptionsChanged.Options
		if t.learned != nil {
			t.learned(full)
		}
	case *inabav1.Event_CommandsChanged:
		if proto.Equal(&inabav1.CommandsChanged{Commands: t.meta.Commands}, p.CommandsChanged) {
			return
		}
		t.meta.Commands = p.CommandsChanged.Commands
	case *inabav1.Event_UsageChanged:
		if proto.Equal(t.meta.Usage, p.UsageChanged.Usage) {
			return
		}
		t.meta.Usage = p.UsageChanged.Usage
	case *inabav1.Event_LayoutChanged:
		if t.meta.Layout == p.LayoutChanged.Layout {
			return
		}
		t.meta.Layout = p.LayoutChanged.Layout
	case *inabav1.Event_ThreadTitle:
		// Someone's own name for the thread stays; the agent's replaces only the first message's.
		title := titleFrom(p.ThreadTitle.Title)
		if t.meta.Named || title == "" || title == t.meta.Title {
			return
		}
		t.meta.Title = title
	}
	t.meta.LastSeq++
	e.Seq = t.meta.LastSeq
	e.ThreadId = t.meta.Id
	e.Time = timestamppb.Now()
	if activity(e) {
		t.meta.ActivitySeq = e.Seq
		t.meta.UpdatedAt = e.Time
	}
	switch p := e.Payload.(type) {
	case *inabav1.Event_UserMessage:
		if t.meta.Title == "" {
			t.meta.Title = titleFrom(p.UserMessage.Text)
		}
	case *inabav1.Event_ToolCall:
		if p.ToolCall.Title != "" {
			t.meta.Activity = p.ToolCall.Title
		}
	case *inabav1.Event_PermissionRequest:
		t.meta.Status = inabav1.ThreadStatus_THREAD_STATUS_WAITING
	case *inabav1.Event_PermissionResolved:
		if t.meta.Status == inabav1.ThreadStatus_THREAD_STATUS_WAITING {
			t.meta.Status = inabav1.ThreadStatus_THREAD_STATUS_RUNNING
		}
	case *inabav1.Event_TurnStarted:
		t.meta.Status = inabav1.ThreadStatus_THREAD_STATUS_RUNNING
		t.meta.Activity = ""
		t.meta.LastTurnFailed = false
	case *inabav1.Event_TurnEnded:
		t.meta.Status = inabav1.ThreadStatus_THREAD_STATUS_IDLE
		t.meta.Activity = ""
		t.meta.LastTurnFailed = p.TurnEnded.Error != ""
	case *inabav1.Event_ModeChanged:
		t.meta.Mode = p.ModeChanged.Mode
	case *inabav1.Event_Rewound:
		t.meta.Rewound = true
	}
	if t.bus != nil {
		t.bus.publish(t.meta, e, t.copying)
	}
	t.events = append(t.events, e)
	t.save.event(e)
	// Streamed text changes nothing about the thread but its length, which the log gives.
	if _, text := e.Payload.(*inabav1.Event_TextDelta); !text {
		t.save.meta(t.meta)
	}
	close(t.changed)
	t.changed = make(chan struct{})
}

// activity reports whether an event moves the conversation, as opposed to bookkeeping (layout,
// options, usage, commands, mode, provider, the agent's title, raw provider output) that someone
// opening the thread or the agent starting up produces. A new event type counts as bookkeeping
// until it is listed here.
func activity(e *inabav1.Event) bool {
	switch e.Payload.(type) {
	case *inabav1.Event_UserMessage, *inabav1.Event_TextDelta, *inabav1.Event_ToolCall, *inabav1.Event_Plan,
		*inabav1.Event_PermissionRequest, *inabav1.Event_PermissionResolved,
		*inabav1.Event_TurnStarted, *inabav1.Event_TurnEnded, *inabav1.Event_TurnChanges, *inabav1.Event_Rewound:
		return true
	}
	return false
}

// titleFrom names a thread after the message that started it: its first line, cut short.
// shortcut: no summarizing; have the agent or a small model write titles if these read poorly.
func titleFrom(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if r := []rune(line); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return line
}

// after returns the events past seq and a channel closed when more arrive.
func (t *thread) after(seq uint64, limit int) ([]*inabav1.Event, <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Sequence numbers start at 1 without gaps, so seq is also the slice offset.
	start := min(seq, uint64(len(t.events)))
	evs := t.events[start:]
	if limit > 0 && len(evs) > limit {
		evs = evs[:limit]
	}
	return evs, t.changed
}

func (t *thread) openSession(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	meta := t.snapshot()
	cfg := provider.Config{Cwd: meta.Cwd, NativeSessionID: meta.NativeSessionId}
	if t.mcp != nil {
		cfg.MCPServers = t.mcp(meta)
	}
	sess, err := t.prov.Open(ctx, cfg, t.emit)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, "could not start "+meta.Provider+": "+err.Error()).WithCause(err)
	}
	// A fresh agent process starts from its defaults. Put back what the user had chosen.
	chosen := map[string]string{}
	for _, was := range meta.Options {
		chosen[was.Id] = was.Value
	}
	t.applyOptions(ctx, sess, chosen)
	t.mu.Lock()
	t.sess = sess
	t.meta.NativeSessionId = sess.NativeSessionID()
	t.save.meta(t.meta)
	t.mu.Unlock()
	return nil
}

func (t *thread) closeSession() {
	t.mu.Lock()
	sess := t.sess
	t.sess = nil
	t.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

func (t *thread) closeTerminal() {
	t.mu.Lock()
	term := t.term
	t.term = nil
	t.mu.Unlock()
	if term != nil {
		term.Close()
	}
}

// structured returns the live session, or why structured calls cannot run.
func (t *thread) structured() (provider.Session, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.meta.Mode != inabav1.ThreadMode_THREAD_MODE_STRUCTURED {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"thread is in terminal mode, where structured calls are not supported; switch it to structured mode first")
	}
	return t.sess, nil
}

func (h *Host) thread(id string) (*thread, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.threads[id]
	if t == nil {
		return nil, connect.NewError(connect.CodeNotFound, "no thread "+id)
	}
	return t, nil
}

// checkDir reports why a path cannot be a working directory on this host.
func checkDir(path string) error {
	if !filepath.IsAbs(path) {
		return connect.NewError(connect.CodeInvalidArgument, "the folder must be an absolute path")
	}
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return connect.NewError(connect.CodeInvalidArgument, "the folder does not exist on this host")
	}
	return nil
}

func (h *Host) ListProviders(context.Context, *inabav1.ListProvidersRequest) (*inabav1.ListProvidersResponse, error) {
	res := &inabav1.ListProvidersResponse{}
	h.pmu.RLock()
	providers, info := h.providers, h.providerInfo
	h.pmu.RUnlock()
	for id, p := range providers {
		if info[id].Disabled {
			continue
		}
		tr, ok := p.(provider.TerminalResumer)
		_, terminalOnly := p.(provider.TerminalOnly)
		h.mu.Lock()
		offered := h.offered[id]
		h.mu.Unlock()
		res.Providers = append(res.Providers, &inabav1.Provider{
			Id:         id,
			Terminal:   ok && len(tr.TerminalCommand("")) > 0,
			Structured: !terminalOnly,
			// shortcut: every structured provider so far has raw threads; ask the provider once one does not.
			Raw:         !terminalOnly,
			Options:     h.shown(id, offered),
			DisplayName: cmp.Or(info[id].DisplayName, id),
			Icon:        info[id].Icon,
		})
	}
	sort.Slice(res.Providers, func(i, j int) bool { return res.Providers[i].Id < res.Providers[j].Id })
	return res, nil
}

func (h *Host) CreateThread(ctx context.Context, req *inabav1.CreateThreadRequest) (*inabav1.Thread, error) {
	prov, info := h.provider(req.Provider)
	if prov == nil || info.Disabled {
		return nil, connect.NewError(connect.CodeInvalidArgument, "unknown provider "+req.Provider)
	}
	id := randomID()
	var project *Project
	if req.ProjectId == "" {
		// Outside a project a thread can start in any folder, which takes its own permission.
		if err := may(ctx, rpc.PermThreadsAnywhere); err != nil {
			return nil, err
		}
	} else {
		if h.projects != nil {
			var err error
			if project, err = h.projects.Get(req.ProjectId); err != nil {
				return nil, connect.NewError(connect.CodeInternal, "could not read the project").WithCause(err)
			}
		}
		if project == nil {
			return nil, connect.NewError(connect.CodeNotFound, "no project "+req.ProjectId)
		}
		req.Cwd = project.Cwd
	}
	if req.Worktree != nil {
		if project == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a worktree is made from a project; name one")
		}
		dir, err := h.addWorktree(ctx, project, id, req.Worktree)
		if err != nil {
			return nil, err
		}
		req.Cwd = dir
	}
	if err := checkDir(req.Cwd); err != nil {
		return nil, err
	}
	t := &thread{
		prov:    prov,
		save:    h.save,
		learned: h.learn(req.Provider),
		show:    func(o []*inabav1.SessionOption) []*inabav1.SessionOption { return h.shown(req.Provider, o) },
		bus:     h.bus,
		mcp:     h.sessionMCP,
		changed: make(chan struct{}),
		meta: &inabav1.Thread{
			Id: id, Provider: req.Provider, Cwd: req.Cwd, Title: req.Title, ProjectId: req.ProjectId, Worktree: req.Worktree != nil,
			Mode:      inabav1.ThreadMode_THREAD_MODE_STRUCTURED,
			Status:    inabav1.ThreadStatus_THREAD_STATUS_IDLE,
			CreatedAt: timestamppb.Now(),
			UpdatedAt: timestamppb.Now(),
		},
	}
	var err error
	if _, terminalOnly := prov.(provider.TerminalOnly); terminalOnly || req.Mode == inabav1.ThreadMode_THREAD_MODE_TERMINAL {
		err = t.startTerminal()
	} else if err = t.openSession(ctx); err == nil {
		t.mu.Lock()
		sess := t.sess
		t.mu.Unlock()
		t.applyOptions(ctx, sess, req.Options)
	}
	if err != nil {
		// The thread never came to be; neither should the worktree made for it.
		h.removeWorktree(ctx, t.meta)
		return nil, err
	}
	h.mu.Lock()
	h.threads[t.meta.Id] = t
	h.mu.Unlock()
	return t.snapshot(), nil
}

func (h *Host) ListThreads(context.Context, *inabav1.ListThreadsRequest) (*inabav1.ListThreadsResponse, error) {
	h.mu.Lock()
	threads := make([]*thread, 0, len(h.threads))
	for _, t := range h.threads {
		threads = append(threads, t)
	}
	h.mu.Unlock()
	res := &inabav1.ListThreadsResponse{}
	for _, t := range threads {
		res.Threads = append(res.Threads, t.snapshot())
	}
	sort.Slice(res.Threads, func(i, j int) bool {
		return res.Threads[i].CreatedAt.AsTime().After(res.Threads[j].CreatedAt.AsTime())
	})
	return res, nil
}

func (h *Host) GetThread(_ context.Context, req *inabav1.GetThreadRequest) (*inabav1.Thread, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	return t.snapshot(), nil
}

func (h *Host) UpdateThread(ctx context.Context, req *inabav1.UpdateThreadRequest) (*inabav1.Thread, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	if req.Provider != nil {
		if err := h.switchProvider(ctx, t, *req.Provider); err != nil {
			return nil, err
		}
	}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a thread needs a title")
		}
		t.mu.Lock()
		t.meta.Title = titleFrom(title)
		t.meta.Named = true
		t.save.meta(t.meta)
		t.mu.Unlock()
	}
	if req.Pinned != nil {
		pinned := ""
		if *req.Pinned {
			pinned = "1"
		}
		if err := h.SetLabel(req.ThreadId, "pinned", pinned); err != nil {
			return nil, err
		}
	}
	if req.Archived != nil {
		archived := ""
		if *req.Archived {
			archived = "1"
		}
		if err := h.SetLabel(req.ThreadId, "archived", archived); err != nil {
			return nil, err
		}
	}
	return t.snapshot(), nil
}

// switchProvider moves an idle thread to another provider's agent. The old agent's memory does
// not come along, so the next turn hands the new one the conversation so far.
func (h *Host) switchProvider(ctx context.Context, t *thread, id string) error {
	prov, info := h.provider(id)
	if prov == nil || info.Disabled {
		return connect.NewError(connect.CodeInvalidArgument, "unknown provider "+id)
	}
	if _, terminalOnly := prov.(provider.TerminalOnly); terminalOnly {
		return connect.NewError(connect.CodeInvalidArgument, id+" only runs in a terminal; start a new thread for it")
	}
	t.op.Lock()
	defer t.op.Unlock()
	meta := t.snapshot()
	if meta.Provider == id {
		return nil
	}
	if meta.Mode != inabav1.ThreadMode_THREAD_MODE_STRUCTURED || meta.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE {
		return connect.NewError(connect.CodeFailedPrecondition, "the provider can be changed only while the thread is idle")
	}
	_, was := h.provider(meta.Provider)
	t.closeSession()
	t.mu.Lock()
	t.prov = prov
	t.learned = h.learn(id)
	t.show = func(o []*inabav1.SessionOption) []*inabav1.SessionOption { return h.shown(id, o) }
	t.meta.Provider = id
	t.meta.NativeSessionId = ""
	t.meta.Options = nil
	// Kept from the first switch, if turns have not run since: the conversation is still the old agent's.
	if t.meta.HandoffFrom == "" {
		t.meta.HandoffFrom = cmp.Or(was.DisplayName, meta.Provider)
	}
	t.save.meta(t.meta)
	t.mu.Unlock()
	t.emit(&inabav1.Event{Payload: &inabav1.Event_ProviderChanged{ProviderChanged: &inabav1.ProviderChanged{Provider: id, From: meta.Provider}}})
	// Started now, so the new agent's models show before the next message; failing that, the next turn starts it.
	if err := t.openSession(ctx); err != nil {
		slog.Warn("could not start the new provider yet", "thread", meta.Id, "provider", id, "error", err)
	}
	return nil
}

// maxLayout bounds what one thread's screen layout may take in its record and log.
const maxLayout = 16 << 10

func (h *Host) SetLayout(_ context.Context, req *inabav1.SetLayoutRequest) (*inabav1.Thread, error) {
	if len(req.Layout) > maxLayout {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a layout is at most 16 KiB")
	}
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	// An event, so everyone following the thread moves with it.
	t.emit(&inabav1.Event{Payload: &inabav1.Event_LayoutChanged{LayoutChanged: &inabav1.LayoutChanged{Layout: req.Layout}}})
	return t.snapshot(), nil
}

func (h *Host) DeleteThread(ctx context.Context, req *inabav1.DeleteThreadRequest) (*inabav1.DeleteThreadResponse, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	delete(h.threads, req.ThreadId)
	h.mu.Unlock()
	t.op.Lock()
	t.deleted = true
	t.op.Unlock()
	t.closeSession()
	t.closeTerminal()
	t.save.delete(req.ThreadId)
	h.bus.gone(req.ThreadId)
	dropCheckpoints(ctx, t.snapshot().Cwd, req.ThreadId)
	h.removeWorktree(ctx, t.snapshot())
	_ = os.RemoveAll(filepath.Join(t.snapshot().Cwd, uploadsDir(req.ThreadId)))
	return &inabav1.DeleteThreadResponse{}, nil
}

// Close stops every thread's process.
func (h *Host) Close() {
	h.mu.Lock()
	if h.stopArchive != nil {
		close(h.stopArchive)
		h.stopArchive = nil
	}
	threads := h.threads
	h.threads = map[string]*thread{}
	h.mu.Unlock()
	for _, t := range threads {
		t.closeSession()
		t.closeTerminal()
	}
}

func (h *Host) StartTurn(ctx context.Context, req *inabav1.StartTurnRequest) (*inabav1.StartTurnResponse, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	t.op.Lock()
	defer t.op.Unlock()
	sess, err := t.structured()
	if err != nil {
		return nil, err
	}
	if t.deleted {
		return nil, connect.NewError(connect.CodeNotFound, "no thread "+req.ThreadId)
	}
	meta := t.snapshot()
	if err := checkAttachments(meta.Cwd, req.Attachments); err != nil {
		return nil, err
	}
	if err := saveUploads(ctx, meta.Cwd, meta.Id, req.Attachments); err != nil {
		return nil, err
	}
	h.bringBack(meta)
	msg := &inabav1.UserMessage{Text: req.Text, Attachments: req.Attachments}
	busy := meta.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE
	if !busy && len(meta.Queue) == 0 {
		return &inabav1.StartTurnResponse{}, t.runTurn(ctx, msg)
	}

	// A turn is running, or older messages are still waiting: this one takes its place in line.
	queued := &inabav1.QueuedMessage{Id: randomID(), Text: req.Text, Attachments: req.Attachments}
	t.mu.Lock()
	if req.Interrupt && busy {
		t.meta.Queue = append([]*inabav1.QueuedMessage{queued}, t.meta.Queue...)
	} else {
		t.meta.Queue = append(t.meta.Queue, queued)
	}
	// Sending a message is a go-ahead for whatever was held back.
	t.meta.QueuePaused = false
	t.save.meta(t.meta)
	t.mu.Unlock()
	switch {
	case !busy:
		t.runNext()
	case req.Interrupt && sess != nil:
		_ = sess.Interrupt(ctx)
	}
	return &inabav1.StartTurnResponse{QueuedId: queued.Id}, nil
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// runTurn starts a turn and returns once it is under way. The caller holds t.op.
func (t *thread) runTurn(ctx context.Context, msg *inabav1.UserMessage) error {
	sess, _ := t.structured()
	if sess == nil {
		// The last turn took the process down with it; start it again on the same thread.
		if err := t.openSession(ctx); err != nil {
			return err
		}
		sess, _ = t.structured()
	}
	prompt := t.prompt(msg)
	t.emit(&inabav1.Event{Payload: &inabav1.Event_UserMessage{UserMessage: msg}})
	t.emit(&inabav1.Event{Payload: &inabav1.Event_TurnStarted{TurnStarted: &inabav1.TurnStarted{}}})
	// Taken now, before the agent can add events: Rewind finds the turn's snapshot by this number.
	meta := t.snapshot()
	turn := turnRef(meta.Id, meta.LastSeq-1)
	go func() {
		// What the folder holds before the agent touches it, to tell afterwards what the turn changed.
		before, _ := snapshot(context.Background(), meta.Cwd, turn+"-before")
		// Every way a turn can end, including the process dying, lands here.
		stop, err := sess.Prompt(context.Background(), prompt)
		ended := &inabav1.TurnEnded{StopReason: stop}
		if r, ok := sess.(provider.TurnUsageReporter); ok {
			ended.Usage = r.LastTurnUsage()
		}
		if err != nil {
			ended.Error = err.Error()
			var turnErr *provider.TurnError
			if !errors.As(err, &turnErr) {
				// Not the agent refusing the turn: its process is gone.
				ended.Error = "the agent stopped before finishing this turn (" + err.Error() + ")"
				t.closeSession()
			}
		}
		t.emit(&inabav1.Event{Payload: &inabav1.Event_TurnEnded{TurnEnded: ended}})
		t.op.Lock()
		defer t.op.Unlock()
		if t.deleted {
			return
		}
		// Under the lock, so the next turn does not start changing files mid-snapshot.
		if before != "" {
			after, _ := snapshot(context.Background(), meta.Cwd, turn+"-after")
			if changes := turnChanges(context.Background(), meta.Cwd, before, after); changes != nil {
				t.emit(&inabav1.Event{Payload: &inabav1.Event_TurnChanges{TurnChanges: changes}})
			}
		}
		t.runNext()
	}()
	return nil
}

// runNext starts the turn for the message at the head of the queue, if the thread is
// free to take it. The caller holds t.op.
func (t *thread) runNext() {
	t.mu.Lock()
	ready := t.meta.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE && t.meta.Mode == inabav1.ThreadMode_THREAD_MODE_STRUCTURED &&
		!t.meta.QueuePaused && len(t.meta.Queue) > 0
	var next *inabav1.QueuedMessage
	if ready {
		next, t.meta.Queue = t.meta.Queue[0], t.meta.Queue[1:]
		t.save.meta(t.meta)
	}
	t.mu.Unlock()
	if next == nil {
		return
	}
	msg := &inabav1.UserMessage{Text: next.Text, Attachments: next.Attachments}
	if err := t.runTurn(context.Background(), msg); err != nil {
		// The agent could not be started. Say so in the log and keep the rest for later.
		t.emit(&inabav1.Event{Payload: &inabav1.Event_UserMessage{UserMessage: msg}})
		t.emit(&inabav1.Event{Payload: &inabav1.Event_TurnStarted{TurnStarted: &inabav1.TurnStarted{}}})
		t.emit(&inabav1.Event{Payload: &inabav1.Event_TurnEnded{TurnEnded: &inabav1.TurnEnded{Error: connectMessage(err)}}})
		t.pauseQueue()
	}
}

// connectMessage is the text of an error as its handler wrote it for a person.
func connectMessage(err error) string {
	var rpcErr *connect.Error
	if errors.As(err, &rpcErr) {
		return rpcErr.Message()
	}
	return err.Error()
}

// pauseQueue holds whatever is queued until someone gives a go-ahead.
func (t *thread) pauseQueue() {
	t.mu.Lock()
	t.meta.QueuePaused = len(t.meta.Queue) > 0
	t.save.meta(t.meta)
	t.mu.Unlock()
}

func (h *Host) Interrupt(ctx context.Context, req *inabav1.InterruptRequest) (*inabav1.InterruptResponse, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	sess, err := h.session(req.ThreadId)
	if err != nil {
		return nil, err
	}
	// The user stopped the work, so what was lined up behind it waits for them too.
	t.pauseQueue()
	if req.Force {
		// Ending the process ends the turn: its Prompt call fails, which closes the turn in the log.
		t.closeSession()
		return &inabav1.InterruptResponse{}, nil
	}
	if err := sess.Interrupt(ctx); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error())
	}
	return &inabav1.InterruptResponse{}, nil
}

// applyOptions sets the thread's options to the wanted values, where they differ and
// the agent offers them. A value it no longer offers is skipped, not an error: what an
// agent offers changes between its versions and with the account it runs as.
func (t *thread) applyOptions(ctx context.Context, sess provider.Session, wanted map[string]string) {
	c, ok := sess.(provider.Configurable)
	if !ok {
		return
	}
	for _, now := range t.snapshot().Options {
		if value, ok := wanted[now.Id]; ok && now.Value != value && offers(now, value) {
			_ = c.SetOption(ctx, now.Id, value)
		}
	}
}

// learn returns what a thread of the provider calls when its agent's options change,
// so the host can offer them for the provider's next thread before it starts.
func (h *Host) learn(providerID string) func([]*inabav1.SessionOption) {
	return func(options []*inabav1.SessionOption) {
		h.mu.Lock()
		h.offered[providerID] = options
		h.mu.Unlock()
		h.save.offered(providerID, options)
	}
}

// offers reports whether a value is one the option can take.
func offers(o *inabav1.SessionOption, value string) bool {
	if o.Boolean {
		return value == "true" || value == "false"
	}
	return slices.ContainsFunc(o.Choices, func(c *inabav1.SessionOption_Choice) bool { return c.Value == value })
}

func (h *Host) SetOption(ctx context.Context, req *inabav1.SetOptionRequest) (*inabav1.Thread, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	sess, err := h.session(req.ThreadId)
	if err != nil {
		return nil, err
	}
	c, ok := sess.(provider.Configurable)
	if !ok {
		return nil, connect.NewError(connect.CodeUnimplemented, "this agent has no options to set")
	}
	meta := t.snapshot()
	i := slices.IndexFunc(meta.Options, func(o *inabav1.SessionOption) bool { return o.Id == req.OptionId })
	if i < 0 || !offers(meta.Options[i], req.Value) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the agent does not offer that choice")
	}
	if err := c.SetOption(ctx, req.OptionId, req.Value); err != nil {
		// The agent's own words say why it refused.
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the agent did not accept the change: "+err.Error())
	}
	return t.snapshot(), nil
}

func (h *Host) CancelQueued(_ context.Context, req *inabav1.CancelQueuedRequest) (*inabav1.Thread, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	before := len(t.meta.Queue)
	t.meta.Queue = slices.DeleteFunc(t.meta.Queue, func(q *inabav1.QueuedMessage) bool { return q.Id == req.QueuedId })
	found := len(t.meta.Queue) < before
	if len(t.meta.Queue) == 0 {
		t.meta.QueuePaused = false
	}
	t.save.meta(t.meta)
	t.mu.Unlock()
	if !found {
		// It ran, or was cancelled, in the meantime.
		return nil, connect.NewError(connect.CodeNotFound, "that message is no longer queued")
	}
	return t.snapshot(), nil
}

// queuedAt finds a waiting message, or says it is no longer waiting. The caller holds t.mu.
func (t *thread) queuedAt(id string) (int, error) {
	i := slices.IndexFunc(t.meta.Queue, func(q *inabav1.QueuedMessage) bool { return q.Id == id })
	if i < 0 {
		// It ran, or was cancelled, in the meantime.
		return 0, connect.NewError(connect.CodeNotFound, "that message is no longer queued")
	}
	return i, nil
}

func (h *Host) EditQueued(_ context.Context, req *inabav1.EditQueuedRequest) (*inabav1.Thread, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	i, err := t.queuedAt(req.QueuedId)
	if err != nil {
		return nil, err
	}
	q := t.meta.Queue[i]
	if strings.TrimSpace(req.Text) == "" && len(q.Attachments) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the message would be empty; cancel it instead")
	}
	q.Text = req.Text
	t.save.meta(t.meta)
	return proto.Clone(t.meta).(*inabav1.Thread), nil
}

func (h *Host) MoveQueued(_ context.Context, req *inabav1.MoveQueuedRequest) (*inabav1.Thread, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	i, err := t.queuedAt(req.QueuedId)
	if err != nil {
		return nil, err
	}
	q := t.meta.Queue[i]
	rest := slices.Delete(t.meta.Queue, i, i+1)
	to := min(int(req.ToIndex), len(rest))
	t.meta.Queue = slices.Insert(rest, to, q)
	t.save.meta(t.meta)
	return proto.Clone(t.meta).(*inabav1.Thread), nil
}

func (h *Host) SendQueuedNow(ctx context.Context, req *inabav1.SendQueuedNowRequest) (*inabav1.Thread, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	t.op.Lock()
	defer t.op.Unlock()
	sess, err := t.structured()
	if err != nil {
		return nil, err
	}
	if t.deleted {
		return nil, connect.NewError(connect.CodeNotFound, "no thread "+req.ThreadId)
	}
	t.mu.Lock()
	i, err := t.queuedAt(req.QueuedId)
	if err != nil {
		t.mu.Unlock()
		return nil, err
	}
	q := t.meta.Queue[i]
	t.meta.Queue = slices.Insert(slices.Delete(t.meta.Queue, i, i+1), 0, q)
	// Asking for it to run is a go-ahead, as sending a message is.
	t.meta.QueuePaused = false
	busy := t.meta.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE
	t.save.meta(t.meta)
	t.mu.Unlock()
	switch {
	case !busy:
		t.runNext()
	case sess != nil:
		// The turn's end runs the head of the queue, which is now this message.
		_ = sess.Interrupt(ctx)
	}
	return t.snapshot(), nil
}

func (h *Host) ResumeQueue(_ context.Context, req *inabav1.ResumeQueueRequest) (*inabav1.Thread, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	t.op.Lock()
	defer t.op.Unlock()
	if _, err := t.structured(); err != nil {
		return nil, err
	}
	if t.deleted {
		return nil, connect.NewError(connect.CodeNotFound, "no thread "+req.ThreadId)
	}
	t.mu.Lock()
	t.meta.QueuePaused = false
	t.save.meta(t.meta)
	t.mu.Unlock()
	t.runNext()
	return t.snapshot(), nil
}

func (h *Host) Respond(ctx context.Context, req *inabav1.RespondRequest) (*inabav1.RespondResponse, error) {
	sess, err := h.session(req.ThreadId)
	if err != nil {
		return nil, err
	}
	if err := sess.Respond(ctx, req.RequestId, req.OptionId); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err.Error())
	}
	return &inabav1.RespondResponse{}, nil
}

func (h *Host) Raw(ctx context.Context, req *inabav1.RawRequest) (*inabav1.RawResponse, error) {
	sess, err := h.session(req.ThreadId)
	if err != nil {
		return nil, err
	}
	raw, ok := sess.(provider.RawCaller)
	if !ok {
		return nil, connect.NewError(connect.CodeUnimplemented, "this provider has no raw calls")
	}
	params, err := protojson.Marshal(req.Params)
	if err != nil || req.Params == nil {
		params = json.RawMessage("null")
	}
	res, err := raw.Raw(ctx, req.Method, params)
	if err != nil {
		// The provider's own error text is the point of a raw call.
		return nil, connect.NewError(connect.CodeUnknown, err.Error())
	}
	return &inabav1.RawResponse{Result: provider.JSONValue(res)}, nil
}

// thread returns the live structured thread of a thread.
func (h *Host) session(threadID string) (provider.Session, error) {
	t, err := h.thread(threadID)
	if err != nil {
		return nil, err
	}
	sess, err := t.structured()
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the thread has no running process; start a turn")
	}
	return sess, nil
}

func (h *Host) SetMode(ctx context.Context, req *inabav1.SetModeRequest) (*inabav1.Thread, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	t.op.Lock()
	defer t.op.Unlock()
	meta := t.snapshot()
	if meta.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "a turn is running; wait for it or interrupt it first")
	}
	switch req.Mode {
	case inabav1.ThreadMode_THREAD_MODE_TERMINAL:
		t.mu.Lock()
		alive := t.term != nil && !t.term.Exited()
		t.mu.Unlock()
		if meta.Mode == req.Mode && alive {
			return meta, nil
		}
		// Check before stopping anything, so a provider without a CLI keeps its session.
		if _, err := t.terminalArgv(); err != nil {
			return nil, err
		}
		t.closeSession()
		t.closeTerminal()
		if err := t.startTerminal(); err != nil {
			return nil, err
		}
	case inabav1.ThreadMode_THREAD_MODE_STRUCTURED:
		if meta.Mode == req.Mode {
			return meta, nil
		}
		if _, terminalOnly := t.prov.(provider.TerminalOnly); terminalOnly {
			return nil, connect.NewError(connect.CodeFailedPrecondition, meta.Provider+" threads run only in the terminal")
		}
		t.closeTerminal()
		if err := t.openSession(ctx); err != nil {
			return nil, err
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, "mode must be structured or terminal")
	}
	if meta.Mode != req.Mode {
		t.emit(&inabav1.Event{Payload: &inabav1.Event_ModeChanged{ModeChanged: &inabav1.ModeChanged{Mode: req.Mode}}})
	}
	return t.snapshot(), nil
}

func (h *Host) ListEvents(_ context.Context, req *inabav1.ListEventsRequest) (*inabav1.ListEventsResponse, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 1000
	}
	evs, _ := t.after(req.AfterSeq, limit)
	return &inabav1.ListEventsResponse{Events: evs}, nil
}

func (h *Host) Subscribe(ctx context.Context, req *inabav1.SubscribeRequest, stream inabav1connect.ThreadServiceSubscribeServerStream) error {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return err
	}
	seq := req.AfterSeq
	for {
		evs, changed := t.after(seq, 0)
		for _, e := range evs {
			if err := stream.Send(e); err != nil {
				return err
			}
			seq = e.Seq
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil
		}
	}
}

// Trusted marks a context as a call from this machine's own administrator: the local
// control socket, or code in this process. Such calls skip the permission checks.
func Trusted(ctx context.Context) context.Context { return rpc.Trusted(ctx) }

func may(ctx context.Context, permission string) error { return rpc.May(ctx, permission) }
