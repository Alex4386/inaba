package main

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Alex4386/inaba/internal/control"
	"github.com/Alex4386/inaba/internal/selfupdate"
	"github.com/Alex4386/inaba/internal/version"
)

func updateCommand() *cobra.Command {
	var channel string
	var check, force, yes bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Replace this inaba with the newest build of a channel",
		Long: `Replaces this inaba with the newest build of a channel from GitHub:

  release      the newest release
  pre-release  the newest release or prerelease
  nightly      the last nightly build of main; these are GitHub Actions artifacts,
               which only signed-in users can download, so it needs the gh CLI
               signed in (gh auth login)

Without --channel, it follows the channel this inaba came from. It asks before
updating, and for a nightly also before using gh. When a host is running here,
it then offers to restart it (inaba restart), which ends running turns. -y says
yes to all of it, for scripts and timers.`,
		Args: cobra.NoArgs,
		// The settings only say where the running host is, to restart it; broken ones
		// must not stop an update, which may be what fixes them.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := loadSettings(cmd); err != nil {
				slog.Warn("settings not loaded, so the running host is not restarted", "error", err.Error())
				socketFile = ""
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			now := version.String()
			if channel == "" {
				channel = selfupdate.ChannelOf(now)
			}
			if !slices.Contains(selfupdate.Channels, channel) {
				return fmt.Errorf("no channel %q: it is one of %s", channel, strings.Join(selfupdate.Channels, ", "))
			}
			if channel == selfupdate.Nightly && !yes {
				// The sign-in is the user's, so it is theirs to lend.
				if err := confirm("Nightly builds are GitHub Actions artifacts, and downloading them needs your GitHub credentials.\nUse the gh CLI for that?",
					"downloading a nightly build uses your GitHub sign-in through the gh CLI; pass --yes to allow it",
					"not using gh, so nothing was downloaded"); err != nil {
					return err
				}
			}
			b, err := selfupdate.Latest(cmd.Context(), channel)
			if err != nil {
				return err
			}
			if b.Current(now) && !force {
				fmt.Printf("inaba %s is the newest %s build already.\n", now, channel)
				return nil
			}
			if check {
				fmt.Printf("inaba %s can be updated to %s (%s).\n", now, b.Version, channel)
				return nil
			}
			if !yes {
				if err := confirm(fmt.Sprintf("Update inaba %s to the newest %s build, %s?", now, channel, b.Version),
					"this replaces inaba; pass --yes to go ahead without being asked",
					"not updating, so nothing was changed"); err != nil {
					return err
				}
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			fmt.Printf("Fetching %s (%s)…\n", b.Archive, b.Version)
			said, err := selfupdate.Install(cmd.Context(), b, "inaba", exe)
			if err != nil {
				return err
			}
			fmt.Printf("Updated %s: %s → %s\n", exe, now, strings.TrimPrefix(said, "inaba version "))
			// The host running here keeps the old one until it restarts.
			if socketPath() == "" || control.Dial(socketPath()) == nil {
				return nil
			}
			if !yes {
				if err := confirm("The host running here still runs the old one. Restart it now? Turns that are running will end.",
					"the host running here still runs the old one; inaba restart switches to the new one",
					"not restarting; inaba restart switches to the new one"); err != nil {
					fmt.Println(control.Plain(err))
					return nil
				}
			}
			return restartHost(cmd.Context())
		},
	}
	f := cmd.Flags()
	f.StringVar(&channel, "channel", "", "release, pre-release or nightly (default: the channel this inaba came from)")
	f.BoolVar(&check, "check", false, "only say whether there is a newer build")
	f.BoolVar(&force, "force", false, "install the build even when it is the one running")
	f.BoolVarP(&yes, "yes", "y", false, "do not ask first: before updating, before using the gh CLI's GitHub sign-in for a nightly, or before restarting the running host")
	return cmd
}

// stdin is read through one buffer, so an answer typed ahead is not lost between questions.
var stdin = bufio.NewReader(os.Stdin)

// confirm asks a yes-or-no question whose answer is yes unless told otherwise. Without
// someone to answer it, it fails with noTerminal: --yes is how a script says yes.
func confirm(question, noTerminal, declined string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New(noTerminal)
	}
	fmt.Print(question + " (Y/n) ")
	answer, err := stdin.ReadString('\n')
	if err != nil && answer == "" {
		fmt.Println()
		return errors.New("no answer, so nothing was changed")
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "", "y", "yes":
		return nil
	}
	return errors.New(declined)
}
