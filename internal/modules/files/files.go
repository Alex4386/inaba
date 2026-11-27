// Package files is the files module: finding, listing and reading the files in a
// thread's folder. It never writes.
package files

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	"go.uber.org/fx"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/module"
)

// Module is the files module, for a host to run.
var Module = module.Module{
	Name:        "files",
	Title:       "Files",
	Description: "Find files to point an agent at with @, and browse and read a thread's folder.",
	Default:     true,
	Option: fx.Options(
		fx.Provide(func(threads module.Threads) *service { return &service{threads: threads} }),
		module.Provide(func(s *service) module.Contribution {
			return module.Contribution{Services: func(server *connect.Server) { inabav1connect.RegisterFileServiceHandler(server, s) }}
		}),
	),
}

const (
	maxSearchResults = 50
	// Past this many files a folder outside git is searched only in part.
	maxWalkedFiles = 20000
)

type service struct {
	inabav1connect.UnimplementedFileServiceHandler
	threads module.Threads
}

// folder is the thread's folder.
func (s *service) folder(threadID string) (string, error) {
	th, err := s.threads.GetThread(rpc.Trusted(context.Background()), &inabav1.GetThreadRequest{ThreadId: threadID})
	if err != nil {
		return "", err
	}
	return th.Cwd, nil
}

// The most of a file ReadFile sends.
const maxReadBytes = 1 << 20

// inside resolves a path given relative to the thread's folder, refusing one that
// leads out of it, through .. or a symbolic link.
func (s *service) inside(threadID, rel string) (string, error) {
	root, err := s.folder(threadID)
	if err != nil {
		return "", err
	}
	if rel != "" && !filepath.IsLocal(rel) {
		return "", connect.NewError(connect.CodeInvalidArgument, rel+" is not a path inside the thread's folder")
	}
	path := filepath.Join(root, rel)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", connect.NewError(connect.CodeNotFound, rel+" does not exist in the thread's folder")
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return "", connect.NewError(connect.CodeInvalidArgument, rel+" leads outside the thread's folder")
	}
	return real, nil
}

func (s *service) ListFolder(_ context.Context, req *inabav1.ListFolderRequest) (*inabav1.ListFolderResponse, error) {
	dir, err := s.inside(req.ThreadId, req.Path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the host cannot read that folder")
	}
	res := &inabav1.ListFolderResponse{}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		entry := &inabav1.ListFolderResponse_Entry{Name: e.Name()}
		// Follow links, so a linked folder opens like any other.
		if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
			entry.Folder, entry.Size = info.IsDir(), uint64(info.Size())
		}
		res.Entries = append(res.Entries, entry)
	}
	slices.SortFunc(res.Entries, func(a, b *inabav1.ListFolderResponse_Entry) int {
		if a.Folder != b.Folder {
			if a.Folder {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return res, nil
}

func (s *service) ReadFile(_ context.Context, req *inabav1.ReadFileRequest) (*inabav1.ReadFileResponse, error) {
	path, err := s.inside(req.ThreadId, req.Path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the host cannot read that file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return nil, connect.NewError(connect.CodeInvalidArgument, req.Path+" is a folder")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxReadBytes))
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the host cannot read that file")
	}
	return &inabav1.ReadFileResponse{Data: data, MimeType: http.DetectContentType(data), Size: uint64(info.Size()), Truncated: info.Size() > maxReadBytes, Version: version(data)}, nil
}

// version names a file's content.
func version(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

func (s *service) WriteFile(_ context.Context, req *inabav1.WriteFileRequest) (*inabav1.WriteFileResponse, error) {
	if len(req.Data) > maxReadBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a file saved here is at most 1 MiB")
	}
	path, err := s.inside(req.ThreadId, req.Path)
	if err != nil {
		return nil, err
	}
	// git's own files stay git's. The resolved path is checked too: where case does not
	// matter, or through a link, .git has other names.
	if slices.ContainsFunc(strings.Split(filepath.ToSlash(req.Path)+"/"+filepath.ToSlash(path), "/"), func(p string) bool { return strings.EqualFold(p, ".git") }) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "files under .git are not saved from here")
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, connect.NewError(connect.CodeInvalidArgument, req.Path+" is a folder")
		}
		mode = info.Mode().Perm()
		if req.BaseVersion != "" {
			if info.Size() > maxReadBytes {
				return nil, connect.NewError(connect.CodeFailedPrecondition, "the file is too large to save from here")
			}
			current, err := os.ReadFile(path)
			if err == nil && version(current) != req.BaseVersion {
				return nil, connect.NewError(connect.CodeAborted, req.Path+" changed since it was opened; reload it before saving")
			}
		}
	}
	// Written beside it and moved over, so a reader never sees half a file.
	tmp := path + ".inaba-save"
	if err := os.WriteFile(tmp, req.Data, mode); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the host cannot write there")
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the host cannot write there")
	}
	return &inabav1.WriteFileResponse{Version: version(req.Data)}, nil
}

