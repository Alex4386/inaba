package host

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// Snapshots of a thread's folder are commits under this ref prefix, one name per
// snapshot: refs/inaba/checkpoints/<thread>/<name>. They sit beside the user's
// branches without being on any of them, so nothing here shows in their history.
const checkpointRefs = "refs/inaba/checkpoints/"

var commitID = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// snapshot records the folder's files, untracked ones included and ignored ones not,
// as a commit that the named ref keeps alive. It returns the commit, or "" when the
// folder is not in a git repository.
//
// The files are staged into an index of its own, so what the user has staged in the
// real one is left exactly as it is.
func snapshot(ctx context.Context, dir, ref string) (string, error) {
	if out, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return "", nil
	}
	tmp, err := os.CreateTemp("", "inaba-index-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	// Starting from a copy of the real index lets git skip files it already knows are unchanged.
	if rel, err := git(ctx, dir, "rev-parse", "--git-path", "index"); err == nil {
		real := strings.TrimSpace(rel)
		if !filepath.IsAbs(real) {
			real = filepath.Join(dir, real)
		}
		if src, err := os.Open(real); err == nil {
			_, _ = io.Copy(tmp, src)
			src.Close()
		}
	}
	tmp.Close()
	env := []string{
		"GIT_INDEX_FILE=" + tmp.Name(),
		// The snapshot commit needs an author; it is never shown as anyone's work.
		"GIT_AUTHOR_NAME=inaba", "GIT_AUTHOR_EMAIL=inaba@localhost", "GIT_COMMITTER_NAME=inaba", "GIT_COMMITTER_EMAIL=inaba@localhost",
	}
	if _, err := gitEnv(ctx, dir, env, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := gitEnv(ctx, dir, env, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", strings.TrimSpace(tree), "-m", "inaba checkpoint"}
	// A repository without a commit yet has no HEAD to hang the snapshot on.
	if head, err := git(ctx, dir, "rev-parse", "-q", "--verify", "HEAD"); err == nil {
		args = append(args, "-p", strings.TrimSpace(head))
	}
	commit, err := gitEnv(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	_, err = git(ctx, dir, "update-ref", ref, commit)
	return commit, err
}

// turnChanges compares the snapshots around a turn. It returns nil when they hold the
// same files.
func turnChanges(ctx context.Context, dir, before, after string) *inabav1.TurnChanges {
	if before == "" || after == "" {
		return nil
	}
	out, err := git(ctx, dir, "diff", "--numstat", "-z", before, after)
	if err != nil {
		return nil
	}
	counts := map[string][2]uint32{}
	readNumstat(out, counts)
	if len(counts) == 0 {
		return nil
	}
	changes := &inabav1.TurnChanges{Before: before, After: after, Files: uint32(len(counts))}
	for _, c := range counts {
		changes.Additions += c[0]
		changes.Deletions += c[1]
	}
	return changes
}

// dropCheckpoints lets go of every snapshot of a thread, so git can reclaim them.
func dropCheckpoints(ctx context.Context, dir, threadID string) {
	out, err := git(ctx, dir, "for-each-ref", "--format=%(refname)", checkpointRefs+threadID+"/")
	if err != nil {
		return
	}
	for _, ref := range strings.Fields(out) {
		if _, err := git(ctx, dir, "update-ref", "-d", ref); err != nil {
			slog.Warn("could not delete a checkpoint", "ref", ref, "error", err.Error())
		}
	}
}

func (h *Host) Restore(ctx context.Context, req *inabav1.GitRestoreRequest) (*inabav1.GitRestoreResponse, error) {
	t, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	// Held so no turn starts while files are being put back.
	t.op.Lock()
	defer t.op.Unlock()
	meta := t.snapshot()
	if !meta.Worktree {
		// The snapshot is of the whole folder. In a folder other threads use too,
		// putting it back would undo their work as well.
		return nil, connect.NewError(connect.CodeFailedPrecondition, "files can be restored only for a thread with a worktree of its own; this one shares its project's folder")
	}
	if meta.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "a turn is running; wait for it or stop it first")
	}
	if !commitID.MatchString(req.Commit) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "that is not a snapshot id")
	}
	return restoreFiles(ctx, meta, req.Commit)
}

// restoreFiles puts the thread's folder back as it was at the snapshot commit. The
// caller holds the thread's op lock and has checked the thread may be restored.
func restoreFiles(ctx context.Context, meta *inabav1.Thread, commit string) (*inabav1.GitRestoreResponse, error) {
	dir := meta.Cwd
	if _, err := git(ctx, dir, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "no such snapshot in this repository")
	}
	// First keep what is there now, so the restore itself can be taken back.
	undo, err := snapshot(ctx, dir, checkpointRefs+meta.Id+"/before-restore-"+randomID())
	if err != nil || undo == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "could not snapshot the current files, so nothing was changed")
	}
	// Files that exist now but did not then are removed; restore below only rewrites
	// and brings back, it does not delete what the snapshot never had.
	added, err := git(ctx, dir, "diff", "--name-only", "--diff-filter=A", "-z", commit, undo)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "git could not compare the snapshot: "+err.Error())
	}
	for _, path := range strings.Split(added, "\x00") {
		if path != "" {
			_ = os.Remove(filepath.Join(dir, path))
		}
	}
	// Working tree only: what the user has staged stays staged.
	if _, err := git(ctx, dir, "restore", "--source="+commit, "--worktree", "--", "."); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "git could not restore the files: "+err.Error())
	}
	return &inabav1.GitRestoreResponse{Status: gitStatus(ctx, dir), Undo: undo}, nil
}
