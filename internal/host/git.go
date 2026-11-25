package host

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

const (
	gitTimeout = 2 * time.Minute
	// The most diff text sent in one answer.
	maxDiffBytes = 1 << 20
	// Untracked files are counted one git call each; past this many they show without line counts.
	maxUntrackedCounted = 50
)

// git runs a git command in dir and returns what it printed. A failure carries git's
// own message. It never waits for a person: a command that would ask for credentials fails.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitEnv(ctx, dir, nil, args...)
}

// gitEnv is git with extra environment variables set for the one command.
func gitEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return command(ctx, dir, env, "git", args...)
}

// command runs a git-family tool (git, gh) without a terminal: anything that would
// ask a person fails instead, with the tool's own message.
func command(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1"), env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		// Most failures explain themselves on stderr; some, like "nothing to commit", on stdout.
		message := strings.TrimSpace(errOut.String())
		if message == "" {
			message = strings.TrimSpace(out.String())
		}
		if message == "" {
			message = err.Error()
		}
		return out.String(), errors.New(message)
	}
	return out.String(), nil
}

// gitRef names a repository the way every git request does: by project or by thread.
type gitRef interface {
	GetProjectId() string
	GetThreadId() string
}

// gitDir returns the folder a git call runs in: the thread's when a thread is named,
// the project's otherwise.
func (h *Host) gitDir(ref gitRef) (string, error) {
	if id := ref.GetThreadId(); id != "" {
		t, err := h.thread(id)
		if err != nil {
			return "", err
		}
		return t.snapshot().Cwd, nil
	}
	project, err := h.findProject(ref.GetProjectId())
	if err != nil {
		return "", err
	}
	return project.Cwd, nil
}

func (h *Host) findProject(id string) (*Project, error) {
	if h.projects != nil {
		project, err := h.projects.Get(id)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, "could not read the project").WithCause(err)
		}
		if project != nil {
			return project, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, "no project "+id)
}

// threadGit serves a thread's REST git address. A thread working in its project's own
// folder shares the project's repository state, so the caller is sent on to the
// project's address, where that state lives. Any other thread, one outside a project
// or in a folder of its own, is answered here by next.
//
// The redirect is relative, so it holds wherever the host's API is mounted: here, or
// under a host id on inaba-server. A 307 keeps the method and body, so a commit sent
// to the thread's address is still a commit.
func (h *Host) threadGit(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if t, err := h.thread(r.PathValue("thread_id")); err == nil && r.PathValue("rest") != "restore" {
			meta := t.snapshot()
			if project, err := h.findProject(meta.ProjectId); err == nil && project.Cwd == meta.Cwd {
				// Up from /threads/{id}/git/<rest> to the API root, however deep <rest> is.
				rest := r.PathValue("rest")
				target := strings.Repeat("../", 3+strings.Count(rest, "/")) + "projects/" + url.PathEscape(project.ID) + "/git/" + rest
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				w.Header().Set("Location", target)
				w.WriteHeader(http.StatusTemporaryRedirect)
				return
			}
		}
		next.ServeHTTP(w, r)
	}
}

func (h *Host) GetStatus(ctx context.Context, req *inabav1.GetGitStatusRequest) (*inabav1.GitStatus, error) {
	dir, err := h.gitDir(req)
	if err != nil {
		return nil, err
	}
	return gitStatus(ctx, dir), nil
}

// gitStatus describes the working tree. A folder outside any repository, or a machine
// without git, reads as "not a repository" rather than an error.
func gitStatus(ctx context.Context, dir string) *inabav1.GitStatus {
	st := &inabav1.GitStatus{}
	if out, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return st
	}
	st.Repository = true
	branch, _ := git(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD")
	if st.Branch = strings.TrimSpace(branch); st.Branch == "" {
		short, _ := git(ctx, dir, "rev-parse", "--short", "HEAD")
		st.Branch = strings.TrimSpace(short)
	}
	if up, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		st.Upstream = strings.TrimSpace(up)
		if counts, err := git(ctx, dir, "rev-list", "--left-right", "--count", "@{upstream}...HEAD"); err == nil {
			if f := strings.Fields(counts); len(f) == 2 {
				behind, _ := strconv.Atoi(f[0])
				ahead, _ := strconv.Atoi(f[1])
				st.Behind, st.Ahead = uint32(behind), uint32(ahead)
			}
		}
	}

	// Line counts of tracked changes, staged or not, against the last commit. A
	// repository without a commit yet has no HEAD to compare with.
	counts := map[string][2]uint32{}
	if out, err := git(ctx, dir, "diff", "HEAD", "--numstat", "-z"); err == nil {
		readNumstat(out, counts)
	}
	out, _ := git(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	entries := strings.Split(out, "\x00")
	untracked := 0
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		file := &inabav1.GitStatus_File{State: e[:2], Path: e[3:]}
		if e[0] == 'R' || e[0] == 'C' {
			// A rename lists the old path as the next entry.
			i++
		}
		if c, ok := counts[file.Path]; ok {
			file.Additions, file.Deletions = c[0], c[1]
		} else if file.State == "??" && untracked < maxUntrackedCounted {
			untracked++
			// --no-index compares against nothing, and exits 1 because they differ.
			if out, _ := git(ctx, dir, "diff", "--no-index", "--numstat", "-z", "--", os.DevNull, file.Path); out != "" {
				one := map[string][2]uint32{}
				readNumstat(out, one)
				for _, c := range one {
					file.Additions = c[0]
				}
			}
		}
		st.Additions += file.Additions
		st.Deletions += file.Deletions
		st.Files = append(st.Files, file)
	}
	return st
}

