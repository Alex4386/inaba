package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/control"
)

// startEnviron is the environment as the process got it, before settings were loaded
// and the secret INABA_ variables taken out of it: a restart has to be given them again.
var startEnviron = os.Environ()

// restartHost asks the running host to restart and waits until it is back.
func restartHost(ctx context.Context) error {
	path := socketPath()
	transport := control.Dial(path)
	if transport == nil {
		return errors.New("the host is not running here; start it with inaba connect")
	}
	// A connection the host holds open goes when its process does, which is how the
	// restart is seen to have happened: the socket itself may be back before a poll looks.
	watch, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return err
	}
	defer watch.Close()
	system := inabav1connect.NewSystemServiceClient(connect.NewClient(transport))
	if _, err := system.RestartHost(ctx, &inabav1.RestartHostRequest{}); err != nil {
		return control.Plain(err)
	}
	fmt.Print("Restarting…")
	_ = watch.SetReadDeadline(time.Now().Add(time.Minute))
	if _, err := io.Copy(io.Discard, watch); err != nil {
		fmt.Println()
		return errors.New("the host did not stop within a minute")
	}
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if control.Dial(path) != nil {
			fmt.Println(" back.")
			return nil
		}
	}
	fmt.Println()
	return errors.New("the host stopped but has not come back within a minute; see its log")
}

func restartCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the host running here",
		Long: `Restarts the host running on this machine: it stops its agents and terminals,
then starts again, reading its settings and its binary afresh, so it picks up
config edits and a build from inaba update. A turn that is running ends.

The host's owner and administrators can do the same from the web UI, on the
host's About page.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if control.Dial(socketPath()) == nil {
				return errors.New("the host is not running here; start it with inaba connect")
			}
			if !yes {
				if err := confirm("Restart the host? Turns that are running will end.",
					"restarting ends running turns; pass --yes to go ahead without being asked",
					"not restarting"); err != nil {
					return err
				}
			}
			return restartHost(cmd.Context())
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask first")
	return cmd
}
