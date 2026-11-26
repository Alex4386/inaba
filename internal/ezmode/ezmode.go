// Package ezmode hands an inaba program (the host, or the server) to whatever starts
// programs on this system, so it runs in the background and comes back after a reboot:
// a systemd user service on Linux, a launchd agent on macOS, a Task Scheduler task on
// Windows. Each system's way is in ezmode_<os>.go:
//
//	check      why it cannot be done here, if it cannot
//	installed  whether ezmode has set it up here
//	stop       stop what it runs, leaving it set up
//	start      write the setup for exe with args, and start it; notes for the person
//	remove     undo start, the program already stopped
//	what       what the setup is, for messages
//	logOf      where the program's log is
package ezmode

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
	"github.com/Alex4386/inaba/internal/prompt"
)

// Spec is one program to run in the background, read once its settings are loaded.
type Spec struct {
	// Name names the service, task and log: inaba or inaba-server.
	Name string
	// Noun is what it is to a person: "the host", "the server".
	Noun string
	// Sub is the command that runs it, such as connect.
	Sub string
	// Socket is the control socket it answers on while it runs.
	Socket string
	// Dir is the folder of its database, where its log goes on Windows.
	Dir string
	// SettingsFile is where INABA_ settings belong, and EnvFile where systemd reads them.
	SettingsFile, EnvFile string
	// Ready says why it cannot run yet, such as not being signed in.
	Ready func() error
	// Notes are said after it starts with args, such as where to look next.
	Notes func(args []string) []string
}

// label is its launchd label.
func (s Spec) label() string { return "io.github.alex4386." + s.Name }

// Command is the ezmode command, with its alias ezmodo, for the program spec describes.
func Command(spec func() Spec) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ezmode",
		Aliases: []string{"ezmodo"},
		Short:   "Keep it running in the background, the easy way",
		Long: `Sets it up to run in the background and start again after a reboot, the simplest
way this system has:

  Linux    a systemd user service (and lingering, so it runs while you are logged out)
  macOS    a launchd agent, which runs while you are logged in
  Windows  a Task Scheduler task, started when you sign in

It runs this binary, as you, with the settings it would use in a terminal.
See docs/EZMODE.md.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(installCommand(spec), uninstallCommand(spec))
	return cmd
}

func installCommand(spec func() Spec) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "install [-- flags for it]",
		Short: "Run it in the background, now and after every reboot",
		Long: `Sets it up to run in the background, starts it, and has it start again after a
reboot. Run it again to set it up afresh, such as after moving the binary or
installing agents somewhere new: it takes this shell's PATH along. Flags after --
are passed on, such as -- --public-url https://inaba.example.com. Settings from
INABA_ variables in this shell are not taken along (they may be secrets); put them
in the settings file.`,
		RunE: func(cmd *cobra.Command, extra []string) error {
			s := spec()
			if err := check(); err != nil {
				return err
			}
			if cmd.ArgsLenAtDash() != 0 && len(extra) > 0 {
				return fmt.Errorf("flags for %[1]s go after --, such as %[1]s ezmode install -- --flag value", s.Name)
			}
			if os.Getenv("SUDO_USER") != "" {
				return errors.New("this runs through sudo, but ezmode sets it up for the user running it: run it as yourself, without sudo")
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			if strings.Contains(exe, "go-build") {
				return fmt.Errorf("%s is a temporary build from go run; build or install %s first", exe, s.Name)
			}
			if s.Ready != nil {
				if err := s.Ready(); err != nil {
					return err
				}
			}
			args, err := argsFor(cmd, s.Sub)
			if err != nil {
				return err
			}
			args = append(args, extra...)
			for _, kv := range os.Environ() {
				if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "INABA_") {
					fmt.Fprintf(os.Stderr, "%s is set in this shell but not taken along; put it in %s if it is needed.\n", k, s.SettingsFile)
				}
			}

			isInstalled, running := installed(s), control.Dial(s.Socket) != nil
			if running && !isInstalled {
				return fmt.Errorf("%s is already running here, not in the background (%s %s in a terminal?); stop it, then run this again", s.Noun, s.Name, s.Sub)
			}
			if isInstalled {
				if running && !yes {
					if err := prompt.Confirm(fmt.Sprintf("%s already runs from %s. Set it up again? It restarts, and anything it is doing ends.", upper(s.Noun), what(s)),
						"setting it up again restarts it; pass --yes to go ahead without being asked",
						"not changed"); err != nil {
						return err
					}
				}
				if err := stop(s); err != nil {
					return err
				}
				if !wait(s, false, 15*time.Second) {
					return fmt.Errorf("%s did not stop within 15 seconds", s.Noun)
				}
			}
			notes, err := start(s, exe, args)
			if err != nil {
				return err
			}
			if !wait(s, true, 20*time.Second) {
				return fmt.Errorf("set up as %s, but %s has not started within 20 seconds; see %s", what(s), s.Noun, logOf(s))
			}
			fmt.Printf("%s runs in the background now, from %s.\n", upper(s.Noun), what(s))
			if s.Notes != nil {
				notes = append(notes, s.Notes(args)...)
			}
			for _, n := range notes {
				fmt.Println(n)
			}
			fmt.Printf("Its log: %s\nTo stop it for good: %s ezmode uninstall\n", logOf(s), s.Name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before restarting what is already set up")
	return cmd
}

func uninstallCommand(spec func() Spec) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop it, and stop it starting by itself",
		Long: `Stops what ezmode install set up and removes that setup. Settings and data stay;
run it in a terminal, or ezmode install again.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			s := spec()
			if err := check(); err != nil {
				return err
			}
			if !installed(s) {
				fmt.Printf("ezmode has not set %s up here, so there is nothing to remove.\n", s.Noun)
				return nil
			}
			w := what(s)
			if control.Dial(s.Socket) != nil && !yes {
				if err := prompt.Confirm(fmt.Sprintf("Stop %s and remove %s? Anything it is doing ends.", s.Noun, w),
					"this stops it; pass --yes to go ahead without being asked",
					"not removed"); err != nil {
					return err
				}
			}
			if err := stop(s); err != nil {
				return err
			}
			if err := remove(s); err != nil {
				return err
			}
			fmt.Printf("Removed %s.\n", w)
			if !wait(s, false, 15*time.Second) {
				fmt.Printf("%s still answers here; it may have been started some other way.\n", upper(s.Noun))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before stopping it")
	return cmd
}

// argsFor is the program's arguments: sub, with the settings flags given here, as
// absolute paths, since the service starts elsewhere.
func argsFor(cmd *cobra.Command, sub string) ([]string, error) {
	args := []string{sub}
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

// wait waits until the program answers (up) or does not, and says whether it did in time.
func wait(s Spec, up bool, d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(200 * time.Millisecond) {
		if (control.Dial(s.Socket) != nil) == up {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// run runs a system tool, its output in the error when it fails.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		if msg := bytes.TrimSpace(out); len(msg) > 0 {
			return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