// readNumstat reads `git diff --numstat -z` into counts by path. A binary file's
// counts are "-" and read as zero.
func readNumstat(out string, counts map[string][2]uint32) {
	records := strings.Split(out, "\x00")
	for i := 0; i < len(records); i++ {
		f := strings.SplitN(records[i], "\t", 3)
		if len(f) < 3 {
			continue
		}
		add, _ := strconv.Atoi(f[0])
		del, _ := strconv.Atoi(f[1])
		path := f[2]
		if path == "" && i+2 < len(records) {
			// A rename: the old and new paths follow as their own records.
			path = records[i+2]
			i += 2
		}
		counts[path] = [2]uint32{uint32(add), uint32(del)}
	}
}

func (h *Host) GetDiff(ctx context.Context, req *inabav1.GetGitDiffRequest) (*inabav1.GetGitDiffResponse, error) {
	dir, err := h.gitDir(req)
	if err != nil {
		return nil, err
	}
	args := []string{"diff", "HEAD"}
	between := req.From != "" || req.To != ""
	if between {
		if !commitID.MatchString(req.From) || !commitID.MatchString(req.To) {
			return nil, connect.NewError(connect.CodeInvalidArgument, "from and to must both be snapshot ids")
		}
		args = []string{"diff", req.From, req.To}
	}
	if req.IgnoreWhitespace {
		args = append(args, "-w")
	}
	if req.Path != "" {
		// "--" keeps a path that looks like an option from being read as one.
		args = append(args, "--", req.Path)
	}
	diff, err := git(ctx, dir, args...)
	if req.Path != "" && diff == "" && !between {
		// Nothing against HEAD: an untracked file, shown as all additions. A tracked file
		// can be empty here too, when -w hid its only changes; that one stays empty.
		if _, untracked := git(ctx, dir, "ls-files", "--error-unmatch", "--", req.Path); untracked != nil || err != nil {
			diff, _ = git(ctx, dir, "diff", "--no-index", "--", os.DevNull, req.Path)
		}
		err = nil
	}
	if err != nil && diff == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "git could not produce the diff: "+err.Error())
	}
	if req.Path == "" && !between {
		// git diff leaves out files git does not track yet; they are changes all the same.
		untracked, _ := git(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z")
		for _, path := range strings.Split(untracked, "\x00") {
			if path == "" || len(diff) > maxDiffBytes {
				continue
			}
			added, _ := git(ctx, dir, "diff", "--no-index", "--", os.DevNull, path)
			diff += added
		}
	}
	res := &inabav1.GetGitDiffResponse{Diff: diff}
	if req.Path != "" {
		res.Before, res.After, res.Sides = sides(ctx, dir, req)
	}
	if len(diff) > maxDiffBytes {
		res.Diff, res.Truncated = strings.ToValidUTF8(diff[:maxDiffBytes], ""), true
	}
	return res, nil
}

func (h *Host) Commit(ctx context.Context, req *inabav1.GitCommitRequest) (*inabav1.GitCommitResponse, error) {
	dir, err := h.gitDir(req)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a commit needs a message")
	}
	var output strings.Builder
	step := func(what string, args ...string) error {
		out, err := git(ctx, dir, args...)
		output.WriteString(out)
		if err != nil {
			// git's message is the useful part: nothing to commit, rejected push, no identity set.
			return connect.NewError(connect.CodeFailedPrecondition, what+" failed: "+err.Error())
		}
		return nil
	}
	if err := step("staging the changes", "add", "-A"); err != nil {
		return nil, err
	}
	if err := step("the commit", "commit", "-m", req.Message); err != nil {
		return nil, err
	}
	if req.Push {
		if err := step("the commit was made, but the push", pushArgs(ctx, dir)...); err != nil {
			return nil, err
		}
	}
	return &inabav1.GitCommitResponse{Output: output.String(), Status: gitStatus(ctx, dir)}, nil
}

