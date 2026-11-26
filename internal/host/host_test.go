package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/testdir"
	"github.com/Alex4386/inaba/pkg/provider"
)

type fakeProvider struct{}

func (fakeProvider) ID() string { return "fake" }
func (fakeProvider) TerminalCommand(string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/d", "/k", "echo term-ready"}
	}
	return []string{"sh", "-c", "echo term-ready; cat"}
}
func (fakeProvider) Open(_ context.Context, cfg provider.Config, emit provider.Emit) (provider.Session, error) {
	s := &fakeSession{emit: emit, cwd: cfg.Cwd, resumed: cfg.NativeSessionID, stop: make(chan struct{}, 1), killed: make(chan struct{}, 1)}
	// Like a real agent, it starts on its default model and says what it offers.
	s.report("opus")
	return s, nil
}

type fakeSession struct {
	emit    provider.Emit
	cwd     string
	resumed string
	stop    chan struct{}
	killed  chan struct{}
}

func (s *fakeSession) NativeSessionID() string { return "native-1" }

func (s *fakeSession) report(model string) {
	s.emit(&inabav1.Event{Payload: &inabav1.Event_OptionsChanged{OptionsChanged: &inabav1.OptionsChanged{Options: []*inabav1.SessionOption{{
		Id: "model", Name: "Model", Category: "model", Value: model,
		Choices: []*inabav1.SessionOption_Choice{{Value: "opus", Name: "Opus"}, {Value: "haiku", Name: "Haiku"}},
	}}}}})
}

func (s *fakeSession) SetOption(_ context.Context, _, value string) error {
	s.report(value)
	return nil
}
func (s *fakeSession) Prompt(_ context.Context, p provider.Prompt) (string, error) {
	text := p.Text
	if text == "hang" {
		s.emit(&inabav1.Event{Payload: &inabav1.Event_ToolCall{ToolCall: &inabav1.ToolCall{Id: "t", Title: "Run tests"}}})
		select {
		case <-s.stop:
			return "cancelled", nil
		case <-s.killed:
			return "", errors.New("process exited")
		}
	}
	if text == "fail" {
		// The agent refusing the turn; its process lives on.
		return "", &provider.TurnError{Err: errors.New("refused")}
	}
	if name, ok := strings.CutPrefix(text, "title:"); ok {
		s.emit(&inabav1.Event{Payload: &inabav1.Event_ThreadTitle{ThreadTitle: &inabav1.ThreadTitle{Title: name}}})
	}
	if text == "edit" {
		// An agent at work: one file changed, one made, one removed.
		os.WriteFile(filepath.Join(s.cwd, "a.txt"), []byte("one\nCHANGED\n"), 0o644)
		os.WriteFile(filepath.Join(s.cwd, "new.txt"), []byte("fresh\n"), 0o644)
		os.Remove(filepath.Join(s.cwd, "b.txt"))
	}
	// Says what it was given to read, so a test can tell the files reached it.
	for _, f := range p.Files {
		data, _ := os.ReadFile(f)
		s.emit(&inabav1.Event{Payload: &inabav1.Event_TextDelta{TextDelta: &inabav1.TextDelta{Text: "file:" + f + "=" + string(data) + "\n"}}})
	}
	s.emit(&inabav1.Event{Payload: &inabav1.Event_TextDelta{TextDelta: &inabav1.TextDelta{Text: "echo " + text + " resumed=" + s.resumed}}})
	return "end_turn", nil
}
func (s *fakeSession) Interrupt(context.Context) error               { s.stop <- struct{}{}; return nil }
func (s *fakeSession) Respond(context.Context, string, string) error { return nil }
func (s *fakeSession) Close() error {
	// A killed agent fails whatever turn it was in.
	select {
	case s.killed <- struct{}{}:
	default:
	}
	return nil
}

// asServer stands in for inaba-server relaying a member of the host, who may do everything on
// it, told as inaba-server tells it, the old name for settings and housekeeping included.
func asServer(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(rpc.PermissionsHeader, strings.Join([]string{rpc.PermProjects, rpc.PermThreadsAnywhere, rpc.PermAutomations, rpc.PermHostSettings, rpc.PermHousekeeping, rpc.PermHosts}, ","))
		h.ServeHTTP(w, r)
	})
}

func setup(t *testing.T) (*httptest.Server, inabav1connect.ThreadServiceClient, inabav1connect.TerminalServiceClient) {
	h := New(fakeProvider{})
	handler, err := h.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(asServer(handler))
	t.Cleanup(srv.Close)
	t.Cleanup(h.Close)
	client := connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
	return srv, inabav1connect.NewThreadServiceClient(client), inabav1connect.NewTerminalServiceClient(client)
}

