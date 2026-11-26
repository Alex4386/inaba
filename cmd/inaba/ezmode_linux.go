package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// On Linux the host is a systemd user service, kept running while its user is logged
// out by lingering.

const ezUnit = "inaba.service"

func ezUnitPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", ezUnit)
}

func ezCheck() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("ezmode runs the host as a systemd user service, and there is no systemd here; run inaba connect under whatever starts your programs instead")
	}
	if err := sysRun("systemctl", "--user", "show-environment"); err != nil {
		return fmt.Errorf("ezmode runs the host as a systemd user service, but there is no systemd user session here (%v); log in normally (not through su), or run inaba connect under whatever starts your programs", err)
	}
	return nil
}

func ezInstalled() bool {
	_, err := os.Stat(ezUnitPath())
	return err == nil
}

func ezWhat() string { return "the systemd user service " + ezUnitPath() }

func ezLog() string { return "journalctl --user -u inaba" }

func ezStop() error {
	err := sysRun("systemctl", "--user", "stop", ezUnit)
	// A unit systemd has not loaded (written but never reloaded) is not running either.
	if err != nil && exec.Command("systemctl", "--user", "is-active", "--quiet", ezUnit).Run() != nil {
		return nil
	}
	return err
}

// ezUnitText is the unit running exe with args, with this PATH, reading env for INABA_ settings.
func ezUnitText(exe string, args []string, path, env string) string {
	// ExecStart expands $ and %, Environment only %.
	cmdline := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	plain := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")
	line := []string{`"` + cmdline.Replace(exe) + `"`}
	for _, a := range args {
		line = append(line, `"`+cmdline.Replace(a)+`"`)
	}
	return fmt.Sprintf(`# Written by inaba ezmode install, which writes it afresh; inaba ezmode uninstall removes it.
# It runs the host as you, in your home, with your git and agent credentials, so it is not
# sandboxed away from them. INABA_ settings can go in %[1]s.

[Unit]
Description=inaba host

[Service]
ExecStart=%[2]s
# The PATH of the shell that installed it, so the host finds the same agents.
Environment="PATH=%[3]s"
EnvironmentFile=-%[1]s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, env, strings.Join(line, " "), plain.Replace(path))
}

func ezStart(exe string, args []string) ([]string, error) {
	unit := ezUnitText(exe, args, os.Getenv("PATH"), filepath.Join(filepath.Dir(userFile), "host.env"))
	p := ezUnitPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(unit), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", ezUnit}, {"restart", ezUnit}} {
		if err := sysRun("systemctl", append([]string{"--user"}, args...)...); err != nil {
			return nil, err
		}
	}
	// Without lingering, the user's services stop when they log out and wait for a login after a reboot.
	u, err := user.Current()
	if err != nil {
		return nil, err
	}
	out, _ := exec.Command("loginctl", "show-user", u.Username, "--property=Linger", "--value").Output()
	if strings.TrimSpace(string(out)) == "yes" {
		return nil, nil
	}
	if err := sysRun("loginctl", "enable-linger"); err != nil {
		return []string{fmt.Sprintf("It runs while you are logged in. To keep it running when you are not, and start it at boot: sudo loginctl enable-linger %s (%v)", u.Username, err)}, nil
	}
	return []string{"Lingering is on for " + u.Username + ", so it keeps running when you log out and starts at boot."}, nil
}

func ezRemove() error {
	// Disabling an already-disabled unit is not a failure; a unit systemd never loaded is.
	_ = sysRun("systemctl", "--user", "disable", ezUnit)
	if err := os.Remove(ezUnitPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = sysRun("systemctl", "--user", "reset-failed", ezUnit)
	return sysRun("systemctl", "--user", "daemon-reload")
}