// pushArgs is the push for the current branch: a plain one when it tracks a branch,
// and one that sets origin as its upstream when it tracks none.
func pushArgs(ctx context.Context, dir string) []string {
	if _, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil {
		return []string{"push", "-u", "origin", "HEAD"}
	}
	return []string{"push"}
}

func (h *Host) Push(ctx context.Context, req *inabav1.GitPushRequest) (*inabav1.GitCommitResponse, error) {
	dir, err := h.gitDir(req)
	if err != nil {
		return nil, err
	}
	out, err := git(ctx, dir, pushArgs(ctx, dir)...)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the push failed: "+err.Error())
	}
	return &inabav1.GitCommitResponse{Output: out, Status: gitStatus(ctx, dir)}, nil
}

// CreatePullRequest leaves GitHub to the GitHub CLI: its sign-in, its choice of
// repository and fork, and its messages when something is missing.
func (h *Host) CreatePullRequest(ctx context.Context, req *inabav1.GitPullRequestRequest) (*inabav1.GitPullRequestResponse, error) {
	dir, err := h.gitDir(req)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "pull requests need the GitHub CLI (gh) on the host, signed in with gh auth login")
	}
	branch, err := git(ctx, dir, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "no branch is checked out to open a pull request from")
	}
	branch = strings.TrimSpace(branch)
	gh := func(args ...string) (string, error) { return command(ctx, dir, nil, "gh", args...) }

	// A failure here is most often "no pull requests found"; anything worse fails the create below too.
	if url, err := gh("pr", "view", branch, "--json", "url,state", "-q", `select(.state == "OPEN") | .url`); err == nil && strings.TrimSpace(url) != "" {
		return &inabav1.GitPullRequestResponse{Url: strings.TrimSpace(url), Existing: true, Status: gitStatus(ctx, dir)}, nil
	}
	if st := gitStatus(ctx, dir); st.Upstream == "" || st.Ahead > 0 {
		if _, err := git(ctx, dir, pushArgs(ctx, dir)...); err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, "the push failed: "+err.Error())
		}
	}
	args := []string{"pr", "create", "--head", branch}
	if strings.TrimSpace(req.Title) == "" {
		args = append(args, "--fill")
	} else {
		args = append(args, "--title", req.Title, "--body", req.Body)
	}
	if req.Base != "" {
		args = append(args, "--base", req.Base)
	}
	if req.Draft {
		args = append(args, "--draft")
	}
	out, err := gh(args...)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the branch was pushed, but opening the pull request failed: "+err.Error())
	}
	// gh prints the new pull request's address last.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return &inabav1.GitPullRequestResponse{Url: strings.TrimSpace(lines[len(lines)-1]), Status: gitStatus(ctx, dir)}, nil
}

func (h *Host) ListBranches(ctx context.Context, req *inabav1.ListBranchesRequest) (*inabav1.ListBranchesResponse, error) {
	dir, err := h.gitDir(req)
	if err != nil {
		return nil, err
	}
	out, err := git(ctx, dir, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "git could not list the branches: "+err.Error())
	}
	res := &inabav1.ListBranchesResponse{Branches: strings.Fields(out)}
	current, _ := git(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD")
	res.Current = strings.TrimSpace(current)
	return res, nil
}

func (h *Host) Checkout(ctx context.Context, req *inabav1.GitCheckoutRequest) (*inabav1.GitStatus, error) {
	dir, err := h.gitDir(req)
	if err != nil {
		return nil, err
	}
	// A leading dash would be read by git as an option, whatever the argument position.
	if req.Branch == "" || strings.HasPrefix(req.Branch, "-") {
		return nil, connect.NewError(connect.CodeInvalidArgument, "that is not a branch name")
	}
	if err := h.idleFolder(dir); err != nil {
		return nil, err
	}
	args := []string{"switch", req.Branch}
	if req.Create {
		args = []string{"switch", "-c", req.Branch}
	}
	if _, err := git(ctx, dir, args...); err != nil {
		// Uncommitted changes in the way, a name that is taken: git says which.
		return nil, connect.NewError(connect.CodeFailedPrecondition, "git could not switch branches: "+err.Error())
	}
	return gitStatus(ctx, dir), nil
}

// idleFolder fails while a thread is running a turn in dir: changing the files under an
// agent in the middle of a turn makes it work on something else.
func (h *Host) idleFolder(dir string) error {
	h.mu.Lock()
	threads := make([]*thread, 0, len(h.threads))
	for _, t := range h.threads {
		threads = append(threads, t)
	}
	h.mu.Unlock()
	for _, t := range threads {
		if meta := t.snapshot(); meta.Cwd == dir && meta.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE {
			return connect.NewError(connect.CodeFailedPrecondition, "a thread is running a turn in this folder; wait for it or stop it first")
		}
	}
	return nil
}