func TestTurnEventsOverConnectAndREST(t *testing.T) {
	srv, threads, _ := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create over REST, to cover the path mapping too.
	body, _ := json.Marshal(map[string]string{"provider": "fake", "cwd": t.TempDir()})
	res, err := http.Post(srv.URL+"/v1/threads", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created struct{ ID, NativeSessionID string }
	raw, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" || created.NativeSessionID != "native-1" {
		t.Fatalf("create over REST: %d %s", res.StatusCode, raw)
	}

	if _, err := threads.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: created.ID, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	// connect-go answers a stream's headers with its first message, so subscribe once
	// there is one; the log replays whatever was missed.
	stream, err := threads.Subscribe(ctx, &inabav1.SubscribeRequest{ThreadId: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var kinds []string
	for i := uint64(1); ; i++ {
		ev, err := stream.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if ev.Seq != i {
			t.Fatalf("seq = %d, want %d", ev.Seq, i)
		}
		switch ev.Payload.(type) {
		case *inabav1.Event_UserMessage:
			kinds = append(kinds, "user")
		case *inabav1.Event_TurnStarted:
			kinds = append(kinds, "started")
		case *inabav1.Event_TextDelta:
			kinds = append(kinds, ev.GetTextDelta().Text)
		case *inabav1.Event_TurnEnded:
			kinds = append(kinds, "ended:"+ev.GetTurnEnded().StopReason)
		}
		if ev.GetTurnEnded() != nil {
			break
		}
	}
	if got := strings.Join(kinds, ","); got != "user,started,echo hi resumed=,ended:end_turn" {
		t.Fatalf("events = %s", got)
	}

	// A late reader gets the same events from the log, from where it left off.
	res, err = http.Get(srv.URL + "/v1/threads/" + created.ID + "/events?after_seq=3")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(res.Body)
	if !strings.Contains(string(raw), `"turnEnded"`) || strings.Contains(string(raw), `"turnStarted"`) {
		t.Fatalf("events after 3: %s", raw)
	}
}

func TestRunningTurnBlocksModeSwitchThenInterrupt(t *testing.T) {
	_, threads, _ := setup(t)
	ctx := context.Background()
	th, err := threads.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threads.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hang"}); err != nil {
		t.Fatal(err)
	}
	if _, err := threads.SetMode(ctx, &inabav1.SetModeRequest{ThreadId: th.Id, Mode: inabav1.ThreadMode_THREAD_MODE_TERMINAL}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("mode switch during a turn: %v", err)
	}
	// While it runs, the thread says what it is about and what it is doing.
	var running *inabav1.Thread
	for range 100 {
		if running, _ = threads.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); running.Activity != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if running.Title != "hang" || running.Activity != "Run tests" || !running.UpdatedAt.AsTime().After(th.CreatedAt.AsTime()) {
		t.Fatalf("running thread = %v", running)
	}
	if _, err := threads.Interrupt(ctx, &inabav1.InterruptRequest{ThreadId: th.Id}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, threads, th.Id)
	if done, _ := threads.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); done.Activity != "" || done.Title != "hang" {
		t.Fatalf("idle thread = %v", done)
	}
}

// A failed turn leaves the thread idle but marked failed, until the next turn starts.
func TestLastTurnFailed(t *testing.T) {
	_, threads, _ := setup(t)
	ctx := context.Background()
	th, _ := threads.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	turn := func(text string) *inabav1.Thread {
		t.Helper()
		if _, err := threads.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: text}); err != nil {
			t.Fatal(err)
		}
		waitIdle(t, threads, th.Id)
		got, _ := threads.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id})
		return got
	}
	if got := turn("fail"); !got.LastTurnFailed || got.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE {
		t.Fatalf("after a failed turn: %v", got)
	}
	if got := turn("ok"); got.LastTurnFailed {
		t.Fatalf("after a good turn: %v", got)
	}
	// Rewinding takes the failed turn back, and the mark with it.
	turn("fail")
	evs, _ := threads.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
	var seq uint64
	for _, e := range evs.Events {
		if e.GetUserMessage().GetText() == "fail" {
			seq = e.Seq
		}
	}
	res, err := threads.Rewind(ctx, &inabav1.RewindRequest{ThreadId: th.Id, Seq: seq})
	if err != nil || res.Thread.LastTurnFailed {
		t.Fatalf("after rewinding the failed turn: %v %v", res, err)
	}
}

func waitIdle(t *testing.T, threads inabav1connect.ThreadServiceClient, id string) {
	t.Helper()
	for range 100 {
		th, err := threads.GetThread(context.Background(), &inabav1.GetThreadRequest{ThreadId: id})
		if err != nil {
			t.Fatal(err)
		}
		if th.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("thread did not become idle")
}

func TestModeSwitch(t *testing.T) {
	_, threads, terms := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	th, err := threads.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := terms.Write(ctx, &inabav1.WriteRequest{ThreadId: th.Id, Data: []byte("x")}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("terminal write in structured mode: %v", err)
	}

	th, err = threads.SetMode(ctx, &inabav1.SetModeRequest{ThreadId: th.Id, Mode: inabav1.ThreadMode_THREAD_MODE_TERMINAL})
	if err != nil || th.Mode != inabav1.ThreadMode_THREAD_MODE_TERMINAL {
		t.Fatalf("to terminal: %v %v", th, err)
	}
	_, err = threads.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hi"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "terminal mode") {
		t.Fatalf("turn in terminal mode: %v", err)
	}

	out, err := terms.Attach(ctx, &inabav1.AttachRequest{ThreadId: th.Id})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	read := func(want string) {
		t.Helper()
		var got string
		for !strings.Contains(got, want) {
			chunk, err := out.Receive()
			if err != nil {
				t.Fatalf("waiting for %q, got %q: %v", want, got, err)
			}
			got += string(chunk.Data)
		}
	}
	read("term-ready")
	if _, err := terms.Write(ctx, &inabav1.WriteRequest{ThreadId: th.Id, Data: []byte("ping\n")}); err != nil {
		t.Fatal(err)
	}
	read("ping")

	th, err = threads.SetMode(ctx, &inabav1.SetModeRequest{ThreadId: th.Id, Mode: inabav1.ThreadMode_THREAD_MODE_STRUCTURED})
	if err != nil || th.Mode != inabav1.ThreadMode_THREAD_MODE_STRUCTURED {
		t.Fatalf("to structured: %v %v", th, err)
	}
	// The terminal is gone, and the new process resumed the same native session.
	// Output still in flight (the PTY echoes "ping" and cat prints it again) comes first.
	for i := 0; ; i++ {
		if _, err := out.Receive(); err != nil {
			break
		}
		if i == 10 {
			t.Fatal("terminal stream stayed open after leaving terminal mode")
		}
	}
	if _, err := threads.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "back"}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, threads, th.Id)
	evs, err := threads.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var modes int
	for _, e := range evs.Events {
		text += e.GetTextDelta().GetText()
		if e.GetModeChanged() != nil {
			modes++
		}
	}
	if text != "echo back resumed=native-1" || modes != 2 {
		t.Fatalf("text=%q mode changes=%d", text, modes)
	}
}

