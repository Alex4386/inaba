//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// reexec starts this program again in place of the running one, with the arguments and
// environment it started with, reading its binary afresh so an update takes effect. It
// keeps the process, so whatever supervises it (systemd, a terminal) sees no exit.
func reexec() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	return fmt.Errorf("restart: %w", syscall.Exec(exe, os.Args, startEnviron))
}
