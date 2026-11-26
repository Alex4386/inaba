package main

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

// On macOS the host is a launchd agent: it starts when its user logs in and runs while
// they are.

const ezLabel = "io.github.alex4386.inaba"

func ezHome() string { h, _ := os.UserHomeDir(); return h }

func ezPlistPath() string {
	return filepath.Join(ezHome(), "Library", "LaunchAgents", ezLabel+".plist")
}

func ezLogPath() string { return filepath.Join(ezHome(), "Library", "Logs", "inaba.log") }

// ezDomain is the logged-in user's launchd domain, where agents run.
func ezDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func ezCheck() error {
	if _, err := exec.LookPath("launchctl"); err != nil {
		return errors.New("ezmode runs the host as a launchd agent, and launchctl is not here")
	}
	return nil
}

func ezInstalled() bool {
	_, err := os.Stat(ezPlistPath())
	return err == nil
}

func ezWhat() string { return "the launchd agent " + ezPlistPath() }

func ezLog() string { return ezLogPath() }

func ezStop() error {
	// Booting out what is not loaded is not a failure here.
	_ = sysRun("launchctl", "bootout", ezDomain()+"/"+ezLabel)
	return nil
}

// ezPlistText is the agent running exe with args, with this PATH, in home, logging to log.
func ezPlistText(exe string, args []string, path, home, log string) string {
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
<!-- Written by inaba ezmode install, which writes it afresh; inaba ezmode uninstall removes it. -->
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>%s
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <!-- The PATH of the shell that installed it, so the host finds the same agents. -->
    <key>PATH</key>
    <string>%s</string>
  </dict>
  <key>WorkingDirectory</key>
  <string>%s</string>
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
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, ezLabel, program.String(), esc(path), esc(home), esc(log), esc(log))
}

func ezStart(exe string, args []string) ([]string, error) {
	plist := ezPlistText(exe, args, os.Getenv("PATH"), ezHome(), ezLogPath())
	p := ezPlistPath()
	for _, dir := range []string{filepath.Dir(p), filepath.Dir(ezLogPath())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		return nil, err
	}
	// An agent turned off by launchctl disable stays off through a bootstrap.
	_ = sysRun("launchctl", "enable", ezDomain()+"/"+ezLabel)
	// launchd may still be letting go of the one just booted out.
	var err error
	for range 10 {
		if err = sysRun("launchctl", "bootstrap", ezDomain(), p); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("%w (launchd agents need you logged in to this Mac; over SSH alone there is no session to run in)", err)
	}
	return []string{"It starts whenever you log in to this Mac, and runs while you are logged in."}, nil
}

func ezRemove() error {
	if err := os.Remove(ezPlistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