func (h *Host) Fetch(ctx context.Context, req *inabav1.GitFetchRequest) (*inabav1.GitCommitResponse, error) {
	dir, err := h.gitDir(req)
	if err != nil {
		return nil, err
	}
	// git fetch reports progress on stderr, so the output is usually empty on success.
	out, err := git(ctx, dir, "fetch")
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the fetch failed: "+err.Error())
	}
	return &inabav1.GitCommitResponse{Output: out, Status: gitStatus(ctx, dir)}, nil
}

func (h *Host) Pull(ctx context.Context, req *inabav1.GitPullBranchRequest) (*inabav1.GitCommitResponse, error) {
	dir, err := h.gitDir(req)
	if err != nil {
		return nil, err
	}
	if err := h.idleFolder(dir); err != nil {
		return nil, err
	}
	// Fast-forward only: a merge or rebase can stop halfway with conflicts, which the
	// web UI has no way to resolve. git's message says what is in the way.
	out, err := git(ctx, dir, "pull", "--ff-only")
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the pull failed (only fast-forwards are made; merge or rebase in a terminal when the branch has diverged): "+err.Error())
	}
	return &inabav1.GitCommitResponse{Output: out, Status: gitStatus(ctx, dir)}, nil
}

// addWorktree gives a thread a checkout of its own beside the host's database, on the
// branch asked for or a new one named after the thread.
func (h *Host) addWorktree(ctx context.Context, project *Project, threadID string, w *inabav1.Worktree) (string, error) {
	if h.dataDir == "" {
		return "", connect.NewError(connect.CodeFailedPrecondition, "this host was started without a data folder to keep worktrees in")
	}
	branch := w.Branch
	if branch == "" {
		branch = "inaba/" + threadID[:8]
	}
	if strings.HasPrefix(branch, "-") || strings.HasPrefix(w.Base, "-") {
		return "", connect.NewError(connect.CodeInvalidArgument, "that is not a branch name")
	}
	dir := filepath.Join(h.dataDir, "worktrees", threadID)
	// An existing branch is checked out as it is; a new one starts from the base.
	args := []string{"worktree", "add", dir, branch}
	if _, err := git(ctx, project.Cwd, "rev-parse", "--verify", "-q", "refs/heads/"+branch); err != nil {
		args = []string{"worktree", "add", "-b", branch, dir}
		if w.Base != "" {
			args = append(args, w.Base)
		}
	}
	if _, err := git(ctx, project.Cwd, args...); err != nil {
		// Not a repository, a branch already checked out elsewhere, an unknown base: git says which.
		return "", connect.NewError(connect.CodeFailedPrecondition, "git could not make the worktree: "+err.Error())
	}
	return dir, nil
}

// removeWorktree takes away a deleted thread's worktree. One with uncommitted changes
// is left where it is: git refuses to remove it, and those changes may be wanted.
func (h *Host) removeWorktree(ctx context.Context, meta *inabav1.Thread) {
	if !meta.Worktree {
		return
	}
	project, err := h.findProject(meta.ProjectId)
	if err != nil {
		return
	}
	if _, err := git(ctx, project.Cwd, "worktree", "remove", meta.Cwd); err != nil {
		slog.Warn("left a deleted thread's worktree in place", "path", meta.Cwd, "reason", err.Error())
	}
}

// maxSideBytes bounds each side of a side-by-side diff.
const maxSideBytes = 1 << 20

// sides reads one path whole before and after the change it diffs. A side the file
// is missing from is empty; a binary or large file has no sides to show.
func sides(ctx context.Context, dir string, req *inabav1.GetGitDiffRequest) (before, after string, ok bool) {
	if !filepath.IsLocal(req.Path) {
		return "", "", false
	}
	from, to := "HEAD", ""
	if req.From != "" || req.To != "" {
		from, to = req.From, req.To
	}
	// A file new on this side has no before; git's refusal says so.
	before, _ = git(ctx, dir, "show", from+":"+filepath.ToSlash(req.Path))
	if to != "" {
		after, _ = git(ctx, dir, "show", to+":"+filepath.ToSlash(req.Path))
	} else if b, err := os.ReadFile(filepath.Join(dir, req.Path)); err == nil {
		after = string(b)
	}
	if len(before) > maxSideBytes || len(after) > maxSideBytes || strings.ContainsRune(before, 0) || strings.ContainsRune(after, 0) {
		return "", "", false
	}
	return before, after, true
}
