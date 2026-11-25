package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/db"
	"github.com/Alex4386/inaba/internal/rpc"
	"google.golang.org/protobuf/proto"
)

func (h *Host) GetHousekeeping(ctx context.Context, _ *inabav1.GetHousekeepingRequest) (*inabav1.Housekeeping, error) {
	if err := may(ctx, rpc.PermHousekeeping); err != nil {
		return nil, err
	}
	return h.housekeeping(ctx), nil
}

func (h *Host) CleanUp(ctx context.Context, req *inabav1.CleanUpRequest) (*inabav1.CleanUpResponse, error) {
	if err := may(ctx, rpc.PermHousekeeping); err != nil {
		return nil, err
	}
	now := h.housekeeping(ctx)
	var kept []string
	if req.LeftoverWorktrees {
		for _, w := range now.Worktrees {
			if w.ThreadId != "" {
				continue
			}
			if why := removeLeftoverWorktree(ctx, w, req.DiscardChanges); why != "" {
				kept = append(kept, w.Path+": "+why)
			}
		}
	}
	if req.LeftoverSnapshots {
		threads := h.knownThreads()
		for _, repo := range now.Snapshots {
			for _, id := range snapshotThreads(ctx, repo.Repository) {
				if threads[id] == nil {
					dropCheckpoints(ctx, repo.Repository, id)
				}
			}
		}
	}
	if req.CompactDatabase && h.projects != nil {
		if err := db.Compact(h.projects.db); err != nil {
			kept = append(kept, "the database: "+err.Error())
		}
	}
	return &inabav1.CleanUpResponse{Housekeeping: h.housekeeping(ctx), Kept: kept}, nil
}

// removeLeftoverWorktree removes a worktree no thread uses, and says why when it does not.
func removeLeftoverWorktree(ctx context.Context, w *inabav1.Housekeeping_Worktree, discard bool) string {
	if w.Dirty && !discard {
		return "it has uncommitted changes"
	}
	common, err := git(ctx, w.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		// Not a worktree git knows; whatever it is, a person should look before it goes.
		return "git does not know it as a worktree; remove it by hand if it is not needed"
	}
	args := []string{"--git-dir=" + strings.TrimSpace(common), "worktree", "remove"}
	if discard {
		args = append(args, "--force")
	}
	if _, err := git(ctx, filepath.Dir(w.Path), append(args, w.Path)...); err != nil {
		return err.Error()
	}
	return ""
}

// knownThreads is every thread this host has, by id. Threads whose provider is
// not configured now are not loaded, but they are still there for when it is: their
// worktrees and snapshots are not leftovers.
func (h *Host) knownThreads() map[string]*inabav1.Thread {
	known := map[string]*inabav1.Thread{}
	if h.save != nil {
		var rows []threadRow
		_ = h.save.db.Find(&rows).Error
		for _, row := range rows {
			meta := &inabav1.Thread{}
			if proto.Unmarshal(row.Meta, meta) == nil {
				known[row.ID] = meta
			}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, t := range h.threads {
		known[id] = t.snapshot()
	}
	return known
}

// housekeeping looks at what inaba keeps on this host's disk.
func (h *Host) housekeeping(ctx context.Context) *inabav1.Housekeeping {
	res := &inabav1.Housekeeping{}
	if h.save != nil {
		res.DatabaseBytes = db.Size(h.dbPath)
	}
	byID := h.knownThreads()

	if h.dataDir != "" {
		root := filepath.Join(h.dataDir, "worktrees")
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			w := &inabav1.Housekeeping_Worktree{Path: filepath.Join(root, e.Name())}
			// Worktrees are named after their thread.
			if m := byID[e.Name()]; m != nil && m.Worktree {
				w.ThreadId, w.ThreadTitle = m.Id, m.Title
			}
			if out, err := git(ctx, w.Path, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
				w.Branch = strings.TrimSpace(out)
			}
			out, err := git(ctx, w.Path, "status", "--porcelain")
			w.Dirty = err == nil && strings.TrimSpace(out) != ""
			res.Worktrees = append(res.Worktrees, w)
		}
	}

	// Snapshots live in the repository a thread works in, shared by its worktrees:
	// each repository once, found from the projects and the threads.
	var dirs []string
	if h.projects != nil {
		projects, _ := h.projects.List()
		for _, p := range projects {
			dirs = append(dirs, p.Cwd)
		}
	}
	for _, m := range byID {
		dirs = append(dirs, m.Cwd)
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		common, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || seen[strings.TrimSpace(common)] {
			continue
		}
		seen[strings.TrimSpace(common)] = true
		s := &inabav1.Housekeeping_Snapshots{Repository: dir}
		out, _ := git(ctx, dir, "for-each-ref", "--format=%(refname)", checkpointRefs)
		for _, ref := range strings.Fields(out) {
			s.Refs++
			id, _, _ := strings.Cut(strings.TrimPrefix(ref, checkpointRefs), "/")
			if byID[id] == nil {
				s.LeftoverRefs++
			}
		}
		if s.Refs > 0 {
			res.Snapshots = append(res.Snapshots, s)
		}
	}
	return res
}

// snapshotThreads lists the threads a repository keeps snapshots for.
func snapshotThreads(ctx context.Context, dir string) []string {
	out, _ := git(ctx, dir, "for-each-ref", "--format=%(refname)", checkpointRefs)
	seen := map[string]bool{}
	var ids []string
	for _, ref := range strings.Fields(out) {
		id, _, _ := strings.Cut(strings.TrimPrefix(ref, checkpointRefs), "/")
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}
