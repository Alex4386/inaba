package files

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

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

func TestGrepFiles(t *testing.T) {
	h := host.New(idleAgent{})
	t.Cleanup(h.Close)
	s := &service{threads: h}
	ctx := rpc.Trusted(context.Background())
	// A user's git config must not change what git grep prints.
	for k, v := range map[string]string{"GIT_CONFIG_COUNT": "2", "GIT_CONFIG_KEY_0": "grep.column", "GIT_CONFIG_VALUE_0": "true", "GIT_CONFIG_KEY_1": "grep.fullName", "GIT_CONFIG_VALUE_1": "true"} {
		t.Setenv(k, v)
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("Needle outside\n"), 0o644)
	setup := func(git bool) string {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "src"), 0o755)
		os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
		os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("x := needle\n\tNeedle()\nneedles\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "b.txt"), []byte("한글 needle\r\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "bin"), []byte("needle\x00"), 0o644)
		os.WriteFile(filepath.Join(dir, "node_modules", "m.js"), []byte("needle\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "flags.txt"), []byte("run --help\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "many.txt"), []byte(strings.Repeat("needle\n", 60)), 0o644)
		os.WriteFile(filepath.Join(dir, "latin.txt"), []byte("caf\xe9 zebra\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "k.txt"), []byte("안녕하세요\n안녕 세계\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "long.txt"), []byte("qq"+strings.Repeat("x", 100000)+"zz\n"), 0o644)
		os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link"))
		if git {
			os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules\n"), 0o644)
			cmd := exec.Command("git", "init", "-q")
			cmd.Dir = dir
			if err := cmd.Run(); err != nil {
				t.Skip("no git")
			}
		}
		th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "idle", Cwd: dir})
		return th.Id
	}
	for _, git := range []bool{false, true} {
		id := setup(git)
		grep := func(req *inabav1.GrepFilesRequest) []string {
			req.ThreadId = id
			r, err := s.GrepFiles(ctx, req)
			if err != nil {
				t.Fatalf("git=%v %v: %v", git, req, err)
			}
			var got []string
			for _, m := range r.Matches {
				if m.Path != "many.txt" {
					got = append(got, fmt.Sprintf("%s:%d:%d %q %d-%d", m.Path, m.Line, m.Column, m.Text, m.MatchStart, m.MatchEnd))
				}
			}
			return got
		}
		r, _ := s.GrepFiles(ctx, &inabav1.GrepFilesRequest{ThreadId: id, Query: "needle"})
		if !r.Truncated || len(r.Matches) != 50+4 {
			t.Errorf("git=%v: 50 a file, then truncated: %d %v", git, len(r.Matches), r.Truncated)
		}
		want := []string{`b.txt:1:4 "한글 needle" 3-9`, `src/a.go:1:6 "x := needle" 5-11`, `src/a.go:2:2 "Needle()" 0-6`, `src/a.go:3:1 "needles" 0-6`}
		if got := grep(&inabav1.GrepFilesRequest{Query: "needle"}); !slices.Equal(got, want) {
			t.Errorf("git=%v: %q", git, got)
		}
		if got := grep(&inabav1.GrepFilesRequest{Query: "Needle", CaseSensitive: true, WholeWord: true}); len(got) != 1 || !strings.HasPrefix(got[0], "src/a.go:2:") {
			t.Errorf("git=%v case and word: %q", git, got)
		}
		if got := grep(&inabav1.GrepFilesRequest{Query: "needle[s]", Regex: true}); len(got) != 1 || !strings.HasPrefix(got[0], "src/a.go:3:") {
			t.Errorf("git=%v regex: %q", git, got)
		}
		if got := grep(&inabav1.GrepFilesRequest{Query: "--help"}); len(got) != 1 || !strings.HasPrefix(got[0], "flags.txt:1:5 ") {
			t.Errorf("git=%v a query like a flag: %q", git, got)
		}
		if _, err := s.GrepFiles(ctx, &inabav1.GrepFilesRequest{ThreadId: id, Query: "(", Regex: true}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("git=%v bad regex: %v", git, err)
		}
		// Go's syntax git refuses is an error in git, not a search of the ignored files.
		if _, err := s.GrepFiles(ctx, &inabav1.GrepFilesRequest{ThreadId: id, Query: "(?:needle)", Regex: true}); git && connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("git=%v regex git refuses: %v", git, err)
		}
		if got := grep(&inabav1.GrepFilesRequest{Query: "zebra"}); !slices.Equal(got, []string{"latin.txt:1:6 \"caf\uFFFD zebra\" 5-10"}) {
			t.Errorf("git=%v not UTF-8: %q", git, got)
		}
		if got := grep(&inabav1.GrepFilesRequest{Query: "안녕", WholeWord: true}); !slices.Equal(got, []string{`k.txt:2:1 "안녕 세계" 0-2`}) {
			t.Errorf("git=%v a word not in ASCII: %q", git, got)
		}
		r, err := s.GrepFiles(ctx, &inabav1.GrepFilesRequest{ThreadId: id, Query: "qq.*zz", Regex: true})
		if err != nil || len(r.Matches) != 1 || len(r.Matches[0].Text) > grepContext+2 || int(r.Matches[0].MatchEnd) != len(r.Matches[0].Text) {
			t.Errorf("git=%v a long match is cut: %v", git, err)
		}
	}
}

func TestGrepTrimsLongLines(t *testing.T) {
	g := &grep{re: regexp.MustCompile("(needle)"), res: &inabav1.GrepFilesResponse{}}
	g.add("x", 1, strings.Repeat("é", 300)+"needle"+strings.Repeat("b", 300))
	m := g.res.Matches[0]
	if len(m.Text) > grepContext+2 || utf16Slice(m.Text, m.MatchStart, m.MatchEnd) != "needle" || m.Column != 301 {
		t.Fatalf("%d %q %d-%d col %d", len(m.Text), m.Text, m.MatchStart, m.MatchEnd, m.Column)
	}
}

func utf16Slice(s string, from, to uint32) string {
	u := utf16.Encode([]rune(s))
	return string(utf16.Decode(u[from:to]))
}