func (s *service) SearchFiles(ctx context.Context, req *inabav1.SearchFilesRequest) (*inabav1.SearchFilesResponse, error) {
	dir, err := s.folder(req.ThreadId)
	if err != nil {
		return nil, err
	}
	var paths []string
	if out, err := gitFiles(ctx, dir); err == nil {
		paths = strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	} else {
		// Not a repository: walk it, skipping hidden folders and dependencies.
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || len(paths) >= maxWalkedFiles {
				return filepath.SkipDir
			}
			if d.IsDir() && path != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				rel, _ := filepath.Rel(dir, path)
				paths = append(paths, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	return &inabav1.SearchFilesResponse{Paths: matchPaths(paths, req.Query, maxSearchResults)}, nil
}

// matchPaths keeps the paths holding the query's letters in order, case aside, best
// first: the query inside the file name, then inside the path, then scattered; shorter
// paths first within each.
func matchPaths(paths []string, query string, limit int) []string {
	q := strings.ToLower(query)
	type match struct {
		path string
		rank int
	}
	var found []match
	for _, p := range paths {
		if p == "" {
			continue
		}
		lower := strings.ToLower(p)
		rank := 2
		switch {
		case strings.Contains(strings.ToLower(filepath.Base(p)), q):
			rank = 0
		case strings.Contains(lower, q):
			rank = 1
		case !inOrder(lower, q):
			continue
		}
		found = append(found, match{p, rank})
	}
	slices.SortStableFunc(found, func(a, b match) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(len(a.path), len(b.path)), strings.Compare(a.path, b.path))
	})
	out := make([]string, 0, min(limit, len(found)))
	for _, m := range found[:min(limit, len(found))] {
		out = append(out, m.path)
	}
	return out
}

