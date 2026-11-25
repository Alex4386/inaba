package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/testdir"
)

func must(out string, err error) string {
	if err != nil {
		panic(err)
	}
	return out
}

// A thread deleted with changes in its worktree leaves the worktree and its
// snapshots behind; housekeeping finds them and clears them when told to.
func TestHousekeepingClearsWhatDeletedSessionsLeft(t *testing.T) {
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
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "first"}} {
		if out, err := git(ctx, dir, args...); err != nil {
			t.Fatal(out, err)
		}
	}
	project, _ := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: dir})
	newSession := func() *inabav1.Thread {
		th, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", ProjectId: project.Id, Worktree: &inabav1.Worktree{}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hi"}); err != nil {
			t.Fatal(err)
		}
		for range 300 {
			if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); got.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE && got.LastSeq > 4 {
				return th
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("turn did not end")
		return nil
	}
	kept := newSession()
	// A fork of a worktree thread gets a worktree of its own, from the same commit.
	fork, err := h.ForkThread(ctx, &inabav1.ForkThreadRequest{ThreadId: kept.Id, Seq: 1})
	if err != nil || !fork.Worktree || fork.Cwd == kept.Cwd {
		t.Fatalf("fork of a worktree thread: %v %v", fork, err)
	}
	if a, b := must(git(ctx, kept.Cwd, "rev-parse", "HEAD")), must(git(ctx, fork.Cwd, "rev-parse", "HEAD")); a != b {
		t.Fatalf("the fork starts at %s, not %s", b, a)
	}
	if _, err := h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: fork.Id}); err != nil {
		t.Fatal(err)
	}
	gone := newSession()
	os.WriteFile(filepath.Join(gone.Cwd, "unsaved.txt"), []byte("work\n"), 0o644)
	if _, err := h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: gone.Id}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.GetHousekeeping(context.Background(), &inabav1.GetHousekeepingRequest{}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("housekeeping without the hosts permission: %v", err)
	}
	hk, err := h.GetHousekeeping(ctx, &inabav1.GetHousekeepingRequest{})
	if err != nil || hk.DatabaseBytes == 0 || len(hk.Worktrees) != 2 || len(hk.Snapshots) != 1 {
		t.Fatalf("housekeeping = %v %v", hk, err)
	}
	for _, w := range hk.Worktrees {
		if leftover := w.Path == gone.Cwd; leftover != (w.ThreadId == "") || leftover != w.Dirty || !strings.HasPrefix(w.Branch, "inaba/") {
			t.Fatalf("worktree %v", w)
		}
	}
	// Deleting a thread drops its snapshots, even one taken as its last turn ended.
	if s := hk.Snapshots[0]; s.Refs == 0 || s.LeftoverRefs != 0 {
		t.Fatalf("snapshots %v", s)
	}
	// Snapshots left by a thread that disappeared some other way, as in a crash.
	if out, err := git(ctx, dir, "update-ref", checkpointRefs+"deadbeef/1-before", "HEAD"); err != nil {
		t.Fatal(out, err)
	}

	res, err := h.CleanUp(ctx, &inabav1.CleanUpRequest{LeftoverWorktrees: true, LeftoverSnapshots: true})
	if err != nil || len(res.Kept) != 1 || !strings.Contains(res.Kept[0], "uncommitted") || len(res.Housekeeping.Worktrees) != 2 || res.Housekeeping.Snapshots[0].LeftoverRefs != 0 {
		t.Fatalf("clean up keeping changes: %v %v", res, err)
	}
	res, err = h.CleanUp(ctx, &inabav1.CleanUpRequest{LeftoverWorktrees: true, DiscardChanges: true, CompactDatabase: true})
	if err != nil || len(res.Kept) != 0 || len(res.Housekeeping.Worktrees) != 1 || res.Housekeeping.Worktrees[0].ThreadId != kept.Id {
		t.Fatalf("clean up discarding changes: %v %v", res, err)
	}
	if _, err := os.Stat(gone.Cwd); !os.IsNotExist(err) {
		t.Fatalf("the leftover worktree is still there: %v", err)
	}
	// A thread this host has but did not load, its provider not being configured now,
	// keeps its worktree and snapshots.
	h2 := New()
	t.Cleanup(h2.Close)
	if err := h2.OpenData(filepath.Join(filepath.Dir(h.dbPath), "inaba.db")); err != nil {
		t.Fatal(err)
	}
	if res, err := h2.CleanUp(ctx, &inabav1.CleanUpRequest{LeftoverWorktrees: true, DiscardChanges: true, LeftoverSnapshots: true}); err != nil || len(res.Housekeeping.Worktrees) != 1 || res.Housekeeping.Snapshots[0].Refs == 0 {
		t.Fatalf("clean up by a host without the provider: %v %v", res, err)
	}
	if _, err := os.Stat(kept.Cwd); err != nil {
		t.Fatalf("an unloaded thread's worktree was removed: %v", err)
	}

}