type shellProvider struct{ fakeProvider }

func (shellProvider) ID() string    { return "shell" }
func (shellProvider) TerminalOnly() {}
func (shellProvider) Open(context.Context, provider.Config, provider.Emit) (provider.Session, error) {
	panic("a terminal-only provider must never be opened")
}

func TestTerminalOnlyProviderStaysInTerminal(t *testing.T) {
	h := New(shellProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	// Structured is the default mode, but this provider has none.
	th, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "shell", Cwd: t.TempDir()})
	if err != nil || th.Mode != inabav1.ThreadMode_THREAD_MODE_TERMINAL {
		t.Fatalf("create: %v %v", th, err)
	}
	if _, err := h.SetMode(ctx, &inabav1.SetModeRequest{ThreadId: th.Id, Mode: inabav1.ThreadMode_THREAD_MODE_STRUCTURED}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("switch to structured: %v", err)
	}
	if _, err := h.Write(ctx, &inabav1.WriteRequest{ThreadId: th.Id, Data: []byte("x")}); err != nil {
		t.Fatalf("write to the terminal: %v", err)
	}
	ps, _ := h.ListProviders(ctx, nil)
	if p := ps.Providers[0]; p.Structured || !p.Terminal {
		t.Fatalf("provider = %v", p)
	}
}

func TestProjectsGroupThreadsAndSurviveRestart(t *testing.T) {
	path := filepath.Join(testdir.New(t), "inaba.db")
	dir := t.TempDir()
	ctx := Trusted(context.Background())

	h := New(fakeProvider{})
	if err := h.LoadProjects(path); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: "relative"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("project with a bad folder: %v", err)
	}
	// Browsing for the folder: sub-folders only, repositories marked, hidden ones left out.
	for _, sub := range []string{"app/.git", "docs", ".cache"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	listing, err := h.ListDirectory(ctx, &inabav1.ListDirectoryRequest{Path: dir})
	if err != nil || len(listing.Entries) != 2 || listing.Entries[0].Name != "app" || !listing.Entries[0].Git || listing.Entries[1].Git || listing.Parent != filepath.Dir(dir) {
		t.Fatalf("listing = %v, %v", listing, err)
	}
	if _, err := h.ListDirectory(context.Background(), &inabav1.ListDirectoryRequest{Path: dir}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("browsing without the permission: %v", err)
	}
	p, err := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: dir})
	if err != nil || p.Name != filepath.Base(dir) {
		t.Fatalf("create project: %v %v", p, err)
	}
	// A thread in a project starts in the project's folder, whatever cwd says.
	th, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: p.Id, Cwd: "/ignored"})
	if err != nil || th.Cwd != p.Cwd || th.ProjectId != p.Id {
		t.Fatalf("thread in project: %v %v", th, err)
	}
	if _, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: "nope"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("thread in an unknown project: %v", err)
	}
	h.Close()

	h = New(fakeProvider{})
	t.Cleanup(h.Close)
	if err := h.LoadProjects(path); err != nil {
		t.Fatal(err)
	}
	list, _ := h.ListProjects(ctx, nil)
	if len(list.Projects) != 1 || list.Projects[0].Id != p.Id {
		t.Fatalf("projects after restart: %v", list.Projects)
	}
	th, _ = h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: p.Id})
	if _, err := h.DeleteProject(ctx, &inabav1.DeleteProjectRequest{ProjectId: p.Id}); err != nil {
		t.Fatal(err)
	}
	// The thread outlives its project.
	if _, err := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); err != nil {
		t.Fatalf("thread after its project is deleted: %v", err)
	}
	if list, _ := h.ListProjects(ctx, nil); len(list.Projects) != 0 {
		t.Fatalf("projects after delete: %v", list.Projects)
	}
}

