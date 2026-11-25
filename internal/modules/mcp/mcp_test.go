package mcp

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/testdir"
	"github.com/Alex4386/inaba/pkg/module"
	"github.com/Alex4386/inaba/pkg/provider"
)

// recorder is an agent that remembers the MCP servers each thread was given.
type recorder struct {
	mu   sync.Mutex
	seen [][]provider.MCPServer
}

func (r *recorder) ID() string { return "rec" }
func (r *recorder) Open(_ context.Context, cfg provider.Config, _ provider.Emit) (provider.Session, error) {
	r.mu.Lock()
	r.seen = append(r.seen, cfg.MCPServers)
	r.mu.Unlock()
	return idle{}, nil
}
func (r *recorder) last() []provider.MCPServer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[len(r.seen)-1]
}

type idle struct{}

func (idle) NativeSessionID() string                                 { return "n" }
func (idle) Prompt(context.Context, provider.Prompt) (string, error) { return "end_turn", nil }
func (idle) Interrupt(context.Context) error                         { return nil }
func (idle) Respond(context.Context, string, string) error           { return nil }
func (idle) Close() error                                            { return nil }

func names(servers []provider.MCPServer) []string {
	var out []string
	for _, m := range servers {
		out = append(out, m.Name)
	}
	return out
}

func TestSessionsGetTheServersThatApplyToThem(t *testing.T) {
	ctx := rpc.Trusted(context.Background())
	dbPath := filepath.Join(testdir.New(t), "inaba.db")
	rec := &recorder{}
	h := host.New(rec)
	t.Cleanup(h.Close)
	if err := h.OpenData(dbPath); err != nil {
		t.Fatal(err)
	}
	s, err := newService(module.Store{DB: h.DB()})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Contribute([]module.Contribution{s.contribution()}); err != nil {
		t.Fatal(err)
	}
	project, _ := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: t.TempDir()})

	for name, bad := range map[string]*inabav1.McpServer{
		"no name": {Command: "x"},
		"neither": {Name: "a"},
		"both":    {Name: "a", Command: "x", Url: "https://x"},
		"bad url": {Name: "a", Url: "ftp://x"},
	} {
		if _, err := s.CreateMcpServer(ctx, bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.CreateMcpServer(context.Background(), &inabav1.McpServer{Name: "x", Command: "x"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("without the projects permission: %v", err)
	}
	everywhere, err := s.CreateMcpServer(ctx, &inabav1.McpServer{Name: "browser", Enabled: true, Command: "npx", Args: []string{"@playwright/mcp"}, Env: map[string]string{"K": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMcpServer(ctx, &inabav1.McpServer{Name: "browser", Command: "y"}); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("a second server named the same: %v", err)
	}
	if _, err := s.CreateMcpServer(ctx, &inabav1.McpServer{Name: "docs", Enabled: true, Url: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"}, ProjectIds: []string{project.Id}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMcpServer(ctx, &inabav1.McpServer{Name: "off", Command: "z"}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "rec", ProjectId: project.Id}); err != nil {
		t.Fatal(err)
	}
	got := rec.last()
	if n := names(got); len(n) != 2 || n[0] != "browser" || n[1] != "docs" || got[0].Args[0] != "@playwright/mcp" || got[0].Env["K"] != "v" || got[1].URL == "" || got[1].Headers["Authorization"] != "Bearer t" {
		t.Fatalf("a thread in the project got %+v", got)
	}
	if _, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "rec", Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if n := names(rec.last()); len(n) != 1 || n[0] != "browser" {
		t.Fatalf("a thread outside the project got %v", n)
	}

	// Turned off, a server is no longer given; the servers outlive the process.
	everywhere.Enabled = false
	if _, err := s.UpdateMcpServer(ctx, everywhere); err != nil {
		t.Fatal(err)
	}
	again, err := newService(module.Store{DB: h.DB()})
	if err != nil {
		t.Fatal(err)
	}
	if got := again.forThread(&inabav1.Thread{ProjectId: project.Id}); len(got) != 1 || got[0].Name != "docs" {
		t.Fatalf("after a reload: %+v", got)
	}
}
