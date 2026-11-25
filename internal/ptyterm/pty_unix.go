//go:build unix

package ptyterm

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

type unixPTY struct {
	*os.File
	cmd *exec.Cmd
}

func startPTY(argv []string, dir string, env []string, cols, rows uint16) (pseudo, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return &unixPTY{f, cmd}, nil
}

func (p *unixPTY) Resize(cols, rows uint16) error {
	return pty.Setsize(p.File, &pty.Winsize{Cols: cols, Rows: rows})
}

func (p *unixPTY) End() {
	// Closing the PTY hangs up the process; kill covers one that ignores it.
	_ = p.File.Close()
	_ = p.cmd.Process.Kill()
}

func (p *unixPTY) Wait() { _ = p.cmd.Wait() }
