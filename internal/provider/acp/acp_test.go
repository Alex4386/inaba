package acp

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/pkg/provider"
)

func providerConfig(t *testing.T, resume string) provider.Config {
	return provider.Config{Cwd: t.TempDir(), NativeSessionID: resume}
}

// The test binary doubles as the agent under test.
func TestMain(m *testing.M) {
	if os.Getenv("INABA_FAKE_ACP") == "1" {
		fakeAgent()
		return
	}
	os.Exit(m.Run())
}

// FakeProvider is the fake agent as a Provider; the host tests use it too.
func fakeProvider(t *testing.T) *Provider {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Provider{Name: "fake", Command: []string{exe}, Env: []string{"INABA_FAKE_ACP=1"}}
}

type recorder struct {
	mu     sync.Mutex
	events []*inabav1.Event
	notify chan struct{}
}

func (r *recorder) emit(e *inabav1.Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *recorder) waitFor(t *testing.T, match func(*inabav1.Event) bool) *inabav1.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		r.mu.Lock()
		for _, e := range r.events {
			if match(e) {
				r.mu.Unlock()
				return e
			}
		}
		r.mu.Unlock()
		select {
		case <-r.notify:
		case <-deadline:
			t.Fatal("event did not arrive")
		}
	}
}

func TestTurnWithPermissionAndUnknownUpdate(t *testing.T) {
	rec := &recorder{notify: make(chan struct{}, 1)}
	ctx := context.Background()
	sess, err := fakeProvider(t).Open(ctx, providerConfig(t, ""), rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.NativeSessionID() != "s1" {
		t.Fatalf("native session id = %q", sess.NativeSessionID())
	}

	done := make(chan string, 1)
	go func() {
		stop, err := sess.Prompt(ctx, provider.Prompt{Text: "hi"})
		if err != nil {
			t.Error(err)
		}
		done <- stop
	}()
	req := rec.waitFor(t, func(e *inabav1.Event) bool { return e.GetPermissionRequest() != nil }).GetPermissionRequest()
	if req.Title != "Run ls" || len(req.Options) != 1 || req.Options[0].Id != "yes" {
		t.Fatalf("permission request = %v", req)
	}
	if err := sess.Respond(ctx, req.RequestId, "yes"); err != nil {
		t.Fatal(err)
	}
	if stop := <-done; stop != "end_turn" {
		t.Fatalf("stop reason = %q", stop)
	}

	// The last update may land just after the turn's answer; wait for it, then read under the lock.
	rec.waitFor(t, func(e *inabav1.Event) bool { return e.GetRaw().GetMethod() == "session/update" })
	var text string
	var tool, raw, resolved bool
	rec.mu.Lock()
	for _, e := range rec.events {
		text += e.GetTextDelta().GetText()
		tool = tool || e.GetToolCall().GetId() == "t1"
		raw = raw || e.GetRaw().GetMethod() == "session/update"
		resolved = resolved || e.GetPermissionResolved().GetOptionId() == "yes"
	}
	// Raw below waits for updates still being delivered, which take the lock.
	rec.mu.Unlock()
	if text != "helloselected:yes" || !tool || !raw || !resolved {
		t.Fatalf("text=%q tool=%v raw=%v resolved=%v", text, tool, raw, resolved)
	}

	echoed, err := sess.(interface {
		Raw(context.Context, string, json.RawMessage) (json.RawMessage, error)
	}).Raw(ctx, "_inaba/echo", json.RawMessage(`{"a":1}`))
	if err != nil || string(echoed) != `{"a":1}` {
		t.Fatalf("raw = %s, %v", echoed, err)
	}
}

func TestInterruptEndsTurn(t *testing.T) {
	rec := &recorder{notify: make(chan struct{}, 1)}
	ctx := context.Background()
	sess, err := fakeProvider(t).Open(ctx, providerConfig(t, ""), rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	done := make(chan string, 1)
	go func() {
		stop, _ := sess.Prompt(ctx, provider.Prompt{Text: "hang"})
		done <- stop
	}()
	time.Sleep(50 * time.Millisecond)
	if err := sess.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case stop := <-done:
		if stop != "cancelled" {
			t.Fatalf("stop reason = %q", stop)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not end")
	}
}

func TestLoadDoesNotReplayHistory(t *testing.T) {
	rec := &recorder{notify: make(chan struct{}, 1)}
	sess, err := fakeProvider(t).Open(context.Background(), providerConfig(t, "s-old"), rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.NativeSessionID() != "s-old" || len(rec.events) != 0 {
		t.Fatalf("id=%q events=%v", sess.NativeSessionID(), rec.events)
	}
}

func TestSessionOptionsAreReportedAndSet(t *testing.T) {
	rec := &recorder{notify: make(chan struct{}, 1)}
	ctx := context.Background()
	sess, err := fakeProvider(t).Open(ctx, providerConfig(t, ""), rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	latest := func() []*inabav1.SessionOption {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		for i := len(rec.events) - 1; i >= 0; i-- {
			if o := rec.events[i].GetOptionsChanged(); o != nil {
				return o.Options
			}
		}
		return nil
	}
	opts := latest()
	if len(opts) != 2 || opts[0].Id != "model" || opts[0].Value != "opus" || opts[0].Category != "model" ||
		len(opts[0].Choices) != 2 || opts[0].Choices[1].Value != "haiku" || opts[0].Choices[1].Group != "Small" ||
		!opts[1].Boolean || opts[1].Value != "false" {
		t.Fatalf("options at start = %v", opts)
	}
	set := sess.(interface {
		SetOption(context.Context, string, string) error
	})
	if err := set.SetOption(ctx, "model", "haiku"); err != nil {
		t.Fatal(err)
	}
	if got := latest()[0].Value; got != "haiku" {
		t.Fatalf("model after set = %q", got)
	}
	if err := set.SetOption(ctx, "fast", "true"); err != nil {
		t.Fatal(err)
	}
	if got := latest()[1].Value; got != "true" {
		t.Fatalf("fast after set = %q", got)
	}
}

func TestPromptWithImagesAndFilesReportsCommandsAndUsage(t *testing.T) {
	rec := &recorder{notify: make(chan struct{}, 1)}
	ctx := context.Background()
	sess, err := fakeProvider(t).Open(ctx, providerConfig(t, ""), rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	cmds := rec.waitFor(t, func(e *inabav1.Event) bool { return e.GetCommandsChanged() != nil }).GetCommandsChanged().Commands
	if len(cmds) != 2 || cmds[0].Name != "review" || cmds[0].Hint != "what to look at" || cmds[1].Hint != "" {
		t.Fatalf("commands = %v", cmds)
	}
	_, err = sess.Prompt(ctx, provider.Prompt{Text: "look", Images: []provider.Image{{MimeType: "image/png", Data: []byte("hi")}}, Files: []string{"/w/a b.go"}})
	if err != nil {
		t.Fatal(err)
	}
	usage := rec.waitFor(t, func(e *inabav1.Event) bool { return e.GetUsageChanged() != nil }).GetUsageChanged().Usage
	if usage.Used != 1200 || usage.Size != 200000 || usage.Cost != 0.25 || usage.Currency != "USD" {
		t.Fatalf("usage = %v", usage)
	}
	got := rec.waitFor(t, func(e *inabav1.Event) bool { return strings.Contains(e.GetTextDelta().GetText(), "image") }).GetTextDelta().Text
	if got != "image:image/pngaGk= resource_link:file:///w/a%20b.go" {
		t.Fatalf("the agent got %q", got)
	}
}

func TestPlanUpdates(t *testing.T) {
	rec := &recorder{notify: make(chan struct{}, 1)}
	ctx := context.Background()
	sess, err := fakeProvider(t).Open(ctx, providerConfig(t, ""), rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if _, err := sess.Prompt(ctx, provider.Prompt{Text: "plan"}); err != nil {
		t.Fatal(err)
	}
	// The turn's token counts come back with its end.
	if u := sess.(provider.TurnUsageReporter).LastTurnUsage(); u.GetInputTokens() != 120 || u.GetOutputTokens() != 30 || u.GetCachedReadTokens() != 7 || u.GetTotalTokens() != 157 {
		t.Fatalf("turn usage = %v", u)
	}
	plan := rec.waitFor(t, func(e *inabav1.Event) bool {
		p := e.GetPlan()
		return p != nil && p.Entries[1].Status == "completed"
	}).GetPlan()
	if len(plan.Entries) != 2 || plan.Entries[0].Content != "Read the code" || plan.Entries[0].Priority != "high" {
		t.Fatalf("plan = %v", plan)
	}
}

func TestSessionGetsMCPServers(t *testing.T) {
	rec := &recorder{notify: make(chan struct{}, 1)}
	ctx := context.Background()
	cfg := providerConfig(t, "")
	cfg.MCPServers = []provider.MCPServer{
		{Name: "browser", Command: "npx", Args: []string{"@playwright/mcp", "--headless"}, Env: map[string]string{"B": "2", "A": "1"}},
		{Name: "docs", URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"}},
	}
	sess, err := fakeProvider(t).Open(ctx, cfg, rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if _, err := sess.Prompt(ctx, provider.Prompt{Text: "mcp"}); err != nil {
		t.Fatal(err)
	}
	got := rec.waitFor(t, func(e *inabav1.Event) bool { return strings.HasPrefix(e.GetTextDelta().GetText(), "mcp:") }).GetTextDelta().Text
	if got != "mcp: browser=npx @playwright/mcp --headless A=1 B=2; docs=https://mcp.example.com/mcp" {
		t.Fatalf("the agent got %q", got)
	}
}

func TestHTTPServersAreBridgedForAgentsWithoutHTTP(t *testing.T) {
	saved := MCPBridge
	t.Cleanup(func() { MCPBridge = saved })
	servers := []provider.MCPServer{{Name: "tools", URL: "http://127.0.0.1:1/x/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}, {Name: "cmd", Command: "c"}}

	if got := mcpServers(servers, sdk.McpCapabilities{Http: true}); len(got) != 2 || got[0].Http == nil || got[0].Http.Url != servers[0].URL {
		t.Fatalf("an agent with HTTP gets the address: %+v", got)
	}
	MCPBridge = nil
	if got := mcpServers(servers, sdk.McpCapabilities{}); len(got) != 1 || got[0].Stdio.Name != "cmd" {
		t.Fatalf("with no bridge, the HTTP server is left out: %+v", got)
	}
	MCPBridge = []string{"/bin/inaba", "mcp-bridge"}
	got := mcpServers(servers, sdk.McpCapabilities{})
	b := got[0].Stdio
	if len(got) != 2 || b == nil || b.Command != "/bin/inaba" || strings.Join(b.Args, " ") != "mcp-bridge http://127.0.0.1:1/x/mcp" ||
		len(b.Env) != 1 || b.Env[0].Name != "INABA_MCP_HEADERS" || b.Env[0].Value != `{"Authorization":"Bearer t"}` {
		t.Fatalf("bridged for an agent without HTTP: %+v", b)
	}
}

// A check starts the agent and reads what it says of itself; a missing command fails it.
func TestCheck(t *testing.T) {
	got, err := fakeProvider(t).Check(context.Background())
	if err != nil || got.Name != "fake-agent" || got.Version != "1.2.3" {
		t.Fatalf("check: %+v %v", got, err)
	}
	if _, err := (&Provider{Name: "gone", Command: []string{"inaba-no-such-agent"}}).Check(context.Background()); err == nil {
		t.Fatal("a missing agent checked fine")
	}
}

func TestFileURI(t *testing.T) {
	path, want := "/a b/c.txt", "file:///a%20b/c.txt"
	if runtime.GOOS == "windows" {
		path, want = `C:\a b\c.txt`, "file:///C:/a%20b/c.txt"
	}
	if got := fileURI(path); got != want {
		t.Fatalf("%s, want %s", got, want)
	}
}

// A thread's language reaches an agent that takes the client's instructions, named so it
// need not know the tag; a thread without one adds nothing.
func TestLanguageIsToldToTheAgent(t *testing.T) {
	for _, c := range []struct{ language, want string }{
		{"ko", "system: Talk to the user in Korean (ko):"},
		{"", "system: "},
	} {
		rec := &recorder{notify: make(chan struct{}, 1)}
		cfg := providerConfig(t, "")
		cfg.Language = c.language
		sess, err := fakeProvider(t).Open(context.Background(), cfg, rec.emit)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sess.Prompt(context.Background(), provider.Prompt{Text: "system"}); err != nil {
			t.Fatal(err)
		}
		rec.waitFor(t, func(e *inabav1.Event) bool { return strings.HasPrefix(e.GetTextDelta().GetText(), "system:") })
		rec.mu.Lock()
		var text string
		for _, e := range rec.events {
			text += e.GetTextDelta().GetText()
		}
		rec.mu.Unlock()
		sess.Close()
		if !strings.HasPrefix(text, c.want) || (c.language == "" && text != c.want) {
			t.Errorf("language %q: the agent was told %q", c.language, text)
		}
	}
}

// An agent's own sessions are listed newest first across pages, and one taken up from elsewhere brings
// its conversation in: the person's messages whole, then the answers.
func TestImportingASession(t *testing.T) {
	p := fakeProvider(t)
	cwd := t.TempDir()
	list, err := p.Sessions(context.Background(), cwd)
	if err != nil || len(list) != 2 || list[0].ID != "s-new" || list[1].ID != "s-old" || list[1].Title != "Fix the build" || list[1].Cwd != cwd {
		t.Fatalf("sessions: %+v %v", list, err)
	}
	rec := &recorder{notify: make(chan struct{}, 1)}
	cfg := providerConfig(t, "s-old")
	cfg.Replay = true
	sess, err := p.Open(context.Background(), cfg, rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	rec.waitFor(t, func(e *inabav1.Event) bool { return e.GetTextDelta().GetText() == "old history" })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var said []string
	for _, e := range rec.events {
		if m := e.GetUserMessage(); m != nil {
			said = append(said, "user:"+m.Text)
		} else if d := e.GetTextDelta(); d != nil {
			said = append(said, "agent:"+d.Text)
		}
	}
	if strings.Join(said, "|") != "user:old question|agent:old history" {
		t.Fatalf("imported history: %v", said)
	}
}
