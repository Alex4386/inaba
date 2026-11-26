package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Alex4386/inaba/internal/control"
)

// ezmode hands the host to whatever starts programs on this system, so it runs in the
// background and comes back after a reboot. Each system's way is in ezmode_<os>.go:
//
//	ezCheck      why it cannot be done here, if it cannot
//	ezInstalled  whether ezmode has set it up here
//	ezStop       stop the host it runs, leaving it set up
//	ezStart      write the setup for exe with args, and start it; notes for the person
//	ezRemove     undo ezStart, the host already stopped
//	ezWhat       what the setup is, for messages
//	ezLog        where the host's log is
func ezmodeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ezmode",
		Aliases: []string{"ezmodo"},
		Short:   "Keep the host running in the background, the easy way",
		Long: `Sets the host up to run in the background and start again after a reboot, the
simplest way this system has:

  Linux    a systemd user service (and lingering, so it runs while you are logged out)
  macOS    a launchd agent, which runs while you are logged in
  Windows  a Task Scheduler task, started when you sign in

It runs this inaba binary, as you, with the settings and sign-in inaba connect
would use. Sign in first (inaba login).`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(ezInstallCommand(), ezUninstallCommand())
	return cmd
}

func ezInstallCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Run the host in the background, now and after every reboot",
		Long: `Sets the host up to run in the background, starts it, and has it start again
after a reboot. Run it again to set it up afresh, such as after moving inaba or
installing agents somewhere new: it takes this shell's PATH along, so the host
finds the same agents. Settings from INABA_ variables in this shell are not taken
along (they may be secrets); put them in the settings file.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ezCheck(); err != nil {
				return err
			}
			if os.Getenv("SUDO_USER") != "" {
				return errors.New("this runs through sudo, but ezmode sets the host up for the user running it: run it as yourself, without sudo")
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			if strings.Contains(exe, "go-build") {
				return fmt.Errorf("%s is a temporary build from go run; build or install inaba first", exe)
			}
			if cfg.Token == "" && cfg.Listen == "" {
				if _, err := os.Stat(credentialsPath()); err != nil {
					return errors.New("this machine is not signed in: run inaba login --server URL first")
				}
			}
			args, err := ezArgs(cmd)
			if err != nil {
				return err
			}
			for _, kv := range os.Environ() {
				if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "INABA_") {
					fmt.Fprintf(os.Stderr, "%s is set in this shell but not taken along; put it in %s if the host needs it.\n", k, userFile)
				}
			}

			installed, running := ezInstalled(), control.Dial(socketPath()) != nil
			if running && !installed {
				return errors.New("a host is already running here, not in the background (inaba connect in a terminal?); stop it, then run this again")
			}
			if installed {
				if running && !yes {
					if err := confirm(fmt.Sprintf("The host already runs from %s. Set it up again? It restarts, and turns that are running will end.", ezWhat()),
						"setting it up again restarts the host, ending running turns; pass --yes to go ahead without being asked",
						"not changed"); err != nil {
						return err
					}
				}
				if err := ezStop(); err != nil {
					return err
				}
				if !waitHost(false, 15*time.Second) {
					return errors.New("the host did not stop within 15 seconds")
				}
			}
			notes, err := ezStart(exe, args)
			if err != nil {
				return err
			}
			if !waitHost(true, 20*time.Second) {
				return fmt.Errorf("set up as %s, but the host has not started within 20 seconds; see %s", ezWhat(), ezLog())
			}
			fmt.Printf("The host runs in the background now, from %s.\n", ezWhat())
			for _, n := range notes {
				fmt.Println(n)
			}
			fmt.Printf("Its log: %s\nTo stop it for good: inaba ezmode uninstall\n", ezLog())
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before restarting a host that is already set up")
	return cmd
}

func ezUninstallCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the background host and stop it starting by itself",
		Long: `Stops the host that inaba ezmode install set up and removes that setup. The
settings, sign-in, projects and threads stay; inaba connect runs the host by hand,
and inaba ezmode install sets it up again.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := ezCheck(); err != nil {
				return err
			}
			if !ezInstalled() {
				fmt.Println("ezmode has not set the host up here, so there is nothing to remove.")
				return nil
			}
			what := ezWhat()
			if control.Dial(socketPath()) != nil && !yes {
				if err := confirm("Stop the host and remove "+what+"? Turns that are running will end.",
					"this stops the host, ending running turns; pass --yes to go ahead without being asked",
					"not removed"); err != nil {
					return err
				}
			}
			if err := ezStop(); err != nil {
				return err
			}
			if err := ezRemove(); err != nil {
				return err
			}
			fmt.Printf("Removed %s.\n", what)
			if !waitHost(false, 15*time.Second) {
				fmt.Println("A host still answers here; it may have been started some other way.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before stopping the host")
	return cmd
}

// ezArgs is inaba's arguments for the host: connect, with the settings flags given here,
// as absolute paths, since the service starts elsewhere.
func ezArgs(cmd *cobra.Command) ([]string, error) {
	args := []string{"connect"}
	for _, name := range []string{"config", "data", "socket"} {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			p, err := filepath.Abs(f.Value.String())
			if err != nil {
				return nil, err
			}
			args = append(args, "--"+name, p)
		}
	}
	return args, nil
}

// waitHost waits until the host answers (up) or does not, and says whether it did in time.
func waitHost(up bool, d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(200 * time.Millisecond) {
		if (control.Dial(socketPath()) != nil) == up {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// sysRun runs a system tool, its output in the error when it fails.
func sysRun(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		if msg := bytes.TrimSpace(out); len(msg) > 0 {
			return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
