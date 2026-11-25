// Package testdir gives tests folders for files that stay open to the end.
package testdir

import (
	"os"
	"runtime"
	"testing"
)

// New is t.TempDir for a folder with a database left open in it, or one a process may still
// be leaving: Windows does not remove files in use, which t.TempDir counts as a failure.
func New(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("", "inaba-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
