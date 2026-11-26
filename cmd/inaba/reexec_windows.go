package main

import (
	"fmt"
	"os"
	"os/exec"
)

// reexec starts this program again with the arguments and environment it started with,
// reading its binary afresh so an update takes effect. Windows cannot replace a running
// process, so the new one is started beside it and this one then leaves.
func reexec() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, startEnviron
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	return cmd.Process.Release()
}