// A host that stops and starts again still has its threads, their history, and the
// native sessions to carry on in.
func TestThreadsSurviveARestart(t *testing.T) {
	path := filepath.Join(testdir.New(t), "inaba.db")
	ctx := Trusted(context.Background())
	dir := t.TempDir()
	wait := func(h *Host, id string) {
		t.Helper()
		for range 200 {
			if th, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: id}); th.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("thread did not become idle")
	}

	h := New(fakeProvider{})
	if err := h.OpenData(path); err != nil {
		t.Fatal(err)
	}
	done, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: dir})
	h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: done.Id, Text: "first"})
	wait(h, done.Id)
	if _, err := h.UpdateThread(ctx, &inabav1.UpdateThreadRequest{ThreadId: done.Id, Title: proto.String("renamed"), Pinned: proto.Bool(true)}); err != nil {
		t.Fatal(err)
	}
	// Archiving is a label too, and taking it off removes it.
	if got, err := h.UpdateThread(ctx, &inabav1.UpdateThreadRequest{ThreadId: done.Id, Archived: proto.Bool(true)}); err != nil || got.Labels["archived"] != "1" || got.Labels["pinned"] != "1" {
		t.Fatalf("archiving: %v %v", got.GetLabels(), err)
	}
	if got, _ := h.UpdateThread(ctx, &inabav1.UpdateThreadRequest{ThreadId: done.Id, Archived: proto.Bool(false)}); got.Labels["archived"] != "" {
		t.Fatalf("unarchiving: %v", got.Labels)
	}
	running, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: dir})
	h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: running.Id, Text: "hang"})
	term, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: dir, Mode: inabav1.ThreadMode_THREAD_MODE_TERMINAL})
	gone, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: dir})
	h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: gone.Id})
	before, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: done.Id})
	h.Close()

	h = New(fakeProvider{})
	t.Cleanup(h.Close)
	if err := h.OpenData(path); err != nil {
		t.Fatal(err)
	}
	list, _ := h.ListThreads(ctx, nil)
	if len(list.Threads) != 3 {
		t.Fatalf("threads after restart: %v", list.Threads)
	}
	th, err := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: done.Id})
	after, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: done.Id})
	if err != nil || th.Title != "renamed" || th.Labels["pinned"] != "1" || th.NativeSessionId != "native-1" || len(after.Events) != len(before.Events) || len(after.Events) == 0 {
		t.Fatalf("finished thread after restart: %v, %d events (had %d), %v", th, len(after.Events), len(before.Events), err)
	}
	// The next turn starts the agent again on the same native session and continues the log.
	h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: done.Id, Text: "second"})
	wait(h, done.Id)
	after, _ = h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: done.Id, AfterSeq: uint64(len(before.Events))})
	var text string
	for i, e := range after.Events {
		if e.Seq != uint64(len(before.Events)+i+1) {
			t.Fatalf("seq %d at position %d after %d", e.Seq, i, len(before.Events))
		}
		text += e.GetTextDelta().GetText()
	}
	if text != "echo second resumed=native-1" {
		t.Fatalf("turn after restart: %q", text)
	}

	// The turn that was running is closed in the log, and the thread can be used again.
	th, _ = h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: running.Id})
	evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: running.Id})
	if last := evs.Events[len(evs.Events)-1].GetTurnEnded(); th.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE || last == nil || last.Error == "" {
		t.Fatalf("interrupted thread after restart: %v, last event %v", th, evs.Events[len(evs.Events)-1])
	}

	// A terminal's process did not survive; it reads as exited and can be started again.
	if _, err := h.Write(ctx, &inabav1.WriteRequest{ThreadId: term.Id, Data: []byte("x")}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("write to a terminal from before the restart: %v", err)
	}
	if _, err := h.SetMode(ctx, &inabav1.SetModeRequest{ThreadId: term.Id, Mode: inabav1.ThreadMode_THREAD_MODE_TERMINAL}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Write(ctx, &inabav1.WriteRequest{ThreadId: term.Id, Data: []byte("x")}); err != nil {
		t.Fatalf("write to the restarted terminal: %v", err)
	}
}

// logLine is the thread's log as one line: who said what, and how each turn ended.
func logLine(t *testing.T, h *Host, id string) string {
	t.Helper()
	evs, err := h.ListEvents(Trusted(context.Background()), &inabav1.ListEventsRequest{ThreadId: id})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs.Events {
		switch p := e.Payload.(type) {
		case *inabav1.Event_UserMessage:
			out = append(out, "user:"+p.UserMessage.Text)
		case *inabav1.Event_TurnEnded:
			if p.TurnEnded.Error != "" {
				out = append(out, "failed")
			} else {
				out = append(out, "ended:"+p.TurnEnded.StopReason)
			}
		}
	}
	return strings.Join(out, " ")
}

