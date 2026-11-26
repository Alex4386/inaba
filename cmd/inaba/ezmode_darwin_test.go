package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The agent is a plist launchd reads back as written, odd characters and all.
func TestEzPlist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.plist")
	exe := "/Users/a & b/<bin>/inaba"
	text := ezPlistText(exe, []string{"connect", "--config", "/x/\"q\".json"}, "/usr/bin:/bin", "/Users/a & b", "/Users/a & b/Library/Logs/inaba.log")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", p).CombinedOutput(); err != nil {
		t.Fatalf("%s\n%s", out, text)
	}
	for key, want := range map[string]string{
		"ProgramArguments.0":        exe,
		"ProgramArguments.3":        `/x/"q".json`,
		"EnvironmentVariables.PATH": "/usr/bin:/bin",
		"Label":                     ezLabel,
	} {
		out, err := exec.Command("plutil", "-extract", key, "raw", "-o", "-", p).Output()
		if got := strings.TrimSpace(string(out)); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", key, got, err, want)
		}
	}
}
