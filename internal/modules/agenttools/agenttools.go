// Package agenttools is the agent tools module: an MCP server that lets a thread's
// agent work with inaba itself, handing work to new threads and reading their answers.
package agenttools

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/module"
	"github.com/Alex4386/inaba/pkg/provider"
)

// Module is the agent tools module, for a host to run.
var Module = module.Module{
	Name:        "agent-tools",
	Title:       "Tools for agents",
	Description: "Lets each thread's agent hand work to new threads, follow them and read their answers, through an MCP server of inaba's.",
	Default:     true,
	Option: fx.Options(
		fx.Provide(newService),
		module.Provide((*service).contribution),
		fx.Invoke(func(lc fx.Lifecycle, s *service) { lc.Append(fx.StartStopHook(s.start, s.stop)) }),
	),
}

const (
	// Labels a thread started through these tools carries.
	parentLabel = "parent"
	depthLabel  = "depth"
	// How deep threads may hand work on: a thread started by a thread started by one.
	maxDepth = 3
	// How many of its threads a thread may have busy at once.
	maxBusyChildren = 5
	// How long wait_for_thread waits by default, and at most. MCP clients give up on
	// a call after a while; an agent calls again for longer.
	defaultWait, maxWait = 120 * time.Second, 600 * time.Second
)

type service struct {
	threads  module.Threads
	listener net.Listener
	token    string
	handler  http.Handler
}

func newService(threads module.Threads) *service {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	s := &service{threads: threads, token: hex.EncodeToString(b)}
	s.handler = mcp.NewStreamableHTTPHandler(s.serverFor, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return s
}

func (s *service) start() error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("could not open the agent tools' address: %w", err)
	}
	s.listener = l
	go func() { _ = http.Serve(l, s.routes()) }()
	return nil
}

func (s *service) stop() {
	if s.listener != nil {
		_ = s.listener.Close()
	}
}

func (s *service) contribution() module.Contribution {
	return module.Contribution{MCPServers: func(thread *inabav1.Thread) []provider.MCPServer {
		return []provider.MCPServer{{Name: "inaba", URL: s.endpoint(thread.Id)}}
	}}
}

// endpoint is where the thread's agent reaches its tools. The path carries the thread,
// which the tools act for, and a token that keeps other local users out.
func (s *service) endpoint(threadID string) string {
	return "http://" + s.listener.Addr().String() + "/" + s.token + "/" + threadID + "/mcp"
}

func (s *service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/{token}/{thread}/mcp", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.PathValue("token")), []byte(s.token)) != 1 {
			http.NotFound(w, r)
			return
		}
		s.handler.ServeHTTP(w, r)
	})
	return mux
}

// serverFor is the MCP server for one call, acting for the thread in its path.
func (s *service) serverFor(r *http.Request) *mcp.Server {
	t := &tools{threads: s.threads, me: r.PathValue("thread")}
	server := mcp.NewServer(&mcp.Implementation{Name: "inaba", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "list_threads", Description: "List the inaba threads in your project, with what each is doing. Yours is marked."}, t.list)
	mcp.AddTool(server, &mcp.Tool{Name: "start_thread", Description: "Start a new inaba thread and give it a task. It works on its own, in parallel with you; follow it with wait_for_thread. It runs in your project, in your folder unless given a worktree of its own."}, t.start)
	mcp.AddTool(server, &mcp.Tool{Name: "send_message", Description: "Send a message to a thread you started. If it is busy, the message waits its turn."}, t.send)
	mcp.AddTool(server, &mcp.Tool{Name: "wait_for_thread", Description: "Wait until a thread finishes its turn or asks a question, then get its answer. If it is still working when the wait ends, call again."}, t.wait)
	mcp.AddTool(server, &mcp.Tool{Name: "read_thread", Description: "Read a thread's latest turns: what it was asked and what it answered."}, t.read)
	mcp.AddTool(server, &mcp.Tool{Name: "stop_thread", Description: "Stop the turn a thread you started is running."}, t.stop)
	return server
}

// tools are the tools for one call, acting for the thread me.
type tools struct {
	threads module.Threads
	me      string
}

func trusted() context.Context { return rpc.Trusted(context.Background()) }

// get finds a thread the caller may see: one in its own project, or its own children.
func (t *tools) get(id string) (*inabav1.Thread, *inabav1.Thread, error) {
	me, err := t.threads.GetThread(trusted(), &inabav1.GetThreadRequest{ThreadId: t.me})
	if err != nil {
		return nil, nil, errors.New("this thread is no longer known to inaba")
	}
	if id == "" {
		return me, me, nil
	}
	th, err := t.threads.GetThread(trusted(), &inabav1.GetThreadRequest{ThreadId: id})
	if err != nil || (th.ProjectId != me.ProjectId && th.Labels[parentLabel] != me.Id) {
		return nil, nil, fmt.Errorf("no thread %s that you can see", id)
	}
	return me, th, nil
}

