// Package terminal is the terminal module: shells on the host that belong to no project
// or thread, opened from the web UI and running until closed.
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	"go.uber.org/fx"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/provider/shell"
	"github.com/Alex4386/inaba/internal/ptyterm"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/module"
)

// Module is the terminal module, for a host to run.
var Module = module.Module{
	Name:        "terminal",
	Title:       "Terminals",
	Description: "Shells on this machine apart from any project, opened from the web UI and kept running until closed.",
	Default:     true,
	Option: fx.Options(
		fx.Provide(newService),
		module.Provide(func(s *service) module.Contribution {
			return module.Contribution{Services: func(server *connect.Server) { inabav1connect.RegisterHostTerminalServiceHandler(server, s) }}
		}),
		fx.Invoke(func(lc fx.Lifecycle, s *service) { lc.Append(fx.StopHook(s.stop)) }),
	),
}

// maxTerminals bounds the processes the module runs.
const maxTerminals = 32

type entry struct {
	info *inabav1.HostTerminal
	term *ptyterm.Terminal
	// owner is the account that opened it, the only one besides trusted calls to see it.
	owner string
}

// mine reports whether the caller may see and use e.
func mine(ctx context.Context, e *entry) bool {
	user, trusted := rpc.Caller(ctx)
	return trusted || e.owner == user
}

type service struct {
	inabav1connect.UnimplementedHostTerminalServiceHandler

	mu sync.Mutex
	// terminals in the order they were opened.
	terminals []*entry
	// stopped is set once the module stops; a late open must not start a shell no one will end.
	stopped bool
}

func newService() *service { return &service{} }

func (s *service) stop() {
	s.mu.Lock()
	s.stopped = true
	all := s.terminals
	s.terminals = nil
	s.mu.Unlock()
	for _, e := range all {
		e.term.Close()
	}
}

// size is a viewer's terminal size, or zero for the default.
func size(cols, rows uint32) (uint16, uint16) {
	if cols > 0 && rows > 0 && cols <= 1000 && rows <= 1000 {
		return uint16(cols), uint16(rows)
	}
	return 0, 0
}

// find is the caller's terminal by id, and its index; another account's is not found, so
// its id is not confirmed.
func (s *service) find(ctx context.Context, id string) (int, *entry) {
	for i, e := range s.terminals {
		if e.info.Id == id && mine(ctx, e) {
			return i, e
		}
	}
	return -1, nil
}

func (s *service) get(ctx context.Context, id string) (*entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, e := s.find(ctx, id); e != nil {
		return e, nil
	}
	return nil, connect.NewError(connect.CodeNotFound, "no terminal "+id)
}

// view is a terminal as callers see it, with whether it has exited and its title now.
func view(e *entry) *inabav1.HostTerminal {
	return &inabav1.HostTerminal{Id: e.info.Id, Cwd: e.info.Cwd, StartedAt: e.info.StartedAt, Exited: e.term.Exited(), Title: e.term.Title(), Name: e.info.Name}
}

func (s *service) ListHostTerminals(ctx context.Context, _ *inabav1.ListHostTerminalsRequest) (*inabav1.ListHostTerminalsResponse, error) {
	if err := rpc.May(ctx, rpc.PermThreadsAnywhere); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &inabav1.ListHostTerminalsResponse{}
	for _, e := range s.terminals {
		if mine(ctx, e) {
			res.Terminals = append(res.Terminals, view(e))
		}
	}
	return res, nil
}

