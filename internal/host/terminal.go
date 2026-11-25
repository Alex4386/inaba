package host

import (
	"context"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/ptyterm"
	"github.com/Alex4386/inaba/pkg/provider"
)

// terminalArgv returns the CLI to run for terminal mode.
func (t *thread) terminalArgv() ([]string, error) {
	meta := t.snapshot()
	if tr, ok := t.prov.(provider.TerminalResumer); ok {
		if argv := tr.TerminalCommand(meta.NativeSessionId); len(argv) > 0 {
			return argv, nil
		}
	}
	return nil, connect.NewError(connect.CodeUnimplemented, "provider "+meta.Provider+" has no terminal mode")
}

func (t *thread) startTerminal() error {
	argv, err := t.terminalArgv()
	if err != nil {
		return err
	}
	// The agent's CLI starts before anyone looks at it; a viewer's Attach gives it their size.
	term, err := ptyterm.Start(argv, t.snapshot().Cwd, 0, 0)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, "could not start "+argv[0]+": "+err.Error())
	}
	t.mu.Lock()
	t.term = term
	t.meta.Mode = inabav1.ThreadMode_THREAD_MODE_TERMINAL
	t.save.meta(t.meta)
	t.mu.Unlock()
	return nil
}

func (h *Host) terminal(threadID string) (*ptyterm.Terminal, error) {
	t, err := h.thread(threadID)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.term == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"thread has no terminal; switch it to terminal mode first")
	}
	return t.term, nil
}

func (h *Host) Attach(ctx context.Context, req *inabav1.AttachRequest, stream inabav1connect.TerminalServiceAttachServerStream) error {
	term, err := h.terminal(req.ThreadId)
	if err != nil {
		return err
	}
	if validSize(req.Cols, req.Rows) {
		_ = term.Resize(uint16(req.Cols), uint16(req.Rows))
	}
	return term.Stream(ctx, func(data []byte, cols, rows uint16) error {
		return stream.Send(&inabav1.TerminalOutput{Data: data, Cols: uint32(cols), Rows: uint32(rows)})
	})
}

func (h *Host) Write(_ context.Context, req *inabav1.WriteRequest) (*inabav1.WriteResponse, error) {
	term, err := h.terminal(req.ThreadId)
	if err != nil {
		return nil, err
	}
	if err := term.Write(req.Data); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return &inabav1.WriteResponse{}, nil
}

func (h *Host) Resize(_ context.Context, req *inabav1.ResizeRequest) (*inabav1.ResizeResponse, error) {
	term, err := h.terminal(req.ThreadId)
	if err != nil {
		return nil, err
	}
	if err := checkSize(req.Cols, req.Rows); err != nil {
		return nil, err
	}
	if err := term.Resize(uint16(req.Cols), uint16(req.Rows)); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return &inabav1.ResizeResponse{}, nil
}

func checkSize(cols, rows uint32) error {
	if cols == 0 || rows == 0 || cols > 1000 || rows > 1000 {
		return connect.NewError(connect.CodeInvalidArgument, "cols and rows must be between 1 and 1000")
	}
	return nil
}

// validSize reports whether a viewer's terminal size is one to apply.
func validSize(cols, rows uint32) bool { return cols > 0 && rows > 0 && cols <= 1000 && rows <= 1000 }
