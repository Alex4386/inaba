// Package sideshell is the shell module: a shell in each thread's folder, beside its
// conversation, for running commands while the agent works.
package sideshell

import (
	"context"
	"regexp"
	"strconv"
	"sync"

	"connectrpc.com/connect/v2"
	"go.uber.org/fx"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/provider/shell"
	"github.com/Alex4386/inaba/internal/ptyterm"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/module"
)

// Module is the shell module, for a host to run.
var Module = module.Module{
	Name:        "shell",
	Title:       "Terminal beside threads",
	Description: "A shell in each thread's folder, next to the conversation, for running commands while the agent works.",
	Default:     true,
	Option: fx.Options(
		fx.Provide(newService),
		module.Provide(func(s *service) module.Contribution {
			return module.Contribution{Services: func(server *connect.Server) { inabav1connect.RegisterShellServiceHandler(server, s) }}
		}),
		fx.Invoke(func(lc fx.Lifecycle, s *service) { lc.Append(fx.StartStopHook(s.start, s.stop)) }),
	),
}

type service struct {
	inabav1connect.UnimplementedShellServiceHandler
	threads module.Threads
	events  module.Events

	mu sync.Mutex
	// shells are by thread, then by shell id.
	shells map[string]map[string]*ptyterm.Terminal
	// stopped is set once the module stops; a late attach must not start a shell no one will end.
	stopped    bool
	stopFollow func()
}

func newService(threads module.Threads, events module.Events) *service {
	return &service{threads: threads, events: events, shells: map[string]map[string]*ptyterm.Terminal{}}
}

func (s *service) start() { s.stopFollow = s.events.OnDelete(s.end) }

func (s *service) stop() {
	if s.stopFollow != nil {
		s.stopFollow()
	}
	s.mu.Lock()
	s.stopped = true
	shells := s.shells
	s.shells = map[string]map[string]*ptyterm.Terminal{}
	s.mu.Unlock()
	for _, byID := range shells {
		for _, t := range byID {
			t.Close()
		}
	}
}

// end closes a thread's shells, as its thread goes.
func (s *service) end(threadID string) {
	s.mu.Lock()
	byID := s.shells[threadID]
	delete(s.shells, threadID)
	s.mu.Unlock()
	for _, t := range byID {
		t.Close()
	}
}

// maxShells bounds the processes one thread's shells can run.
const maxShells = 16

var validShellID = regexp.MustCompile(`^[A-Za-z0-9_-]{0,32}$`)

func checkShellID(id string) error {
	if !validShellID.MatchString(id) {
		return connect.NewError(connect.CodeInvalidArgument, "a shell id is up to 32 letters, digits, - and _")
	}
	return nil
}

// open returns one of the thread's shells, starting it if it is not running.
func (s *service) open(threadID, shellID string, cols, rows uint16) (*ptyterm.Terminal, error) {
	if err := checkShellID(shellID); err != nil {
		return nil, err
	}
	th, err := s.threads.GetThread(rpc.Trusted(context.Background()), &inabav1.GetThreadRequest{ThreadId: threadID})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, connect.NewError(connect.CodeUnavailable, "the shell module is stopping")
	}
	byID := s.shells[threadID]
	if t := byID[shellID]; t != nil && !t.Exited() {
		if cols > 0 && rows > 0 {
			_ = t.Resize(cols, rows)
		}
		return t, nil
	}
	if _, again := byID[shellID]; !again && len(byID) >= maxShells {
		return nil, connect.NewError(connect.CodeResourceExhausted, "a thread has at most "+strconv.Itoa(maxShells)+" shells; close one first")
	}
	argv := shell.Provider{}.TerminalCommand("")
	t, err := ptyterm.Start(argv, th.Cwd, cols, rows)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "could not start "+argv[0]+": "+err.Error())
	}
	if byID == nil {
		byID = map[string]*ptyterm.Terminal{}
		s.shells[threadID] = byID
	}
	byID[shellID] = t
	return t, nil
}

// running returns one of the thread's shells if it was started.
func (s *service) running(threadID, shellID string) (*ptyterm.Terminal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.shells[threadID][shellID]; t != nil {
		return t, nil
	}
	return nil, connect.NewError(connect.CodeFailedPrecondition, "that shell is not running; attach to it to start it")
}

func (s *service) CloseShell(_ context.Context, req *inabav1.CloseShellRequest) (*inabav1.CloseShellResponse, error) {
	s.mu.Lock()
	t := s.shells[req.ThreadId][req.ShellId]
	delete(s.shells[req.ThreadId], req.ShellId)
	s.mu.Unlock()
	if t != nil {
		t.Close()
	}
	return &inabav1.CloseShellResponse{}, nil
}

func (s *service) AttachShell(ctx context.Context, req *inabav1.AttachShellRequest, stream inabav1connect.ShellServiceAttachShellServerStream) error {
	var cols, rows uint16
	if req.Cols > 0 && req.Rows > 0 && req.Cols <= 1000 && req.Rows <= 1000 {
		cols, rows = uint16(req.Cols), uint16(req.Rows)
	}
	t, err := s.open(req.ThreadId, req.ShellId, cols, rows)
	if err != nil {
		return err
	}
	return t.Stream(ctx, func(data []byte, cols, rows uint16) error {
		return stream.Send(&inabav1.TerminalOutput{Data: data, Cols: uint32(cols), Rows: uint32(rows)})
	})
}

func (s *service) WriteShell(_ context.Context, req *inabav1.WriteShellRequest) (*inabav1.WriteShellResponse, error) {
	t, err := s.running(req.ThreadId, req.ShellId)
	if err != nil {
		return nil, err
	}
	if err := t.Write(req.Data); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return &inabav1.WriteShellResponse{}, nil
}

func (s *service) ResizeShell(_ context.Context, req *inabav1.ResizeShellRequest) (*inabav1.ResizeShellResponse, error) {
	if req.Cols == 0 || req.Rows == 0 || req.Cols > 1000 || req.Rows > 1000 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "cols and rows must be between 1 and 1000")
	}
	t, err := s.running(req.ThreadId, req.ShellId)
	if err != nil {
		return nil, err
	}
	if err := t.Resize(uint16(req.Cols), uint16(req.Rows)); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return &inabav1.ResizeShellResponse{}, nil
}