func (s *service) OpenHostTerminal(ctx context.Context, req *inabav1.OpenHostTerminalRequest) (*inabav1.HostTerminal, error) {
	if err := rpc.May(ctx, rpc.PermThreadsAnywhere); err != nil {
		return nil, err
	}
	dir := req.Cwd
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, "this machine has no home folder; name a folder")
		}
		dir = home
	}
	if !filepath.IsAbs(dir) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the folder must be an absolute path")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, connect.NewError(connect.CodeInvalidArgument, dir+" is not a folder")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, connect.NewError(connect.CodeUnavailable, "the terminal module is stopping")
	}
	if len(s.terminals) >= maxTerminals {
		return nil, connect.NewError(connect.CodeResourceExhausted, "a host runs at most "+strconv.Itoa(maxTerminals)+" terminals; close one first")
	}
	cols, rows := size(req.Cols, req.Rows)
	argv := shell.Provider{}.TerminalCommand("")
	t, err := ptyterm.Start(argv, dir, cols, rows)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "could not start "+argv[0]+": "+err.Error())
	}
	owner, _ := rpc.Caller(ctx)
	e := &entry{info: &inabav1.HostTerminal{Id: newID(), Cwd: dir, StartedAt: timestamppb.New(time.Now())}, term: t, owner: owner}
	s.terminals = append(s.terminals, e)
	return view(e), nil
}

func (s *service) AttachHostTerminal(ctx context.Context, req *inabav1.AttachHostTerminalRequest, stream inabav1connect.HostTerminalServiceAttachHostTerminalServerStream) error {
	if err := rpc.May(ctx, rpc.PermThreadsAnywhere); err != nil {
		return err
	}
	e, err := s.get(ctx, req.Id)
	if err != nil {
		return err
	}
	if cols, rows := size(req.Cols, req.Rows); cols > 0 && !e.term.Exited() {
		_ = e.term.Resize(cols, rows)
	}
	return e.term.Stream(ctx, func(data []byte, cols, rows uint16) error {
		return stream.Send(&inabav1.TerminalOutput{Data: data, Cols: uint32(cols), Rows: uint32(rows)})
	})
}

func (s *service) WriteHostTerminal(ctx context.Context, req *inabav1.WriteHostTerminalRequest) (*inabav1.WriteHostTerminalResponse, error) {
	if err := rpc.May(ctx, rpc.PermThreadsAnywhere); err != nil {
		return nil, err
	}
	e, err := s.get(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := e.term.Write(req.Data); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return &inabav1.WriteHostTerminalResponse{}, nil
}

func (s *service) ResizeHostTerminal(ctx context.Context, req *inabav1.ResizeHostTerminalRequest) (*inabav1.ResizeHostTerminalResponse, error) {
	if err := rpc.May(ctx, rpc.PermThreadsAnywhere); err != nil {
		return nil, err
	}
	cols, rows := size(req.Cols, req.Rows)
	if cols == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "cols and rows must be between 1 and 1000")
	}
	e, err := s.get(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := e.term.Resize(cols, rows); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return &inabav1.ResizeHostTerminalResponse{}, nil
}

// maxName bounds a terminal's name, in characters.
const maxName = 200

func (s *service) RenameHostTerminal(ctx context.Context, req *inabav1.RenameHostTerminalRequest) (*inabav1.HostTerminal, error) {
	if err := rpc.May(ctx, rpc.PermThreadsAnywhere); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if utf8.RuneCountInString(name) > maxName {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("a terminal's name is at most %d characters", maxName))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e := s.find(ctx, req.Id)
	if e == nil {
		return nil, connect.NewError(connect.CodeNotFound, "no terminal "+req.Id)
	}
	e.info.Name = name
	return view(e), nil
}

func (s *service) CloseHostTerminal(ctx context.Context, req *inabav1.CloseHostTerminalRequest) (*inabav1.CloseHostTerminalResponse, error) {
	if err := rpc.May(ctx, rpc.PermThreadsAnywhere); err != nil {
		return nil, err
	}
	s.mu.Lock()
	i, gone := s.find(ctx, req.Id)
	if gone == nil {
		s.mu.Unlock()
		// Already gone, or not the caller's: closing it is done either way, and confirms nothing.
		return &inabav1.CloseHostTerminalResponse{}, nil
	}
	s.terminals = append(s.terminals[:i:i], s.terminals[i+1:]...)
	s.mu.Unlock()
	gone.term.Close()
	return &inabav1.CloseHostTerminalResponse{}, nil
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
