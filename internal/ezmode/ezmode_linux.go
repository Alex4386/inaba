package ezmode

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// On Linux it is a systemd user service, kept running while its user is logged out by
// lingering.

func unit(s Spec) string { return s.Name + ".service" }

func unitPath(s Spec) string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", unit(s))
}

func check() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("ezmode runs it as a systemd user service, and there is no systemd here; run it under whatever starts your programs instead")
	}
	if err := run("systemctl", "--user", "show-environment"); err != nil {
		return fmt.Errorf("ezmode runs it as a systemd user service, but there is no systemd user session here (%v); log in normally (not through su), or run it under whatever starts your programs", err)
	}
	return nil
}

func installed(s Spec) bool {
	_, err := os.Stat(unitPath(s))
	return err == nil
}

func what(s Spec) string { return "the systemd user service " + unitPath(s) }

func logOf(s Spec) string { return "journalctl --user -u " + s.Name }

func stop(s Spec) error {
	err := run("systemctl", "--user", "stop", unit(s))
	// A unit systemd has not loaded (written but never reloaded) is not running either.
	if err != nil && exec.Command("systemctl", "--user", "is-active", "--quiet", unit(s)).Run() != nil {
		return nil
	}
	return err
}

// unitText is the unit running exe with args, with this PATH, reading env for INABA_ settings.
func unitText(s Spec, exe string, args []string, path, env string) string {
	// ExecStart expands $ and %, Environment only %.
	cmdline := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	plain := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")
	line := []string{`"` + cmdline.Replace(exe) + `"`}
	for _, a := range args {
		line = append(line, `"`+cmdline.Replace(a)+`"`)
	}
	return fmt.Sprintf(`# Written by %[1]s ezmode install, which writes it afresh; %[1]s ezmode uninstall removes it.
# It runs as you, in your home, so it is not sandboxed away from your files.
# INABA_ settings can go in %[2]s.

[Unit]
Description=%[1]s

[Service]
ExecStart=%[3]s
# The PATH of the shell that installed it, so it finds the same programs.
Environment="PATH=%[4]s"
EnvironmentFile=-%[2]s
Restart=on-failure
RestartSec=5
# Stopping signals only the host, which ends its agents itself and so knows their turns were cut short.
KillMode=mixed

[Install]
WantedBy=default.target
`, s.Name, env, strings.Join(line, " "), plain.Replace(path))
}

func start(s Spec, exe string, args []string) ([]string, error) {
	p := unitPath(s)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(unitText(s, exe, args, os.Getenv("PATH"), s.EnvFile)), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unit(s)}, {"restart", unit(s)}} {
		if err := run("systemctl", append([]string{"--user"}, args...)...); err != nil {
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
	if err := run("loginctl", "enable-linger"); err != nil {
		return []string{fmt.Sprintf("It runs while you are logged in. To keep it running when you are not, and start it at boot: sudo loginctl enable-linger %s (%v)", u.Username, err)}, nil
	}
	return []string{"Lingering is on for " + u.Username + ", so it keeps running when you log out and starts at boot."}, nil
}

func remove(s Spec) error {
	// Disabling an already-disabled unit is not a failure; a unit systemd never loaded is.
	_ = run("systemctl", "--user", "disable", unit(s))
	if err := os.Remove(unitPath(s)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = run("systemctl", "--user", "reset-failed", unit(s))
	return run("systemctl", "--user", "daemon-reload")
}