func TestQueueRunsInOrderAndStopsWhenTold(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	send := func(text string, interrupt bool) string {
		t.Helper()
		res, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: text, Interrupt: interrupt})
		if err != nil {
			t.Fatal(err)
		}
		return res.QueuedId
	}
	// settled waits until nothing runs and nothing more will start by itself.
	settled := func() *inabav1.Thread {
		t.Helper()
		for range 300 {
			th, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id})
			if th.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE && (len(th.Queue) == 0 || th.QueuePaused) {
				return th
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("thread did not settle")
		return nil
	}

	// Messages sent during a turn wait their turn; one sent with interrupt goes first
	// and ends the running turn.
	if id := send("hang", false); id != "" {
		t.Fatalf("first message was queued as %q", id)
	}
	a := send("a", false)
	send("b", false)
	if a == "" {
		t.Fatal("message sent during a turn was not queued")
	}
	if _, err := h.CancelQueued(ctx, &inabav1.CancelQueuedRequest{ThreadId: th.Id, QueuedId: a}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CancelQueued(ctx, &inabav1.CancelQueuedRequest{ThreadId: th.Id, QueuedId: a}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("cancelling twice: %v", err)
	}
	send("urgent", true)
	settled()
	if got := logLine(t, h, th.Id); got != "user:hang ended:cancelled user:urgent ended:end_turn user:b ended:end_turn" {
		t.Fatalf("transcript = %s", got)
	}

	// Stop holds the queue; resuming lets it run.
	send("hang", false)
	send("later", false)
	if _, err := h.Interrupt(ctx, &inabav1.InterruptRequest{ThreadId: th.Id}); err != nil {
		t.Fatal(err)
	}
	if th := settled(); !th.QueuePaused || len(th.Queue) != 1 {
		t.Fatalf("after stop: paused=%v queue=%v", th.QueuePaused, th.Queue)
	}
	if _, err := h.ResumeQueue(ctx, &inabav1.ResumeQueueRequest{ThreadId: th.Id}); err != nil {
		t.Fatal(err)
	}
	settled()
	if got := logLine(t, h, th.Id); !strings.HasSuffix(got, "user:hang ended:cancelled user:later ended:end_turn") {
		t.Fatalf("transcript = %s", got)
	}

	// Force stop ends an agent that does not answer; the thread works again afterwards.
	send("hang", false)
	if _, err := h.Interrupt(ctx, &inabav1.InterruptRequest{ThreadId: th.Id, Force: true}); err != nil {
		t.Fatal(err)
	}
	settled()
	send("after", false)
	settled()
	if got := logLine(t, h, th.Id); !strings.HasSuffix(got, "user:hang failed user:after ended:end_turn") {
		t.Fatalf("transcript = %s", got)
	}
}

func TestQueuedMessagesCanBeEditedMovedAndSentNow(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	send := func(text string) string {
		t.Helper()
		res, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return res.QueuedId
	}
	texts := func(got *inabav1.Thread) string {
		var s []string
		for _, q := range got.Queue {
			s = append(s, q.Text)
		}
		return strings.Join(s, ",")
	}
	settled := func() {
		t.Helper()
		for range 300 {
			got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id})
			if got.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE && (len(got.Queue) == 0 || got.QueuePaused) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("thread did not settle")
	}

	send("hang")
	a, b, c := send("a"), send("b"), send("c")
	got, err := h.EditQueued(ctx, &inabav1.EditQueuedRequest{ThreadId: th.Id, QueuedId: a, Text: "A"})
	if err != nil || texts(got) != "A,b,c" {
		t.Fatalf("edit: %v %v", texts(got), err)
	}
	if _, err := h.EditQueued(ctx, &inabav1.EditQueuedRequest{ThreadId: th.Id, QueuedId: a, Text: " "}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("emptying a message: %v", err)
	}
	if got, err = h.MoveQueued(ctx, &inabav1.MoveQueuedRequest{ThreadId: th.Id, QueuedId: c, ToIndex: 0}); err != nil || texts(got) != "c,A,b" {
		t.Fatalf("move to front: %v %v", texts(got), err)
	}
	if got, err = h.MoveQueued(ctx, &inabav1.MoveQueuedRequest{ThreadId: th.Id, QueuedId: c, ToIndex: 99}); err != nil || texts(got) != "A,b,c" {
		t.Fatalf("move past the end: %v %v", texts(got), err)
	}
	for _, err := range []error{
		func() error {
			_, err := h.EditQueued(ctx, &inabav1.EditQueuedRequest{ThreadId: th.Id, QueuedId: "nope", Text: "x"})
			return err
		}(),
		func() error {
			_, err := h.MoveQueued(ctx, &inabav1.MoveQueuedRequest{ThreadId: th.Id, QueuedId: "nope"})
			return err
		}(),
		func() error {
			_, err := h.SendQueuedNow(ctx, &inabav1.SendQueuedNowRequest{ThreadId: th.Id, QueuedId: "nope"})
			return err
		}(),
	} {
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("unknown id: %v", err)
		}
	}
	// Sending b now stops the running turn and runs b before the rest.
	if _, err := h.SendQueuedNow(ctx, &inabav1.SendQueuedNowRequest{ThreadId: th.Id, QueuedId: b}); err != nil {
		t.Fatal(err)
	}
	settled()
	if got := logLine(t, h, th.Id); got != "user:hang ended:cancelled user:b ended:end_turn user:A ended:end_turn user:c ended:end_turn" {
		t.Fatalf("transcript = %s", got)
	}

	// On a thread that is not running, a held queue is reordered and sending one runs it.
	send("hang")
	x, y := send("x"), send("y")
	if _, err := h.Interrupt(ctx, &inabav1.InterruptRequest{ThreadId: th.Id}); err != nil {
		t.Fatal(err)
	}
	settled()
	if got, err = h.MoveQueued(ctx, &inabav1.MoveQueuedRequest{ThreadId: th.Id, QueuedId: y, ToIndex: 0}); err != nil || texts(got) != "y,x" || !got.QueuePaused {
		t.Fatalf("move while idle: %v %v", got, err)
	}
	if _, err := h.EditQueued(ctx, &inabav1.EditQueuedRequest{ThreadId: th.Id, QueuedId: x, Text: "X"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SendQueuedNow(ctx, &inabav1.SendQueuedNowRequest{ThreadId: th.Id, QueuedId: x}); err != nil {
		t.Fatal(err)
	}
	settled()
	if got := logLine(t, h, th.Id); !strings.HasSuffix(got, "user:hang ended:cancelled user:X ended:end_turn user:y ended:end_turn") {
		t.Fatalf("transcript = %s", got)
	}
}

func TestOptionChoiceOutlivesTheAgentProcess(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	if len(th.Options) != 1 || th.Options[0].Value != "opus" {
		t.Fatalf("options of a new thread = %v", th.Options)
	}
	if _, err := h.SetOption(ctx, &inabav1.SetOptionRequest{ThreadId: th.Id, OptionId: "model", Value: "gpt"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a choice the agent does not offer: %v", err)
	}
	th, err := h.SetOption(ctx, &inabav1.SetOptionRequest{ThreadId: th.Id, OptionId: "model", Value: "haiku"})
	if err != nil || th.Options[0].Value != "haiku" {
		t.Fatalf("set option: %v %v", th, err)
	}
	// Kill the agent. The next turn starts a new one, which comes up on its default
	// and is put back on what the user chose.
	h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hang"})
	h.Interrupt(ctx, &inabav1.InterruptRequest{ThreadId: th.Id, Force: true})
	waitStatus := func() {
		for range 200 {
			if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); got.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("thread did not become idle")
	}
	waitStatus()
	h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "again"})
	waitStatus()
	if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); got.Options[0].Value != "haiku" {
		t.Fatalf("model after the agent restarted = %q", got.Options[0].Value)
	}
	// With no agent running, as after a rewind, a choice is kept and put on the next one.
	h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hang"})
	h.Interrupt(ctx, &inabav1.InterruptRequest{ThreadId: th.Id, Force: true})
	waitStatus()
	if got, err := h.SetOption(ctx, &inabav1.SetOptionRequest{ThreadId: th.Id, OptionId: "model", Value: "opus"}); err != nil || got.Options[0].Value != "opus" {
		t.Fatalf("set option with no agent running: %v %v", got, err)
	}
	if _, err := h.SetOption(ctx, &inabav1.SetOptionRequest{ThreadId: th.Id, OptionId: "model", Value: "gpt"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("an unoffered choice with no agent running: %v", err)
	}
	h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "again"})
	waitStatus()
	if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); got.Options[0].Value != "opus" {
		t.Fatalf("model chosen while no agent ran, after it started = %q", got.Options[0].Value)
	}
}

