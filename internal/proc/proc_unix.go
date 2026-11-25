//go:build unix

package proc

import (
	"io"
	"os/exec"
	"syscall"
)

// Group is a started program and the processes it started.
type Group struct{ pid int }

// Start starts cmd in a process group of its own.
func Start(cmd *exec.Cmd) (*Group, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Group{pid: cmd.Process.Pid}, nil
}

// Ask asks the group to end.
func (g *Group) Ask(io.Closer) { _ = syscall.Kill(-g.pid, syscall.SIGTERM) }

// Kill ends the group at once.
func (g *Group) Kill() { _ = syscall.Kill(-g.pid, syscall.SIGKILL) }

// Release lets go of the group once its head has exited.
func (g *Group) Release() {}

// Alive reports whether a process with that id is running.
func Alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