// mine finds a thread the caller started.
func (t *tools) mine(id string) (*inabav1.Thread, error) {
	_, th, err := t.get(id)
	if err != nil {
		return nil, err
	}
	if th.Labels[parentLabel] != t.me {
		return nil, fmt.Errorf("thread %s is not one you started; you can only read it", id)
	}
	return th, nil
}

type thread struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status" jsonschema:"idle, running or waiting (for an answer to a question)"`
	Provider string `json:"provider"`
	Yours    bool   `json:"yours,omitempty" jsonschema:"you started it"`
	You      bool   `json:"you,omitempty" jsonschema:"this is your own thread"`
}

func (t *tools) describe(th *inabav1.Thread) thread {
	status := strings.ToLower(strings.TrimPrefix(th.Status.String(), "THREAD_STATUS_"))
	return thread{ID: th.Id, Title: th.Title, Status: status, Provider: th.Provider, Yours: th.Labels[parentLabel] == t.me, You: th.Id == t.me}
}

type listIn struct{}
type listOut struct {
	Threads []thread `json:"threads"`
}

func (t *tools) list(_ context.Context, _ *mcp.CallToolRequest, _ listIn) (*mcp.CallToolResult, listOut, error) {
	me, _, err := t.get("")
	if err != nil {
		return nil, listOut{}, err
	}
	all, err := t.threads.ListThreads(trusted(), &inabav1.ListThreadsRequest{})
	if err != nil {
		return nil, listOut{}, err
	}
	out := listOut{Threads: []thread{}}
	for _, th := range all.Threads {
		if th.ProjectId == me.ProjectId || th.Labels[parentLabel] == me.Id {
			out.Threads = append(out.Threads, t.describe(th))
		}
	}
	return nil, out, nil
}

type startIn struct {
	Message  string `json:"message" jsonschema:"the task: what the new thread should do, with everything it needs to know"`
	Title    string `json:"title,omitempty" jsonschema:"a short name for the thread"`
	Provider string `json:"provider,omitempty" jsonschema:"the agent to run it; yours when left out"`
	Worktree bool   `json:"worktree,omitempty" jsonschema:"give it a git worktree and branch of its own, so its changes stay apart from yours"`
}
type startOut struct {
	ID string `json:"id"`
}

func (t *tools) start(_ context.Context, _ *mcp.CallToolRequest, in startIn) (*mcp.CallToolResult, startOut, error) {
	me, _, err := t.get("")
	if err != nil {
		return nil, startOut{}, err
	}
	if strings.TrimSpace(in.Message) == "" {
		return nil, startOut{}, errors.New("give the thread a message: its task")
	}
	depth, _ := strconv.Atoi(me.Labels[depthLabel])
	if depth+1 > maxDepth {
		return nil, startOut{}, fmt.Errorf("threads hand work on at most %d deep; do this one yourself", maxDepth)
	}
	all, err := t.threads.ListThreads(trusted(), &inabav1.ListThreadsRequest{})
	if err != nil {
		return nil, startOut{}, err
	}
	busy := 0
	for _, th := range all.Threads {
		if th.Labels[parentLabel] == me.Id && th.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE {
			busy++
		}
	}
	if busy >= maxBusyChildren {
		return nil, startOut{}, fmt.Errorf("you have %d threads at work already; wait for some to finish", busy)
	}
	// A thread it starts talks the language it does.
	create := &inabav1.CreateThreadRequest{Provider: in.Provider, ProjectId: me.ProjectId, Title: in.Title, Language: me.Language}
	if create.Provider == "" {
		create.Provider = me.Provider
	}
	if me.ProjectId == "" {
		create.Cwd = me.Cwd
	}
	if in.Worktree {
		create.Worktree = &inabav1.Worktree{}
	}
	th, err := t.threads.CreateThread(trusted(), create)
	if err != nil {
		return nil, startOut{}, errors.New(message(err))
	}
	_ = t.threads.SetLabel(th.Id, parentLabel, me.Id)
	_ = t.threads.SetLabel(th.Id, depthLabel, strconv.Itoa(depth+1))
	if _, err := t.threads.StartTurn(trusted(), &inabav1.StartTurnRequest{ThreadId: th.Id, Text: in.Message}); err != nil {
		return nil, startOut{}, errors.New(message(err))
	}
	return nil, startOut{ID: th.Id}, nil
}

type sendIn struct {
	ID      string `json:"id" jsonschema:"the thread's id"`
	Message string `json:"message"`
}
type sendOut struct {
	Queued bool `json:"queued" jsonschema:"the thread was busy; the message runs after what it is doing"`
}

func (t *tools) send(_ context.Context, _ *mcp.CallToolRequest, in sendIn) (*mcp.CallToolResult, sendOut, error) {
	th, err := t.mine(in.ID)
	if err != nil {
		return nil, sendOut{}, err
	}
	res, err := t.threads.StartTurn(trusted(), &inabav1.StartTurnRequest{ThreadId: th.Id, Text: in.Message})
	if err != nil {
		return nil, sendOut{}, errors.New(message(err))
	}
	return nil, sendOut{Queued: res.QueuedId != ""}, nil
}

