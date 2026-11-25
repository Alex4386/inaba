package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{
		"":         home,
		"~":        home,
		"~/git":    filepath.Join(home, "git"),
		"~/git/":   filepath.Join(home, "git"),
		"/tmp":     "/tmp",
		"~someone": "~someone",
	} {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
