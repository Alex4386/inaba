package agenttools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/testdir"
	"github.com/Alex4386/inaba/pkg/provider"
)

// echoAgent answers each message with "echo <message>"; "slow" takes a while.
type echoAgent struct{}

func (echoAgent) ID() string { return "echo" }
func (echoAgent) Open(_ context.Context, _ provider.Config, emit provider.Emit) (provider.Session, error) {
	return echoSession{emit}, nil
}

type echoSession struct{ emit provider.Emit }

func (echoSession) NativeSessionID() string { return "n" }
func (s echoSession) Prompt(_ context.Context, p provider.Prompt) (string, error) {
	if p.Text == "slow" {
		time.Sleep(700 * time.Millisecond)
	}
	s.emit(&inabav1.Event{Payload: &inabav1.Event_TextDelta{TextDelta: &inabav1.TextDelta{Text: "echo " + p.Text}}})
	return "end_turn", nil
}
func (echoSession) Interrupt(context.Context) error               { return nil }
func (echoSession) Respond(context.Context, string, string) error { return nil }
func (echoSession) Close() error                                  { return nil }

// agent is an MCP client acting as the agent of one thread.
type agent struct {
	t       *testing.T
	session *mcp.ClientSession
}

func connectAs(t *testing.T, s *service, threadID string) *agent {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: s.endpoint(threadID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &agent{t, cs}
}

// call runs a tool and decodes its structured answer into out; it returns the tool's error text, if any.
func (a *agent) call(name string, args map[string]any, out any) string {
	a.t.Helper()
	res, err := a.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		a.t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		return res.Content[0].(*mcp.TextContent).Text
	}
	if out != nil {
		data, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(data, out); err != nil {
			a.t.Fatalf("%s answered %s: %v", name, data, err)
		}
	}
	return ""
}

func TestAgentsHandWorkToSessions(t *testing.T) {
	ctx := rpc.Trusted(context.Background())
	h := host.New(echoAgent{})
	t.Cleanup(h.Close)
	if err := h.OpenData(filepath.Join(testdir.New(t), "inaba.db")); err != nil {
		t.Fatal(err)
	}
	s := newService(h)
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stop)

	project, _ := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: t.TempDir()})
	boss, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "echo", ProjectId: project.Id, Title: "boss"})
	elsewhere, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "echo", Cwd: t.TempDir(), Title: "elsewhere"})
	me := connectAs(t, s, boss.Id)

	tools, err := me.session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 6 {
		t.Fatalf("tools: %v %v", tools, err)
	}

	// Hand a task to a new thread, wait for it, and read its answer.
	var started startOut
	if msg := me.call("start_thread", map[string]any{"message": "slow", "title": "helper"}, &started); msg != "" {
		t.Fatal(msg)
	}
	helper, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: started.ID})
	if helper.ProjectId != project.Id || helper.Labels[parentLabel] != boss.Id || helper.Labels[depthLabel] != "1" || helper.Title != "helper" {
		t.Fatalf("the new thread: %v", helper)
	}
	var waited waitOut
	me.call("wait_for_thread", map[string]any{"id": started.ID, "seconds": 1}, &waited)
	if waited.Status == "running" {
		// Not done within a second: the wait says so, and a second wait gets the answer.
		me.call("wait_for_thread", map[string]any{"id": started.ID}, &waited)
	}
	if waited.Status != "idle" || waited.Answer != "echo slow" || !waited.Yours {
		t.Fatalf("waited: %+v", waited)
	}

	if msg := me.call("send_message", map[string]any{"id": started.ID, "message": "more"}, nil); msg != "" {
		t.Fatal(msg)
	}
	me.call("wait_for_thread", map[string]any{"id": started.ID}, nil)
	var read readOut
	me.call("read_thread", map[string]any{"id": started.ID, "turns": 5}, &read)
	if len(read.Turns) != 2 || read.Turns[0].Asked != "slow" || read.Turns[1].Answer != "echo more" {
		t.Fatalf("read: %+v", read)
	}

	// What it can see: its project's threads, not others.
	var list listOut
	me.call("list_threads", nil, &list)
	var titles []string
	for _, ses := range list.Threads {
		titles = append(titles, ses.Title)
		if ses.ID == elsewhere.Id {
			t.Fatal("a thread outside the project was listed")
		}
	}
	if len(list.Threads) != 2 {
		t.Fatalf("listed %v", titles)
	}
	if msg := me.call("read_thread", map[string]any{"id": elsewhere.Id}, nil); !strings.Contains(msg, "no thread") {
		t.Fatalf("reading a thread outside the project: %q", msg)
	}

	// What it can drive: only what it started.
	sibling, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "echo", ProjectId: project.Id, Title: "sibling"})
	if msg := me.call("send_message", map[string]any{"id": sibling.Id, "message": "x"}, nil); !strings.Contains(msg, "not one you started") {
		t.Fatalf("driving another's thread: %q", msg)
	}

	// Work handed on stops being handed on past the depth limit.
	depth := connectAs(t, s, started.ID)
	var level startOut
	for i := 2; i <= maxDepth; i++ {
		if msg := depth.call("start_thread", map[string]any{"message": "go"}, &level); msg != "" {
			t.Fatalf("depth %d: %s", i, msg)
		}
		depth = connectAs(t, s, level.ID)
	}
	if msg := depth.call("start_thread", map[string]any{"message": "go"}, nil); !strings.Contains(msg, "deep") {
		t.Fatalf("past the depth limit: %q", msg)
	}
}