type waitIn struct {
	ID      string `json:"id" jsonschema:"the thread's id"`
	Seconds int    `json:"seconds,omitempty" jsonschema:"how long to wait at most; 120 when left out, 600 at most"`
}
type waitOut struct {
	thread
	// What it answered in its last turn, once that turn is over.
	Answer string `json:"answer,omitempty"`
	// What it is asking, when it waits for an answer.
	Question string `json:"question,omitempty"`
	Error    string `json:"error,omitempty" jsonschema:"why its last turn failed"`
}

func (t *tools) wait(ctx context.Context, _ *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, waitOut, error) {
	if _, _, err := t.get(in.ID); err != nil {
		return nil, waitOut{}, err
	}
	limit := defaultWait
	if in.Seconds > 0 {
		limit = min(time.Duration(in.Seconds)*time.Second, maxWait)
	}
	deadline := time.Now().Add(limit)
	for {
		th, err := t.threads.GetThread(trusted(), &inabav1.GetThreadRequest{ThreadId: in.ID})
		if err != nil {
			return nil, waitOut{}, errors.New(message(err))
		}
		if th.Status != inabav1.ThreadStatus_THREAD_STATUS_RUNNING || time.Now().After(deadline) {
			out := waitOut{thread: t.describe(th)}
			turns, err := t.turns(in.ID, 1)
			if err == nil && len(turns) > 0 {
				last := turns[len(turns)-1]
				out.Question, out.Error = last.Question, last.Error
				if th.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE {
					out.Answer = last.Answer
				}
			}
			return nil, out, nil
		}
		select {
		case <-ctx.Done():
			return nil, waitOut{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type turn struct {
	Asked    string `json:"asked"`
	Answer   string `json:"answer,omitempty"`
	Question string `json:"question,omitempty" jsonschema:"a question it asked that is still open"`
	Error    string `json:"error,omitempty"`
}

type readIn struct {
	ID    string `json:"id" jsonschema:"the thread's id"`
	Turns int    `json:"turns,omitempty" jsonschema:"how many of the latest turns; 1 when left out"`
}
type readOut struct {
	Turns []turn `json:"turns"`
}

func (t *tools) read(_ context.Context, _ *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, readOut, error) {
	if _, _, err := t.get(in.ID); err != nil {
		return nil, readOut{}, err
	}
	turns, err := t.turns(in.ID, max(1, min(in.Turns, 20)))
	if err != nil {
		return nil, readOut{}, err
	}
	return nil, readOut{Turns: turns}, nil
}

// turns is the thread's latest n turns: what it was asked, said and asked back.
// shortcut: turns taken back by a rewind still show; filter on Rewound events if agents trip on them.
func (t *tools) turns(id string, n int) ([]turn, error) {
	evs, err := t.threads.ListEvents(trusted(), &inabav1.ListEventsRequest{ThreadId: id})
	if err != nil {
		return nil, errors.New(message(err))
	}
	var out []turn
	open := map[string]bool{}
	for _, e := range evs.Events {
		switch p := e.Payload.(type) {
		case *inabav1.Event_UserMessage:
			out = append(out, turn{Asked: p.UserMessage.Text})
		case *inabav1.Event_TextDelta:
			if len(out) > 0 && p.TextDelta.Kind != inabav1.TextDelta_KIND_THOUGHT {
				out[len(out)-1].Answer += p.TextDelta.Text
			}
		case *inabav1.Event_PermissionRequest:
			open[p.PermissionRequest.RequestId] = true
			if len(out) > 0 {
				out[len(out)-1].Question = p.PermissionRequest.Title
			}
		case *inabav1.Event_PermissionResolved:
			delete(open, p.PermissionResolved.RequestId)
			if len(out) > 0 && len(open) == 0 {
				out[len(out)-1].Question = ""
			}
		case *inabav1.Event_TurnEnded:
			if len(out) > 0 {
				out[len(out)-1].Error = p.TurnEnded.Error
			}
		}
	}
	return out[max(0, len(out)-n):], nil
}

type stopIn struct {
	ID string `json:"id" jsonschema:"the thread's id"`
}
type stopOut struct{}

func (t *tools) stop(_ context.Context, _ *mcp.CallToolRequest, in stopIn) (*mcp.CallToolResult, stopOut, error) {
	th, err := t.mine(in.ID)
	if err != nil {
		return nil, stopOut{}, err
	}
	if _, err := t.threads.Interrupt(trusted(), &inabav1.InterruptRequest{ThreadId: th.Id}); err != nil {
		return nil, stopOut{}, errors.New(message(err))
	}
	return nil, stopOut{}, nil
}

// message is an error as a person reads it.
func message(err error) string {
	var rpcErr *connect.Error
	if errors.As(err, &rpcErr) {
		return rpcErr.Message()
	}
	return err.Error()
}
