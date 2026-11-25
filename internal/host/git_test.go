package host

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/testdir"
)

func TestGitStatusDiffAndCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// An identity for the commits, and nothing from the machine's own git config.
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull,
	} {
		t.Setenv(k, v)
	}
	ctx := Trusted(context.Background())
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	if err := h.OpenData(filepath.Join(testdir.New(t), "inaba.db")); err != nil {
		t.Fatal(err)
	}

	plain, _ := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: t.TempDir()})
	if st, err := h.GetStatus(ctx, &inabav1.GetGitStatusRequest{ProjectId: plain.Id}); err != nil || st.Repository {
		t.Fatalf("status outside a repository: %v %v", st, err)
	}
	if _, err := h.GetStatus(ctx, &inabav1.GetGitStatusRequest{ProjectId: "nope"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("status of an unknown project: %v", err)
	}

	// A bare repository to push to, and a clone of it to work in.
	root := t.TempDir()
	origin, dir := filepath.Join(root, "origin.git"), filepath.Join(root, "work")
	run := func(in string, args ...string) {
		t.Helper()
		if out, err := git(ctx, in, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(root, "init", "-q", "--bare", "-b", "main", origin)
	run(root, "clone", "-q", origin, dir)
	write("a.txt", "one\ntwo\n")
	write("-odd name.txt", "x\n")
	run(dir, "add", "-A")
	run(dir, "commit", "-q", "-m", "first")
	run(dir, "push", "-q", "-u", "origin", "HEAD")

	project, err := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	th := project                       // the git calls below are the project's
	write("a.txt", "one\nTWO\nthree\n") // 2 added, 1 removed
	write("new.txt", "n1\nn2\n")        // untracked, 2 lines
	write("-odd name.txt", "x\ny\n")    // a name that looks like an option

	st, err := h.GetStatus(ctx, &inabav1.GetGitStatusRequest{ProjectId: th.Id})
	if err != nil || !st.Repository || st.Branch != "main" || st.Upstream != "origin/main" || st.Ahead != 0 {
		t.Fatalf("status = %v, %v", st, err)
	}
	got := map[string]string{}
	for _, f := range st.Files {
		got[f.Path] = strings.TrimSpace(f.State) + " +" + string(rune('0'+f.Additions)) + " -" + string(rune('0'+f.Deletions))
	}
	if len(got) != 3 || got["a.txt"] != "M +2 -1" || got["new.txt"] != "?? +2 -0" || got["-odd name.txt"] != "M +1 -0" || st.Additions != 5 || st.Deletions != 1 {
		t.Fatalf("files = %v, totals +%d -%d", got, st.Additions, st.Deletions)
	}

	all, err := h.GetDiff(ctx, &inabav1.GetGitDiffRequest{ProjectId: th.Id})
	if err != nil || !strings.Contains(all.Diff, "+TWO") || !strings.Contains(all.Diff, "-two") || !strings.Contains(all.Diff, "+n2") {
		t.Fatalf("full diff: %v %v", all, err)
	}
	// One file comes whole on both sides, for a side-by-side view; a new file has no before.
	if d, err := h.GetDiff(ctx, &inabav1.GetGitDiffRequest{ProjectId: th.Id, Path: "a.txt"}); err != nil || !d.Sides || d.Before != "one\ntwo\n" || d.After != "one\nTWO\nthree\n" {
		t.Fatalf("sides of a.txt: %v %v", d, err)
	}
	if d, _ := h.GetDiff(ctx, &inabav1.GetGitDiffRequest{ProjectId: th.Id, Path: "new.txt"}); !d.Sides || d.Before != "" || d.After != "n1\nn2\n" {
		t.Fatalf("sides of new.txt: %v", d)
	}
	for path, want := range map[string]string{"new.txt": "+n2", "-odd name.txt": "+y"} {
		d, err := h.GetDiff(ctx, &inabav1.GetGitDiffRequest{ProjectId: th.Id, Path: path})
		if err != nil || !strings.Contains(d.Diff, want) || strings.Contains(d.Diff, "+TWO") {
			t.Fatalf("diff of %q: %v %v", path, d, err)
		}
	}

	if _, err := h.Commit(ctx, &inabav1.GitCommitRequest{ProjectId: th.Id, Message: "  "}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("commit without a message: %v", err)
	}
	res, err := h.Commit(ctx, &inabav1.GitCommitRequest{ProjectId: th.Id, Message: "second", Push: true})
	if err != nil || len(res.Status.Files) != 0 || res.Status.Ahead != 0 {
		t.Fatalf("commit and push: %v %v", res, err)
	}
	if out, _ := git(ctx, origin, "log", "--format=%s", "-1", "main"); strings.TrimSpace(out) != "second" {
		t.Fatalf("the remote's last commit is %q", out)
	}
	// A commit made without pushing leaves the branch ahead; Push sends it.
	write("a.txt", "four\n")
	if res, err := h.Commit(ctx, &inabav1.GitCommitRequest{ProjectId: th.Id, Message: "third"}); err != nil || res.Status.Ahead != 1 {
		t.Fatalf("commit without push: %v %v", res, err)
	}
	if res, err := h.Push(ctx, &inabav1.GitPushRequest{ProjectId: th.Id}); err != nil || res.Status.Ahead != 0 {
		t.Fatalf("push: %v %v", res, err)
	}
	// With nothing changed, git's own refusal comes back.
	if _, err := h.Commit(ctx, &inabav1.GitCommitRequest{ProjectId: th.Id, Message: "fourth"}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("commit with nothing to commit: %v", err)
	}

	if runtime.GOOS == "windows" {
		return // The stand-in below is a shell script.
	}
	// Pull requests go through gh; a stand-in records what it was asked and answers like gh.
	bin := t.TempDir()
	log := filepath.Join(bin, "gh.log")
	fake := `#!/bin/sh
echo "$@" >> "$GH_LOG"
case "$1 $2" in
"pr view") [ -f "$GH_LOG.made" ] && echo https://github.com/o/r/pull/7 && exit 0
  echo 'no pull requests found for branch "feature"' >&2; exit 1 ;;
"pr create") touch "$GH_LOG.made"; echo "Creating pull request for feature into main"; echo https://github.com/o/r/pull/7 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_LOG", log)
	run(dir, "checkout", "-q", "-b", "feature")
	write("a.txt", "five\n")
	run(dir, "commit", "-q", "-am", "feature work")
	pr, err := h.CreatePullRequest(ctx, &inabav1.GitPullRequestRequest{ProjectId: th.Id, Draft: true})
	if err != nil || pr.Url != "https://github.com/o/r/pull/7" || pr.Existing || pr.Status.Upstream != "origin/feature" {
		t.Fatalf("pull request: %v %v", pr, err)
	}
	if out, _ := git(ctx, origin, "log", "--format=%s", "-1", "feature"); strings.TrimSpace(out) != "feature work" {
		t.Fatalf("the branch was not pushed first: %q", out)
	}
	if calls, _ := os.ReadFile(log); !strings.Contains(string(calls), "pr create --head feature --fill --draft") {
		t.Fatalf("gh was asked: %s", calls)
	}
	if pr, err := h.CreatePullRequest(ctx, &inabav1.GitPullRequestRequest{ProjectId: th.Id, Title: "again"}); err != nil || !pr.Existing {
		t.Fatalf("second pull request for the branch: %v %v", pr, err)
	}
}

// A thread working in its project's folder has no git state of its own: its address
// sends the caller on to the project's, also when the API is mounted under a prefix,
// as it is on inaba-server. A thread in a folder of its own answers for itself.
func TestThreadGitAddress(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	if err := h.OpenData(filepath.Join(testdir.New(t), "inaba.db")); err != nil {
		t.Fatal(err)
	}
	ctx := Trusted(context.Background())
	project, err := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	inProject, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: project.Id})
	if err != nil {
		t.Fatal(err)
	}
	own := t.TempDir()
	if _, err := git(ctx, own, "init", "-q", "-b", "side"); err != nil {
		t.Fatal(err)
	}
	floating, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: own})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := h.Handler()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/hosts/h1/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/v1/" + r.PathValue("rest")
		asServer(handler).ServeHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var hops []string
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		hops = append(hops, req.Response.Status+" -> "+req.URL.RequestURI())
		return nil
	}}
	get := func(path string) (int, string) {
		t.Helper()
		hops = nil
		res, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}

	code, _ := get("/v1/hosts/h1/threads/" + inProject.Id + "/git/diff?path=a.txt")
	if want := "307 Temporary Redirect -> /v1/hosts/h1/projects/" + project.Id + "/git/diff?path=a.txt"; len(hops) != 1 || hops[0] != want {
		t.Fatalf("thread in its project: hops %v (final %d), want %q", hops, code, want)
	}
	code, body := get("/v1/hosts/h1/threads/" + floating.Id + "/git/status")
	if code != 200 || len(hops) != 0 || !strings.Contains(body, `"branch":"side"`) {
		t.Fatalf("thread in a folder of its own: %d %s, hops %v", code, body, hops)
	}
	if code, _ := get("/v1/hosts/h1/threads/nope/git/status"); code != http.StatusNotFound {
		t.Fatalf("git address of an unknown thread: %d", code)
	}
}

func TestBranchesAndWorktreeThreads(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull,
	} {
		t.Setenv(k, v)
	}
	ctx := Trusted(context.Background())
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	// The database is named by a relative path, as it is when started from its own folder.
	data := testdir.New(t)
	t.Chdir(data)
	if err := h.OpenData("inaba.db"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "first"}} {
		if out, err := git(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	git(ctx, dir, "add", "-A")
	git(ctx, dir, "commit", "-q", "-m", "second")
	project, _ := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: dir})
	ref := &inabav1.GitCheckoutRequest{ProjectId: project.Id}

	// Branches: create, list, switch; never with a turn running in the folder.
	ref.Branch, ref.Create = "feature", true
	if st, err := h.Checkout(ctx, ref); err != nil || st.Branch != "feature" {
		t.Fatalf("create branch: %v %v", st, err)
	}
	list, err := h.ListBranches(ctx, &inabav1.ListBranchesRequest{ProjectId: project.Id})
	if err != nil || list.Current != "feature" || len(list.Branches) != 2 {
		t.Fatalf("branches: %v %v", list, err)
	}
	if _, err := h.Checkout(ctx, &inabav1.GitCheckoutRequest{ProjectId: project.Id, Branch: "--orphan"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a branch name that is an option: %v", err)
	}
	shared, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: project.Id})
	h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: shared.Id, Text: "hang"})
	if _, err := h.Checkout(ctx, &inabav1.GitCheckoutRequest{ProjectId: project.Id, Branch: "main"}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("switching branches under a running turn: %v", err)
	}
	h.Interrupt(ctx, &inabav1.InterruptRequest{ThreadId: shared.Id})

	// A worktree thread gets a folder and a branch of its own, with the project's files.
	wt, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: project.Id, Worktree: &inabav1.Worktree{Base: "main"}})
	if err != nil || !wt.Worktree || wt.Cwd == dir || !strings.HasPrefix(wt.Cwd, data) {
		t.Fatalf("worktree thread: %v %v", wt, err)
	}
	if _, err := os.Stat(filepath.Join(wt.Cwd, "a.txt")); err != nil {
		t.Fatalf("the worktree lacks the project's files: %v", err)
	}
	st, _ := h.GetStatus(ctx, &inabav1.GetGitStatusRequest{ThreadId: wt.Id})
	if st.Branch != "inaba/"+wt.Id[:8] {
		t.Fatalf("worktree branch = %q", st.Branch)
	}
	// Its changes are its own: the project's folder does not see them.
	os.WriteFile(filepath.Join(wt.Cwd, "only-here.txt"), []byte("x\n"), 0o644)
	if st, _ := h.GetStatus(ctx, &inabav1.GetGitStatusRequest{ProjectId: project.Id}); len(st.Files) != 0 {
		t.Fatalf("the project sees the worktree's changes: %v", st.Files)
	}
	// With changes in it, deleting the thread leaves the worktree; once clean, it goes.
	h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: wt.Id})
	if _, err := os.Stat(wt.Cwd); err != nil {
		t.Fatalf("a worktree with uncommitted changes was removed: %v", err)
	}
	clean, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: project.Id, Worktree: &inabav1.Worktree{Branch: "topic"}})
	h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: clean.Id})
	if _, err := os.Stat(clean.Cwd); !os.IsNotExist(err) {
		t.Fatalf("a clean worktree was left behind: %v", err)
	}
	// A worktree needs a project to be made from.
	if _, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: dir, Worktree: &inabav1.Worktree{}}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("worktree without a project: %v", err)
	}
}

func TestTurnSnapshotsDiffAndRestore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull,
	} {
		t.Setenv(k, v)
	}
	ctx := Trusted(context.Background())
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	if err := h.OpenData(filepath.Join(testdir.New(t), "inaba.db")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	must := func(in string, args ...string) string {
		t.Helper()
		out, err := git(ctx, in, args...)
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(out)
	}
	must(dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bee\n"), 0o644)
	must(dir, "add", "-A")
	must(dir, "commit", "-q", "-m", "first")
	project, _ := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: dir})
	th, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: project.Id, Worktree: &inabav1.Worktree{}})
	if err != nil {
		t.Fatal(err)
	}
	wt := th.Cwd
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(wt, name))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}
	// Something the user staged themselves; snapshots must leave it exactly so.
	os.WriteFile(filepath.Join(wt, "staged.txt"), []byte("mine\n"), 0o644)
	must(wt, "add", "staged.txt")
	head := must(wt, "rev-parse", "HEAD")

	turn := func(text string) *inabav1.TurnChanges {
		t.Helper()
		if _, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: text}); err != nil {
			t.Fatal(err)
		}
		var changes *inabav1.TurnChanges
		for range 300 {
			evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
			ended := 0
			changes = nil
			for _, e := range evs.Events {
				if e.GetTurnEnded() != nil {
					ended++
					changes = nil
				}
				if c := e.GetTurnChanges(); c != nil {
					changes = c
				}
			}
			// The snapshot comes after the turn's end; wait for the thread to be free again.
			if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); got.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE && ended > 0 && (changes != nil || text != "edit") {
				return changes
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no result for turn %q", text)
		return nil
	}

	if c := turn("hello"); c != nil {
		t.Fatalf("a turn that changed nothing reported changes: %v", c)
	}
	c := turn("edit")
	if c == nil || c.Files != 3 || c.Additions != 2 || c.Deletions != 2 {
		t.Fatalf("turn changes = %v", c)
	}
	// Nothing of this shows where the user works: same commit, same staged file, no new branch.
	if must(wt, "rev-parse", "HEAD") != head || must(wt, "diff", "--cached", "--name-only") != "staged.txt" || strings.Contains(must(wt, "branch", "-a"), "checkpoint") {
		t.Fatal("a snapshot moved HEAD, changed the index, or made a branch")
	}
	d, err := h.GetDiff(ctx, &inabav1.GetGitDiffRequest{ThreadId: th.Id, From: c.Before, To: c.After})
	if err != nil || !strings.Contains(d.Diff, "+CHANGED") || !strings.Contains(d.Diff, "+fresh") || !strings.Contains(d.Diff, "-bee") || strings.Contains(d.Diff, "mine") {
		t.Fatalf("turn diff: %v %v", d, err)
	}
	if _, err := h.GetDiff(ctx, &inabav1.GetGitDiffRequest{ThreadId: th.Id, From: "--output=/tmp/x", To: c.After}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a snapshot id that is an option: %v", err)
	}

	// Back to before the turn: the change undone, the new file gone, the removed one back.
	res, err := h.Restore(ctx, &inabav1.GitRestoreRequest{ThreadId: th.Id, Commit: c.Before})
	if err != nil {
		t.Fatal(err)
	}
	if read("a.txt") != "one\ntwo\n" || read("new.txt") != "<missing>" || read("b.txt") != "bee\n" || read("staged.txt") != "mine\n" {
		t.Fatalf("after restore: a=%q new=%q b=%q staged=%q", read("a.txt"), read("new.txt"), read("b.txt"), read("staged.txt"))
	}
	if must(wt, "diff", "--cached", "--name-only") != "staged.txt" {
		t.Fatal("the restore changed what was staged")
	}
	// And the restore itself can be taken back.
	if _, err := h.Restore(ctx, &inabav1.GitRestoreRequest{ThreadId: th.Id, Commit: res.Undo}); err != nil {
		t.Fatal(err)
	}
	if read("a.txt") != "one\nCHANGED\n" || read("new.txt") != "fresh\n" || read("b.txt") != "<missing>" {
		t.Fatalf("after undoing the restore: a=%q new=%q b=%q", read("a.txt"), read("new.txt"), read("b.txt"))
	}

	// A thread in the project's shared folder may not restore files.
	shared, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: project.Id})
	if _, err := h.Restore(ctx, &inabav1.GitRestoreRequest{ThreadId: shared.Id, Commit: c.Before}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("restore in a shared folder: %v", err)
	}
	// Deleting the thread lets its snapshots go.
	if must(dir, "for-each-ref", checkpointRefs+th.Id+"/") == "" {
		t.Fatal("no snapshot refs to begin with")
	}
	h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: th.Id})
	if left := must(dir, "for-each-ref", checkpointRefs+th.Id+"/"); left != "" {
		t.Fatalf("snapshot refs left after deleting the thread: %s", left)
	}
}

// Fetch sees what others pushed, Pull fast-forwards to it but never under a running turn
// or onto a diverged branch, and a diff can leave out whitespace-only changes.
func TestFetchPullAndWhitespaceDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull,
	} {
		t.Setenv(k, v)
	}
	ctx := Trusted(context.Background())
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	if err := h.OpenData(filepath.Join(testdir.New(t), "inaba.db")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	origin, dir, other := filepath.Join(root, "origin.git"), filepath.Join(root, "work"), filepath.Join(root, "other")
	run := func(in string, args ...string) {
		t.Helper()
		if out, err := git(ctx, in, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	commit := func(in, name, content string) {
		t.Helper()
		os.WriteFile(filepath.Join(in, name), []byte(content), 0o644)
		run(in, "add", "-A")
		run(in, "commit", "-q", "-m", name)
	}
	run(root, "init", "-q", "--bare", "-b", "main", origin)
	run(root, "clone", "-q", origin, dir)
	commit(dir, "a.txt", "one\ntwo\n")
	run(dir, "push", "-q", "-u", "origin", "HEAD")
	run(root, "clone", "-q", origin, other)
	project, _ := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: dir})

	// Only indentation changed: the diff shows it, unless whitespace is ignored.
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n  two\n"), 0o644)
	if d, err := h.GetDiff(ctx, &inabav1.GetGitDiffRequest{ProjectId: project.Id, Path: "a.txt"}); err != nil || !strings.Contains(d.Diff, "+  two") {
		t.Fatalf("diff with whitespace: %v %v", d, err)
	}
	for _, path := range []string{"a.txt", ""} {
		if d, err := h.GetDiff(ctx, &inabav1.GetGitDiffRequest{ProjectId: project.Id, Path: path, IgnoreWhitespace: true}); err != nil || d.Diff != "" {
			t.Fatalf("diff of %q ignoring whitespace: %q %v", path, d.Diff, err)
		}
	}
	run(dir, "checkout", "--", "a.txt")

	commit(other, "b.txt", "b\n")
	run(other, "push", "-q")
	if res, err := h.Fetch(ctx, &inabav1.GitFetchRequest{ProjectId: project.Id}); err != nil || res.Status.Behind != 1 {
		t.Fatalf("fetch: %v %v", res, err)
	}
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: project.Id})
	h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hang"})
	if _, err := h.Pull(ctx, &inabav1.GitPullBranchRequest{ProjectId: project.Id}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("pull under a running turn: %v", err)
	}
	h.Interrupt(ctx, &inabav1.InterruptRequest{ThreadId: th.Id})
	for i := 0; ; i++ {
		if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); got.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE {
			break
		} else if i == 100 {
			t.Fatal("the turn did not stop")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if res, err := h.Pull(ctx, &inabav1.GitPullBranchRequest{ProjectId: project.Id}); err != nil || res.Status.Behind != 0 {
		t.Fatalf("pull: %v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("the pulled file is missing: %v", err)
	}

	// Diverged: a commit on each side. Nothing is merged; git's reason comes back.
	commit(other, "c.txt", "c\n")
	run(other, "push", "-q")
	commit(dir, "d.txt", "d\n")
	_, err := h.Pull(ctx, &inabav1.GitPullBranchRequest{ProjectId: project.Id})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "fast-forward") {
		t.Fatalf("pull onto a diverged branch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); !os.IsNotExist(err) {
		t.Fatalf("a diverged pull changed the files: %v", err)
	}
}