// What one thread's agent offered can be chosen for the next thread before it
// starts, also after the host restarts.
func TestOptionsCanBeChosenBeforeASessionExists(t *testing.T) {
	path := filepath.Join(testdir.New(t), "inaba.db")
	ctx := Trusted(context.Background())
	h := New(fakeProvider{})
	if err := h.OpenData(path); err != nil {
		t.Fatal(err)
	}
	offered := func(h *Host) []*inabav1.SessionOption {
		list, _ := h.ListProviders(ctx, nil)
		return list.Providers[0].Options
	}
	if got := offered(h); len(got) != 0 {
		t.Fatalf("options before any thread ran: %v", got)
	}
	h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	if got := offered(h); len(got) != 1 || got[0].Id != "model" || len(got[0].Choices) != 2 {
		t.Fatalf("options after a thread ran: %v", got)
	}
	h.Close()

	h = New(fakeProvider{})
	t.Cleanup(h.Close)
	if err := h.OpenData(path); err != nil {
		t.Fatal(err)
	}
	if got := offered(h); len(got) != 1 {
		t.Fatalf("options after a restart: %v", got)
	}
	// A choice the agent no longer offers is skipped; the rest of the request stands.
	th, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir(), Options: map[string]string{"model": "haiku", "gone": "x"}})
	if err != nil || th.Options[0].Value != "haiku" {
		t.Fatalf("thread created with a chosen model: %v %v", th, err)
	}
	th, err = h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir(), Options: map[string]string{"model": "retired"}})
	if err != nil || th.Options[0].Value != "opus" {
		t.Fatalf("thread created with a model the agent dropped: %v %v", th, err)
	}
}

// A model the config hides is left out of what threads offer, unless a thread is on it.
func TestHiddenModelsAreNotOffered(t *testing.T) {
	h := New()
	h.SetProviders(nil, map[string]ProviderInfo{"p": {HiddenModels: []string{"b", "c"}}})
	options := []*inabav1.SessionOption{
		{Id: "model", Category: "model", Value: "c", Choices: []*inabav1.SessionOption_Choice{{Value: "a"}, {Value: "b"}, {Value: "c"}}},
		{Id: "mode", Category: "mode", Value: "b", Choices: []*inabav1.SessionOption_Choice{{Value: "a"}, {Value: "b"}}},
	}
	shown := h.shown("p", options)
	var got []string
	for _, c := range shown[0].Choices {
		got = append(got, c.Value)
	}
	if strings.Join(got, ",") != "a,c" || len(shown[1].Choices) != 2 || len(options[0].Choices) != 3 {
		t.Fatalf("models %v, modes %d, original %d", got, len(shown[1].Choices), len(options[0].Choices))
	}
}

