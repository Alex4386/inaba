// Package files is the files module: finding, listing and reading the files in a
// thread's folder. It never writes.
package files

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
