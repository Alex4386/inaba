package files

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/provider"
)

type idleAgent struct{}

func (idleAgent) ID() string { return "idle" }
func (idleAgent) Open(context.Context, provider.Config, provider.Emit) (provider.Session, error) {
	return idleSession{}, nil
}

type idleSession struct{}

func (idleSession) NativeSessionID() string                                 { return "n" }
func (idleSession) Prompt(context.Context, provider.Prompt) (string, error) { return "end_turn", nil }
func (idleSession) Interrupt(context.Context) error                         { return nil }
func (idleSession) Respond(context.Context, string, string) error           { return nil }
func (idleSession) Close() error                                            { return nil }

func TestFilesStayInsideTheThreadsFolder(t *testing.T) {
	h := host.New(idleAgent{})
	t.Cleanup(h.Close)
	s := &service{threads: h}
	ctx := rpc.Trusted(context.Background())
	dir, outside := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src", ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# hi\n"), 0o644)
	os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link"))
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "idle", Cwd: dir})

	top, err := s.ListFolder(ctx, &inabav1.ListFolderRequest{ThreadId: th.Id})
	var names []string
	for _, e := range top.GetEntries() {
		names = append(names, e.Name)
	}
	if err != nil || strings.Join(names, ",") != "src,link,README.md" || !top.Entries[0].Folder {
		t.Fatalf("top folder: %v %v", names, err)
	}
	if src, _ := s.ListFolder(ctx, &inabav1.ListFolderRequest{ThreadId: th.Id, Path: "src"}); len(src.Entries) != 1 || src.Entries[0].Name != "main.go" {
		t.Fatalf("src, with .git left out: %v", src)
	}
	f, err := s.ReadFile(ctx, &inabav1.ReadFileRequest{ThreadId: th.Id, Path: "src/main.go"})
	if err != nil || string(f.Data) != "package main\n" || !strings.HasPrefix(f.MimeType, "text/plain") || f.Truncated {
		t.Fatalf("read: %v %v", f, err)
	}
	for _, path := range []string{"link", "../" + filepath.Base(outside) + "/secret", "/etc/hosts"} {
		if _, err := s.ReadFile(ctx, &inabav1.ReadFileRequest{ThreadId: th.Id, Path: path}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("reading %s: %v", path, err)
		}
	}

	// Saving: from the version read, refused once the file changed meanwhile, never outside.
	saved, err := s.WriteFile(ctx, &inabav1.WriteFileRequest{ThreadId: th.Id, Path: "src/main.go", Data: []byte("package main\n\nfunc main() {}\n"), BaseVersion: f.Version})
	if err != nil || saved.Version == f.Version {
		t.Fatalf("save: %v %v", saved, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "src", "main.go")); !strings.Contains(string(b), "func main") {
		t.Fatalf("on disk: %q", b)
	}
	if _, err := s.WriteFile(ctx, &inabav1.WriteFileRequest{ThreadId: th.Id, Path: "src/main.go", Data: []byte("stale"), BaseVersion: f.Version}); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("saving over a change: %v", err)
	}
	for _, path := range []string{"link", "src/.git", "../" + filepath.Base(outside) + "/secret"} {
		if _, err := s.WriteFile(ctx, &inabav1.WriteFileRequest{ThreadId: th.Id, Path: path, Data: []byte("x")}); err == nil {
			t.Errorf("saved %s", path)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret")); string(b) != "x" {
		t.Fatalf("the file outside changed: %q", b)
	}
}

func TestMatchPaths(t *testing.T) {
	paths := []string{"web/src/components/session-view.tsx", "internal/host/session.go", "docs/sessions/x.md", "README.md", "s/e/s/s.txt"}
	got := matchPaths(paths, "Session", 10)
	want := []string{"internal/host/session.go", "web/src/components/session-view.tsx", "docs/sessions/x.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("matchPaths = %v", got)
	}
	if got := matchPaths(paths, "ses", 10); !slices.Contains(got, "s/e/s/s.txt") || got[len(got)-1] != "s/e/s/s.txt" {
		t.Fatalf("letters in order rank last: %v", got)
	}
	if got := matchPaths(paths, "", 2); len(got) != 2 || got[0] != "README.md" {
		t.Fatalf("empty query: %v", got)
	}
}