// The machine's numbers come back; what a sandbox hides may be zero, but not the basics.
func TestGetSystemInfo(t *testing.T) {
	h := New()
	info, err := h.GetSystemInfo(context.Background(), &inabav1.GetSystemInfoRequest{})
	if err != nil || info.CpuCores < 1 || info.MemoryTotal == 0 || info.DiskTotal == 0 || info.InabaVersion == "" || info.CpuPercent < 0 || info.CpuPercent > 100 {
		t.Fatalf("%+v %v", info, err)
	}
}

// A project's settings are saved and come back with it; a pinned project comes first.
func TestUpdateProject(t *testing.T) {
	ctx := rpc.Trusted(context.Background())
	h := New()
	if err := h.LoadProjects(filepath.Join(testdir.New(t), "p.db")); err != nil {
		t.Fatal(err)
	}
	a, _ := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: t.TempDir()})
	b, _ := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: t.TempDir()})
	got, err := h.UpdateProject(ctx, &inabav1.UpdateProjectRequest{ProjectId: b.Id, Name: proto.String("second"), Pinned: proto.Bool(true),
		NewThread: &inabav1.NewThreadDefaults{Provider: "claude", Options: map[string]string{"model": "opus"}, Worktree: true}})
	if err != nil || got.Name != "second" || !got.Pinned || got.NewThread.GetOptions()["model"] != "opus" || !got.NewThread.GetWorktree() {
		t.Fatalf("%v %v", got, err)
	}
	list, _ := h.ListProjects(ctx, &inabav1.ListProjectsRequest{})
	if len(list.Projects) != 2 || list.Projects[0].Id != b.Id || list.Projects[1].Id != a.Id {
		t.Fatalf("order: %v", list.Projects)
	}
	// Put away and back: kept on the project, apart from everything else about it.
	if got, err := h.UpdateProject(ctx, &inabav1.UpdateProjectRequest{ProjectId: a.Id, Archived: proto.Bool(true)}); err != nil || !got.Archived || got.Name == "" {
		t.Fatalf("archiving: %v %v", got, err)
	}
	if got, _ := h.UpdateProject(ctx, &inabav1.UpdateProjectRequest{ProjectId: a.Id, Archived: proto.Bool(false)}); got.Archived {
		t.Fatal("unarchiving did not take")
	}
	if _, err := h.UpdateProject(ctx, &inabav1.UpdateProjectRequest{ProjectId: a.Id, Icon: proto.String("data:text/html;base64,PHNjcmlwdD4=")}); err == nil {
		t.Fatal("a picture that is not one was taken")
	}
	if _, err := h.UpdateProject(context.Background(), &inabav1.UpdateProjectRequest{ProjectId: a.Id, Name: proto.String("x")}); err == nil {
		t.Fatal("changed without the projects permission")
	}
}

// A thread's screen layout is kept with it and told to everyone following it, once per change.
func TestSetLayout(t *testing.T) {
	ctx := context.Background()
	h := New(fakeProvider{})
	th, _ := h.CreateThread(rpc.Trusted(ctx), &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	layout := `{"right":{"open":true,"tabs":["shell-1"]}}`
	got, err := h.SetLayout(ctx, &inabav1.SetLayoutRequest{ThreadId: th.Id, Layout: layout})
	if err != nil || got.Layout != layout {
		t.Fatalf("%v %v", got, err)
	}
	h.SetLayout(ctx, &inabav1.SetLayoutRequest{ThreadId: th.Id, Layout: layout})
	events, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
	n := 0
	for _, e := range events.Events {
		if e.GetLayoutChanged() != nil {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("layout events: %d", n)
	}
	if _, err := h.SetLayout(ctx, &inabav1.SetLayoutRequest{ThreadId: th.Id, Layout: strings.Repeat("x", maxLayout+1)}); err == nil {
		t.Fatal("an oversized layout was taken")
	}
}

// The agent's name for a thread replaces the first message as its title, but not a name someone gave it.
func TestAgentNamesSession(t *testing.T) {
	_, client, _ := setup(t)
	ctx := context.Background()
	th, err := client.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	turn := func(text string) string {
		t.Helper()
		if _, err := client.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: text}); err != nil {
			t.Fatal(err)
		}
		for range 200 {
			got, _ := client.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id})
			if got.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE && got.LastSeq > th.LastSeq {
				th = got
				return got.Title
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("the turn never ended")
		return ""
	}
	if got := turn("title:Reverse strings"); got != "Reverse strings" {
		t.Fatalf("after the agent named it: %q", got)
	}
	mine := "My thread"
	if _, err := client.UpdateThread(ctx, &inabav1.UpdateThreadRequest{ThreadId: th.Id, Title: &mine}); err != nil {
		t.Fatal(err)
	}
	if got := turn("title:Something else"); got != mine {
		t.Fatalf("a name someone gave was replaced: %q", got)
	}
}

