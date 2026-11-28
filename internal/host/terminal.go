package host

import (
	"context"
	"runtime"
	"strings"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/ptyterm"
	"github.com/Alex4386/inaba/pkg/provider"
)

// terminalArgv returns the CLI to run for terminal mode: resuming the agent's session when it has a
// conversation to resume, the plain CLI otherwise.
func (t *thread) terminalArgv() ([]string, error) {
	resume := t.resumable()
	argv, err := t.terminalCommand(resume)
	if err != nil || resume == "" {
		return argv, err
	}
	if fresh, err := t.terminalCommand(""); err == nil {
		argv = resumeOrAnew(argv, fresh)
	}
	return argv, nil
}

// terminalCommand is the provider's CLI, resuming nativeID when one is given.
func (t *thread) terminalCommand(nativeID string) ([]string, error) {
	if tr, ok := t.prov.(provider.TerminalResumer); ok {
		if argv := tr.TerminalCommand(nativeID); len(argv) > 0 {
			return argv, nil
		}
	}
	return nil, connect.NewError(connect.CodeUnimplemented, "provider "+t.snapshot().Provider+" has no terminal mode")
}

// resumable is the agent session a terminal can go on with, if any: one where something was said. A
// session opened for a thread is named at once, before its first message, and an agent's CLI asked to
// resume one with no conversation gives up ("no conversation found"), leaving a dead terminal.
func (t *thread) resumable() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.meta.NativeSessionId == "" {
		return ""
	}
	for _, e := range t.events {
		if e.GetUserMessage() != nil {
			return t.meta.NativeSessionId
		}
	}
	return ""
}

// resumeOrAnew runs resume, and the plain CLI in the same terminal if resume gives up within a few
// seconds, as an agent's CLI does when it keeps no conversation under the session asked for: a terminal to
// work in rather than one that only says why not. One that ran a while and ended stays ended.
// shortcut: by sh, so not on Windows, where a session that cannot be resumed leaves the terminal ended.
func resumeOrAnew(resume, fresh []string) []string {
	if runtime.GOOS == "windows" {
		return resume
	}
	quote := func(argv []string) string {
		out := make([]string, len(argv))
		for i, a := range argv {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		return strings.Join(out, " ")
	}
	return []string{"sh", "-c", `started=$(date +%s); ` + quote(resume) + ` && exit 0; ` +
		`[ $(( $(date +%s) - started )) -lt 5 ] || exit 1; ` +
		`printf '\r\n[no conversation to resume: starting a new one]\r\n'; exec ` + quote(fresh)}
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
