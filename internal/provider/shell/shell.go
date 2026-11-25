// Package shell offers a plain login shell as a session, for long-running work
// that is not an agent: builds, servers, anything run from a terminal.
package shell

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"

	"github.com/Alex4386/inaba/pkg/provider"
)

type Provider struct{}

func (Provider) ID() string    { return "shell" }
func (Provider) TerminalOnly() {}

func (Provider) TerminalCommand(string) []string {
	if runtime.GOOS == "windows" {
		return windowsShell()
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	return []string{sh, "-l"}
}

// windowsShell is PowerShell, the newer one when it is installed, or else cmd. $SHELL says
// nothing here: Git Bash sets it to a path only it can read.
func windowsShell() []string {
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return []string{path, "-NoLogo"}
		}
	}
	if path := os.Getenv("ComSpec"); path != "" {
		return []string{path}
	}
	return []string{"cmd.exe"}
}

func (Provider) Open(context.Context, provider.Config, provider.Emit) (provider.Session, error) {
	return nil, errors.New("shell sessions have no structured mode")
}