// A thread nothing happened in for longer than the host's policy is archived; a pinned one is not,
// and only those allowed to look after the host set the policy.
func TestArchivePolicy(t *testing.T) {
	ctx := rpc.Trusted(context.Background())
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	if err := h.OpenData(filepath.Join(testdir.New(t), "inaba.db")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	quiet, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: dir})
	pinned, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: dir})
	h.UpdateThread(ctx, &inabav1.UpdateThreadRequest{ThreadId: pinned.Id, Pinned: proto.Bool(true)})
	if p, _ := h.GetArchivePolicy(ctx, &inabav1.GetArchivePolicyRequest{}); p.AfterDays != defaultArchiveDays {
		t.Fatalf("a host that never set a policy has %v", p)
	}
	if _, err := h.SetArchivePolicy(context.Background(), &inabav1.ArchivePolicy{AfterDays: 7}); err == nil {
		t.Fatal("the policy was set without the hosts permission")
	}
	if got, err := h.SetArchivePolicy(ctx, &inabav1.ArchivePolicy{AfterDays: 7}); err != nil || got.AfterDays != 7 {
		t.Fatalf("setting the policy: %v %v", got, err)
	}
	if n := h.archiveQuiet(time.Now()); n != 0 {
		t.Fatalf("archived %d threads that were just used", n)
	}
	if n := h.archiveQuiet(time.Now().Add(8 * 24 * time.Hour)); n != 1 {
		t.Fatalf("archived %d threads a week on; want the unpinned one", n)
	}
	got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: quiet.Id})
	kept, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: pinned.Id})
	if got.Labels["archived"] != "1" || kept.Labels["archived"] != "" {
		t.Fatalf("archived labels: quiet %v, pinned %v", got.Labels, kept.Labels)
	}
	if p, _ := h.GetArchivePolicy(ctx, &inabav1.GetArchivePolicyRequest{}); p.AfterDays != 7 {
		t.Fatalf("policy read back: %v", p)
	}

	// Writing in an archived thread brings it back, and its archived project with it.
	proj, _ := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: t.TempDir()})
	inside, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: proj.Id})
	h.UpdateThread(ctx, &inabav1.UpdateThreadRequest{ThreadId: inside.Id, Archived: proto.Bool(true)})
	h.UpdateProject(ctx, &inabav1.UpdateProjectRequest{ProjectId: proj.Id, Archived: proto.Bool(true)})
	if _, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: inside.Id, Text: "hello again"}); err != nil {
		t.Fatal(err)
	}
	back, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: inside.Id})
	projects, _ := h.ListProjects(ctx, &inabav1.ListProjectsRequest{})
	if back.Labels["archived"] != "" {
		t.Fatalf("the thread written in stayed archived: %v", back.Labels)
	}
	for _, p := range projects.Projects {
		if p.Id == proj.Id && p.Archived {
			t.Fatal("its project stayed archived")
		}
	}
}

// Opening a thread writes its layout; that must not make it recent or unread for everyone.
func TestEmitActivity(t *testing.T) {
	th := &thread{meta: &inabav1.Thread{Id: "t"}, changed: make(chan struct{})}
	th.emit(&inabav1.Event{Payload: &inabav1.Event_LayoutChanged{LayoutChanged: &inabav1.LayoutChanged{Layout: "docks"}}})
	if m := th.snapshot(); m.LastSeq != 1 || m.ActivitySeq != 0 || m.UpdatedAt != nil {
		t.Fatalf("after a layout change: last %d, activity %d, updated %v", m.LastSeq, m.ActivitySeq, m.UpdatedAt)
	}
	th.emit(&inabav1.Event{Payload: &inabav1.Event_UserMessage{UserMessage: &inabav1.UserMessage{Text: "hi"}}})
	if m := th.snapshot(); m.LastSeq != 2 || m.ActivitySeq != 2 || !proto.Equal(m.UpdatedAt, th.events[1].Time) {
		t.Fatalf("after a message: last %d, activity %d, updated %v", m.LastSeq, m.ActivitySeq, m.UpdatedAt)
	}
}

// The failed mark is worked out again from the log on start, so one a rewind left
// behind before rewinds cleared it goes, and a real failure stays.
func TestFailedMarkFollowsTheLogAfterARestart(t *testing.T) {
	path := filepath.Join(testdir.New(t), "inaba.db")
	ctx := Trusted(context.Background())
	h := New(fakeProvider{})
	if err := h.OpenData(path); err != nil {
		t.Fatal(err)
	}
	failTurn := func(id string) {
		t.Helper()
		h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: id, Text: "fail"})
		for range 200 {
			if th, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: id}); th.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("turn did not end")
	}
	stale, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	failed, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	failTurn(stale.Id)
	failTurn(failed.Id)
	evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: stale.Id})
	if _, err := h.Rewind(ctx, &inabav1.RewindRequest{ThreadId: stale.Id, Seq: evs.Events[slices.IndexFunc(evs.Events, func(e *inabav1.Event) bool { return e.GetUserMessage() != nil })].Seq}); err != nil {
		t.Fatal(err)
	}
	// As an older inaba saved it: rewound, yet still marked failed.
	th, _ := h.thread(stale.Id)
	th.mu.Lock()
	th.meta.LastTurnFailed = true
	th.save.meta(th.meta)
	th.mu.Unlock()
	h.Close()

	h = New(fakeProvider{})
	t.Cleanup(h.Close)
	if err := h.OpenData(path); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: stale.Id}); got.LastTurnFailed {
		t.Fatal("a rewound thread is still marked failed after a restart")
	}
	if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: failed.Id}); !got.LastTurnFailed {
		t.Fatal("a failed thread lost its mark after a restart")
	}
}