func inOrder(s, letters string) bool {
	for _, r := range letters {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}

// gitFiles lists the files git sees in dir, tracked and untracked, ignored ones left out.
func gitFiles(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return string(out), err
}

const (
	maxGrepMatches = 500
	maxGrepPerFile = 50
	// A file larger than this is left out of a search outside git.
	maxGrepFileBytes = 1 << 20
	// Lines longer than this in git grep's output end the search there.
	maxGrepLineBytes = 1 << 20
	// How much of a matching line is sent, in bytes, around the match.
	grepContext = 200
	grepTimeout = 10 * time.Second
)

func (s *service) GrepFiles(ctx context.Context, req *inabav1.GrepFilesRequest) (*inabav1.GrepFilesResponse, error) {
	dir, err := s.folder(req.ThreadId)
	if err != nil {
		return nil, err
	}
	res := &inabav1.GrepFilesResponse{}
	if req.Query == "" {
		return res, nil
	}
	// The same pattern finds where in a line the match is, whichever way the lines were found.
	expr := req.Query
	if !req.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	// Group 1 is the match; whole words are edged by hand, as \b only knows ASCII letters.
	expr = "(" + expr + ")"
	if req.WholeWord {
		expr = `(?:^|[^\p{L}\p{N}_])` + expr + `(?:[^\p{L}\p{N}_]|$)`
	}
	if !req.CaseSensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "not a regular expression: "+err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	g := &grep{re: re, res: res}
	if err := g.git(ctx, dir, req); err != nil {
		if bad := (*badPattern)(nil); errors.As(err, &bad) {
			return nil, connect.NewError(connect.CodeInvalidArgument, "not a regular expression: "+bad.msg)
		}
		if ctx.Err() == nil && !g.full() {
			// Not a repository, or git cannot search it: read the files here.
			g.res.Matches, g.res.Truncated, g.counts = nil, false, nil
			g.walk(ctx, dir)
		}
	}
	if ctx.Err() != nil {
		res.Truncated = true
	}
	return res, nil
}

type grep struct {
	re     *regexp.Regexp
	res    *inabav1.GrepFilesResponse
	counts map[string]int
}

// badPattern is git refusing the query, which Go's syntax accepted.
type badPattern struct{ msg string }

func (e *badPattern) Error() string { return e.msg }

func (g *grep) full() bool { return len(g.res.Matches) >= maxGrepMatches }

// add records a match of path's line n, unless the file or the search has had its share.
func (g *grep) add(path string, n int, line string) {
	// Lines in another encoding are sent as far as they are UTF-8, as proto strings must be.
	path, line = strings.ToValidUTF8(path, "\uFFFD"), strings.ToValidUTF8(line, "\uFFFD")
	if g.counts == nil {
		g.counts = map[string]int{}
	}
	if g.full() || g.counts[path] >= maxGrepPerFile {
		g.res.Truncated = true
		return
	}
	loc := g.re.FindStringSubmatchIndex(line)
	if loc == nil {
		return
	}
	g.counts[path]++
	from, to := loc[2], loc[3]
	col := utf16Len(line[:from]) + 1
	// Around the match, cut on whole characters, without leading space; a long match is cut too.
	start, end := max(0, from-grepContext/4), min(len(line), from+grepContext*3/4)
	for start > 0 && !utf8.RuneStart(line[start]) {
		start--
	}
	for end < len(line) && !utf8.RuneStart(line[end]) {
		end++
	}
	text := strings.TrimLeft(line[start:end], " \t")
	start = end - len(text)
	if start > from {
		start = from
		text = line[start:end]
	}
	ms := utf16Len(line[start:from])
	g.res.Matches = append(g.res.Matches, &inabav1.GrepFilesResponse_Match{
		Path: path, Line: uint32(n), Column: uint32(col), Text: text,
		MatchStart: uint32(ms), MatchEnd: uint32(ms + utf16Len(line[from:min(to, end)])),
	})
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// git searches with git grep, which leaves ignored and binary files out.
func (g *grep) git(ctx context.Context, dir string, req *inabav1.GrepFilesRequest) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	// shortcut: git's -E is POSIX, so Go-only syntax such as \d matches nothing here; -P if that is missed.
	args := []string{"grep", "-n", "-z", "-I", "--no-color", "--no-column", "--no-full-name", "--untracked", "-E"}
	if !req.Regex {
		args[len(args)-1] = "-F"
	}
	if !req.CaseSensitive {
		args = append(args, "-i")
	}
	if req.WholeWord {
		args = append(args, "-w")
	}
	// -e, so a query starting with - is not taken for an option.
	cmd := exec.CommandContext(ctx, "git", append(args, "-e", req.Query)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(nil, maxGrepLineBytes)
	for sc.Scan() {
		if g.full() {
			break
		}
		// path NUL line NUL text
		parts := strings.SplitN(sc.Text(), "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		n, _ := strconv.Atoi(parts[1])
		g.add(parts[0], n, strings.TrimSuffix(parts[2], "\r"))
	}
	if sc.Err() != nil || g.full() {
		g.res.Truncated = true
		stop()
		_ = cmd.Wait()
		return nil
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil // nothing matched
	}
	// git's POSIX syntax refuses some of Go's, such as (?:x); outside git it would search ignored files.
	if msg := stderr.String(); req.Regex && strings.Contains(msg, "regular expression") {
		return &badPattern{strings.TrimSpace(strings.TrimPrefix(msg, "fatal: "))}
	}
	return err
}

// walk searches the folder outside git, skipping hidden folders, dependencies, links,
// binary files and large ones.
func (g *grep) walk(ctx context.Context, dir string) {
	files := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil || g.full() || files >= maxWalkedFiles {
			// shortcut: says truncated once full even if nothing more would match.
			g.res.Truncated = true
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		files++
		if info, err := d.Info(); err != nil || info.Size() > maxGrepFileBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(data), "\n") {
			if g.re.MatchString(line) {
				g.add(rel, i+1, strings.TrimSuffix(line, "\r"))
			}
		}
		return nil
	})
}
