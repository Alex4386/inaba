//go:build !linux && !darwin && !windows

package main

import (
	"errors"
	"runtime"
)

// Elsewhere ezmode knows no way to run the host in the background.

func ezCheck() error {
	return errors.New("ezmode knows systemd, launchd and Task Scheduler, none of which " + runtime.GOOS + " has; run inaba connect under whatever starts your programs there")
}

func ezInstalled() bool                          { return false }
func ezWhat() string                             { return "" }
func ezLog() string                              { return "" }
func ezStop() error                              { return nil }
func ezStart(string, []string) ([]string, error) { return nil, ezCheck() }
func ezRemove() error                            { return nil }
