//go:build !linux && !darwin && !windows

package ezmode

import (
	"errors"
	"runtime"
)

// Elsewhere ezmode knows no way to run it in the background.

func check() error {
	return errors.New("ezmode knows systemd, launchd and Task Scheduler, none of which " + runtime.GOOS + " has; run it under whatever starts your programs there")
}

func installed(Spec) bool                            { return false }
func what(Spec) string                               { return "" }
func logOf(Spec) string                              { return "" }
func stop(Spec) error                                { return nil }
func start(Spec, string, []string) ([]string, error) { return nil, check() }
func remove(Spec) error                              { return nil }
