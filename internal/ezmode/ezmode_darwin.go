package ezmode

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// On macOS it is a launchd agent: it starts when its user logs in and runs while they are.

func home() string { h, _ := os.UserHomeDir(); return h }

func plistPath(s Spec) string {
	return filepath.Join(home(), "Library", "LaunchAgents", s.label()+".plist")
}

func logPath(s Spec) string { return filepath.Join(home(), "Library", "Logs", s.Name+".log") }

// domain is the logged-in user's launchd domain, where agents run.
func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func check() error {
	if _, err := exec.LookPath("launchctl"); err != nil {
		return errors.New("ezmode runs it as a launchd agent, and launchctl is not here")
	}
	return nil
}

func installed(s Spec) bool {
	_, err := os.Stat(plistPath(s))
	return err == nil
}

func what(s Spec) string { return "the launchd agent " + plistPath(s) }

func logOf(s Spec) string { return logPath(s) }

func stop(s Spec) error {
	// Booting out what is not loaded is not a failure here.
	_ = run("launchctl", "bootout", domain()+"/"+s.label())
	return nil
}

// plistText is the agent running exe with args, with this PATH, in home, logging to log.
func plistText(s Spec, exe string, args []string, path, home, log string) string {
	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	var program strings.Builder
	for _, a := range append([]string{exe}, args...) {
		program.WriteString("\n    <string>" + esc(a) + "</string>")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by %[1]s ezmode install, which writes it afresh; %[1]s ezmode uninstall removes it. -->
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%[2]s</string>
  <key>ProgramArguments</key>
  <array>%[3]s
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <!-- The PATH of the shell that installed it, so it finds the same programs. -->
    <key>PATH</key>
    <string>%[4]s</string>
  </dict>
  <key>WorkingDirectory</key>
  <string>%[5]s</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ThrottleInterval</key>
  <integer>5</integer>
  <key>ProcessType</key>
  <string>Standard</string>
  <key>StandardOutPath</key>
  <string>%[6]s</string>
  <key>StandardErrorPath</key>
  <string>%[6]s</string>
</dict>
</plist>
`, esc(s.Name), esc(s.label()), program.String(), esc(path), esc(home), esc(log))
}

func start(s Spec, exe string, args []string) ([]string, error) {
	p := plistPath(s)
	for _, dir := range []string{filepath.Dir(p), filepath.Dir(logPath(s))} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(p, []byte(plistText(s, exe, args, os.Getenv("PATH"), home(), logPath(s))), 0o644); err != nil {
		return nil, err
	}
	// An agent turned off by launchctl disable stays off through a bootstrap.
	_ = run("launchctl", "enable", domain()+"/"+s.label())
	// launchd may still be letting go of the one just booted out.
	var err error
	for range 10 {
		if err = run("launchctl", "bootstrap", domain(), p); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("%w (launchd agents need you logged in to this Mac; over SSH alone there is no session to run in)", err)
	}
	return []string{"It starts whenever you log in to this Mac, and runs while you are logged in."}, nil
}

func remove(s Spec) error {
	if err := os.Remove(plistPath(s)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
