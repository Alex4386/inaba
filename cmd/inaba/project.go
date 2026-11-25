package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectinprocess"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/control"
	"github.com/Alex4386/inaba/internal/host"
)

// hostClient reaches this machine's host API.
type hostClient struct {
	projects inabav1connect.ProjectServiceClient
	threads  inabav1connect.ThreadServiceClient
	upkeep   inabav1connect.HousekeepingServiceClient
	autos    inabav1connect.AutomationServiceClient
	modules  inabav1connect.ModuleServiceClient
	mcp      inabav1connect.McpServiceClient
	// live reports whether a running host answered, rather than the database alone.
	live bool
}

// openHost talks to the running host over its control socket. With no host running,
// the same API is served from the database right here in this process; projects
// work that way, and there are no threads to speak of.
func openHost() (*hostClient, error) {
	transport := control.Dial(socketPath())
	live := transport != nil
	if !live {
		h := host.New()
		if err := h.LoadProjects(dataPath); err != nil {
			return nil, err
		}
		transport = connectinprocess.New(h.ControlServer())
	}
	client := connect.NewClient(transport)
	return &hostClient{
		projects: inabav1connect.NewProjectServiceClient(client),
		threads:  inabav1connect.NewThreadServiceClient(client),
		upkeep:   inabav1connect.NewHousekeepingServiceClient(client),
		autos:    inabav1connect.NewAutomationServiceClient(client),
		modules:  inabav1connect.NewModuleServiceClient(client),
		mcp:      inabav1connect.NewMcpServiceClient(client),
		live:     live,
	}, nil
}

// run wraps a command body with the host client and plain error messages.
func run(do func(ctx context.Context, c *hostClient, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		c, err := openHost()
		if err != nil {
			return err
		}
		return control.Plain(do(cmd.Context(), c, args))
	}
}

func table(header string, rows func(add func(cells ...string))) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, header)
	rows(func(cells ...string) { fmt.Fprintln(w, strings.Join(cells, "\t")) })
	return w.Flush()
}

func projectCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Manage this host's projects"}
	var name string
	add := &cobra.Command{
		Use:   "add FOLDER",
		Short: "Add a folder as a project",
		Args:  cobra.ExactArgs(1),
		RunE: run(func(ctx context.Context, c *hostClient, args []string) error {
			// The host needs an absolute path; the shell gives a relative one.
			folder, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			p, err := c.projects.CreateProject(ctx, &inabav1.CreateProjectRequest{Name: name, Cwd: folder})
			if err != nil {
				return err
			}
			fmt.Printf("Added project %s (%s) at %s\n", p.Name, p.Id, p.Cwd)
			return nil
		}),
	}
	add.Flags().StringVar(&name, "name", "", "project name; defaults to the folder's name")
	cmd.AddCommand(add,
		&cobra.Command{
			Use:   "list",
			Short: "List the projects",
			Args:  cobra.NoArgs,
			RunE: run(func(ctx context.Context, c *hostClient, _ []string) error {
				res, err := c.projects.ListProjects(ctx, &inabav1.ListProjectsRequest{})
				if err != nil {
					return err
				}
				return table("ID\tNAME\tFOLDER", func(add func(...string)) {
					for _, p := range res.Projects {
						add(p.Id, p.Name, p.Cwd)
					}
				})
			}),
		},
		&cobra.Command{
			Use:   "rm ID",
			Short: "Remove a project; its folder and running threads are left alone",
			Args:  cobra.ExactArgs(1),
			RunE: run(func(ctx context.Context, c *hostClient, args []string) error {
				if _, err := c.projects.DeleteProject(ctx, &inabav1.DeleteProjectRequest{ProjectId: args[0]}); err != nil {
					return err
				}
				fmt.Printf("Removed project %s\n", args[0])
				return nil
			}),
		},
	)
	return cmd
}

// Threads live in the running host's memory, so these need one to be running.
var errNotRunning = errors.New("inaba is not running on this machine; threads exist only while `inaba connect` runs")

func sessionCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "thread", Short: "See and end the threads running on this host"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List the threads",
			Args:  cobra.NoArgs,
			RunE: run(func(ctx context.Context, c *hostClient, _ []string) error {
				if !c.live {
					return errNotRunning
				}
				res, err := c.threads.ListThreads(ctx, &inabav1.ListThreadsRequest{})
				if err != nil {
					return err
				}
				return table("ID\tRUNS\tSTATE\tTITLE\tFOLDER", func(add func(...string)) {
					for _, t := range res.Threads {
						state := strings.ToLower(strings.TrimPrefix(t.Status.String(), "THREAD_STATUS_"))
						if t.Mode == inabav1.ThreadMode_THREAD_MODE_TERMINAL {
							state = "terminal"
						} else if t.Activity != "" {
							state += ": " + t.Activity
						}
						add(t.Id, t.Provider, state, t.Title, t.Cwd)
					}
				})
			}),
		},
		&cobra.Command{
			Use:   "rm ID",
			Short: "End a thread and stop its process",
			Args:  cobra.ExactArgs(1),
			RunE: run(func(ctx context.Context, c *hostClient, args []string) error {
				if !c.live {
					return errNotRunning
				}
				if _, err := c.threads.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: args[0]}); err != nil {
					return err
				}
				fmt.Printf("Ended thread %s\n", args[0])
				return nil
			}),
		},
	)
	return cmd
}

func housekeepingCommand() *cobra.Command {
	var req inabav1.CleanUpRequest
	cmd := &cobra.Command{
		Use:   "housekeeping",
		Short: "Show what inaba keeps on this host's disk, and clear what is left over",
		Long: "Without flags, shows the database size, the thread worktrees and the file snapshots.\n" +
			"Leftovers are those of threads that no longer exist. Needs the host running, which knows its threads.",
		Args: cobra.NoArgs,
		RunE: run(func(ctx context.Context, c *hostClient, _ []string) error {
			if !c.live {
				return errors.New("the host is not running; start it with inaba connect, so housekeeping knows which threads exist")
			}
			var hk *inabav1.Housekeeping
			if req.LeftoverWorktrees || req.LeftoverSnapshots || req.CompactDatabase {
				res, err := c.upkeep.CleanUp(ctx, &req)
				if err != nil {
					return err
				}
				for _, why := range res.Kept {
					fmt.Println("Kept " + why)
				}
				hk = res.Housekeeping
			} else {
				var err error
				if hk, err = c.upkeep.GetHousekeeping(ctx, &inabav1.GetHousekeepingRequest{}); err != nil {
					return err
				}
			}
			fmt.Printf("Database: %s\n\n", bytesText(hk.DatabaseBytes))
			_ = table("WORKTREE\tBRANCH\tSESSION\tCHANGES", func(add func(...string)) {
				for _, w := range hk.Worktrees {
					thread, changes := w.ThreadTitle, ""
					if w.ThreadId == "" {
						thread = "(leftover)"
					}
					if w.Dirty {
						changes = "uncommitted"
					}
					add(w.Path, w.Branch, thread, changes)
				}
			})
			fmt.Println()
			return table("REPOSITORY\tSNAPSHOTS\tLEFTOVER", func(add func(...string)) {
				for _, s := range hk.Snapshots {
					add(s.Repository, fmt.Sprint(s.Refs), fmt.Sprint(s.LeftoverRefs))
				}
			})
		}),
	}
	cmd.Flags().BoolVar(&req.LeftoverWorktrees, "worktrees", false, "remove worktrees no thread uses (ones with uncommitted changes stay)")
	cmd.Flags().BoolVar(&req.DiscardChanges, "discard-changes", false, "with --worktrees, remove them even with uncommitted changes")
	cmd.Flags().BoolVar(&req.LeftoverSnapshots, "snapshots", false, "delete the file snapshots of threads that no longer exist")
	cmd.Flags().BoolVar(&req.CompactDatabase, "compact", false, "give the space deleted data leaves in the database back to the disk")
	return cmd
}

// bytesText is a size the way people read one.
func bytesText(n uint64) string {
	units := []string{"B", "KiB", "MiB", "GiB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f, i = f/1024, i+1
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

func automationCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "automation", Short: "Manage work that starts on a schedule or from a webhook"}
	live := func(c *hostClient) error {
		if !c.live {
			return errors.New("the host is not running; start it with inaba connect, which runs the automations")
		}
		return nil
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List the automations and how each last ran",
		Args:  cobra.NoArgs,
		RunE: run(func(ctx context.Context, c *hostClient, _ []string) error {
			if err := live(c); err != nil {
				return err
			}
			res, err := c.autos.ListAutomations(ctx, &inabav1.ListAutomationsRequest{})
			if err != nil {
				return err
			}
			return table("ID\tNAME\tON\tNEXT RUN\tLAST RUN", func(add func(...string)) {
				for _, a := range res.Automations {
					next, last := "-", "-"
					if a.NextRun != nil {
						next = a.NextRun.AsTime().Local().Format("2006-01-02 15:04")
					}
					if len(a.Runs) > 0 {
						r := a.Runs[0]
						last = r.Time.AsTime().Local().Format("2006-01-02 15:04") + " " + r.Trigger
						if r.Error != "" {
							last += " (failed: " + r.Error + ")"
						}
					}
					add(a.Id, a.Name, fmt.Sprint(a.Enabled), next, last)
				}
			})
		}),
	}
	var a inabav1.Automation
	var ns inabav1.NewThread
	var every uint32
	var daily, once string
	var days []uint
	add := &cobra.Command{
		Use:   "add",
		Short: "Add an automation",
		Long: "Each run sends --prompt to --thread, or to a new thread of --provider in --project or --cwd.\n" +
			"It runs --every N minutes, --daily at HH:MM (on --days, 0 for Sunday), --once at a time, from its --webhook, or by hand.",
		Args: cobra.NoArgs,
		RunE: run(func(ctx context.Context, c *hostClient, _ []string) error {
			if err := live(c); err != nil {
				return err
			}
			a.Enabled = true
			switch {
			case every > 0:
				a.Schedule = &inabav1.Schedule{When: &inabav1.Schedule_EveryMinutes{EveryMinutes: every}}
			case daily != "":
				d := &inabav1.Daily{At: daily}
				for _, day := range days {
					d.Weekdays = append(d.Weekdays, uint32(day))
				}
				a.Schedule = &inabav1.Schedule{When: &inabav1.Schedule_Daily{Daily: d}}
			case once != "":
				at, err := time.ParseInLocation("2006-01-02 15:04", once, time.Local)
				if err != nil {
					return errors.New("--once takes a time as 2006-01-02 15:04")
				}
				a.Schedule = &inabav1.Schedule{When: &inabav1.Schedule_OnceAt{OnceAt: timestamppb.New(at)}}
			}
			if a.ThreadId == "" {
				if ns.Cwd != "" {
					abs, err := filepath.Abs(ns.Cwd)
					if err != nil {
						return err
					}
					ns.Cwd = abs
				}
				a.NewThread = &ns
			}
			res, err := c.autos.CreateAutomation(ctx, &a)
			if err != nil {
				return err
			}
			fmt.Printf("Added automation %s\n", res.Automation.Id)
			if res.WebhookSecret != "" {
				fmt.Printf("Webhook: POST <server>/hooks/<host id>/%s\nSecret, not shown again: %s\n", res.Automation.Id, res.WebhookSecret)
			}
			return nil
		}),
	}
	f := add.Flags()
	f.StringVar(&a.Name, "name", "", "what to call it")
	f.StringVar(&a.Prompt, "prompt", "", "the message each run sends; {{payload}} is the webhook's body, {{time}} the run's time")
	f.StringVar(&a.ThreadId, "thread", "", "send to this thread")
	f.StringVar(&ns.Provider, "provider", "", "for a new thread each run: its provider")
	f.StringVar(&ns.ProjectId, "project", "", "for a new thread each run: its project")
	f.StringVar(&ns.Cwd, "cwd", "", "for a new thread each run outside a project: its folder")
	f.BoolVar(&ns.Worktree, "worktree", false, "give each run's thread its own worktree")
	f.Uint32Var(&every, "every", 0, "run every this many minutes")
	f.StringVar(&daily, "daily", "", "run each day at HH:MM")
	f.UintSliceVar(&days, "days", nil, "with --daily, only on these days, 0 for Sunday to 6")
	f.StringVar(&once, "once", "", "run once, at 2006-01-02 15:04")
	f.BoolVar(&a.Webhook, "webhook", false, "run when its webhook is called")
	_ = add.MarkFlagRequired("name")
	_ = add.MarkFlagRequired("prompt")

	byID := func(use, short string, do func(ctx context.Context, c *hostClient, id string) error) *cobra.Command {
		return &cobra.Command{Use: use + " ID", Short: short, Args: cobra.ExactArgs(1), RunE: run(func(ctx context.Context, c *hostClient, args []string) error {
			if err := live(c); err != nil {
				return err
			}
			return do(ctx, c, args[0])
		})}
	}
	runNow := byID("run", "Run an automation now", func(ctx context.Context, c *hostClient, id string) error {
		r, err := c.autos.RunAutomation(ctx, &inabav1.RunAutomationRequest{Id: id})
		if err != nil {
			return err
		}
		if r.Error != "" {
			return errors.New(r.Error)
		}
		fmt.Printf("Sent to thread %s\n", r.ThreadId)
		return nil
	})
	rm := byID("rm", "Delete an automation", func(ctx context.Context, c *hostClient, id string) error {
		_, err := c.autos.DeleteAutomation(ctx, &inabav1.DeleteAutomationRequest{Id: id})
		return err
	})
	cmd.AddCommand(list, add, runNow, rm, notifierCommand(live))
	return cmd
}

func notifierCommand(live func(*hostClient) error) *cobra.Command {
	cmd := &cobra.Command{Use: "notifier", Short: "Tell another service when a thread finishes a turn or needs an answer"}
	byLive := func(do func(ctx context.Context, c *hostClient, args []string) error) func(*cobra.Command, []string) error {
		return run(func(ctx context.Context, c *hostClient, args []string) error {
			if err := live(c); err != nil {
				return err
			}
			return do(ctx, c, args)
		})
	}
	list := &cobra.Command{Use: "list", Short: "List the notifiers", Args: cobra.NoArgs, RunE: byLive(func(ctx context.Context, c *hostClient, _ []string) error {
		res, err := c.autos.ListNotifiers(ctx, &inabav1.ListNotifiersRequest{})
		if err != nil {
			return err
		}
		return table("ID\tURL\tEVENTS\tLAST DELIVERY", func(add func(...string)) {
			for _, n := range res.Notifiers {
				events, last := strings.Join(n.Events, ","), "-"
				if events == "" {
					events = "all"
				}
				if n.AutomationsOnly {
					events += " (automations)"
				}
				if len(n.Deliveries) > 0 {
					d := n.Deliveries[0]
					last = fmt.Sprintf("%s %s %d %s", d.Time.AsTime().Local().Format("2006-01-02 15:04"), d.Event, d.Status, d.Error)
				}
				add(n.Id, n.Url, events, last)
			}
		})
	})}
	var n inabav1.Notifier
	add := &cobra.Command{Use: "add URL", Short: "Add a notifier", Args: cobra.ExactArgs(1), RunE: byLive(func(ctx context.Context, c *hostClient, args []string) error {
		n.Url = args[0]
		res, err := c.autos.CreateNotifier(ctx, &n)
		if err != nil {
			return err
		}
		fmt.Printf("Added notifier %s\nSigning secret, not shown again: %s\n", res.Notifier.Id, res.Secret)
		return nil
	})}
	add.Flags().StringSliceVar(&n.Events, "events", nil, "turn.ended, turn.failed, input.needed; all when left out")
	add.Flags().BoolVar(&n.AutomationsOnly, "automations-only", false, "only for threads automations started")
	test := &cobra.Command{Use: "test ID", Short: "Send a test event", Args: cobra.ExactArgs(1), RunE: byLive(func(ctx context.Context, c *hostClient, args []string) error {
		d, err := c.autos.TestNotifier(ctx, &inabav1.TestNotifierRequest{Id: args[0]})
		if err != nil {
			return err
		}
		if d.Error != "" {
			return errors.New(d.Error)
		}
		fmt.Printf("Delivered: %d\n", d.Status)
		return nil
	})}
	rm := &cobra.Command{Use: "rm ID", Short: "Delete a notifier", Args: cobra.ExactArgs(1), RunE: byLive(func(ctx context.Context, c *hostClient, args []string) error {
		_, err := c.autos.DeleteNotifier(ctx, &inabav1.DeleteNotifierRequest{Id: args[0]})
		return err
	})}
	cmd.AddCommand(list, add, test, rm)
	return cmd
}

func moduleCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "module", Short: "Turn this host's optional parts on and off", Long: "Changes take effect at once, while the host runs; threads are not touched."}
	live := func(c *hostClient) error {
		if !c.live {
			return errors.New("the host is not running; start it with inaba connect")
		}
		return nil
	}
	list := &cobra.Command{Use: "list", Short: "List the modules", Args: cobra.NoArgs, RunE: run(func(ctx context.Context, c *hostClient, _ []string) error {
		if err := live(c); err != nil {
			return err
		}
		res, err := c.modules.ListModules(ctx, &inabav1.ListModulesRequest{})
		if err != nil {
			return err
		}
		return table("NAME\tON\tDESCRIPTION", func(add func(...string)) {
			for _, m := range res.Modules {
				on := fmt.Sprint(m.Enabled)
				if m.Error != "" {
					on += " (failed: " + m.Error + ")"
				}
				add(m.Name, on, m.Description)
			}
		})
	})}
	set := func(use string, enabled bool) *cobra.Command {
		return &cobra.Command{Use: use + " NAME", Short: strings.ToUpper(use[:1]) + use[1:] + " a module", Args: cobra.ExactArgs(1), RunE: run(func(ctx context.Context, c *hostClient, args []string) error {
			if err := live(c); err != nil {
				return err
			}
			m, err := c.modules.SetModule(ctx, &inabav1.SetModuleRequest{Name: args[0], Enabled: enabled})
			if err != nil {
				return err
			}
			if m.Error != "" {
				return errors.New(m.Name + " is on but could not start: " + m.Error)
			}
			return nil
		})}
	}
	cmd.AddCommand(list, set("enable", true), set("disable", false))
	return cmd
}

func mcpCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: "Manage the MCP servers this host gives its threads' agents"}
	live := func(c *hostClient) error {
		if !c.live {
			return errors.New("the host is not running; start it with inaba connect")
		}
		return nil
	}
	list := &cobra.Command{Use: "list", Short: "List the MCP servers", Args: cobra.NoArgs, RunE: run(func(ctx context.Context, c *hostClient, _ []string) error {
		if err := live(c); err != nil {
			return err
		}
		res, err := c.mcp.ListMcpServers(ctx, &inabav1.ListMcpServersRequest{})
		if err != nil {
			return err
		}
		return table("ID\tNAME\tON\tRUNS\tPROJECTS", func(add func(...string)) {
			for _, m := range res.Servers {
				runs := m.Url
				if runs == "" {
					runs = strings.Join(append([]string{m.Command}, m.Args...), " ")
				}
				projects := strings.Join(m.ProjectIds, ",")
				if projects == "" {
					projects = "all"
				}
				add(m.Id, m.Name, fmt.Sprint(m.Enabled), runs, projects)
			}
		})
	})}
	var m inabav1.McpServer
	var env, headers []string
	add := &cobra.Command{
		Use:   "add NAME [-- COMMAND ARGS...]",
		Short: "Add an MCP server: a command after --, or --url",
		Args:  cobra.MinimumNArgs(1),
		RunE: run(func(ctx context.Context, c *hostClient, args []string) error {
			if err := live(c); err != nil {
				return err
			}
			m.Name, m.Enabled = args[0], true
			if len(args) > 1 {
				m.Command, m.Args = args[1], args[2:]
			}
			pairs := func(list []string, sep string) map[string]string {
				out := map[string]string{}
				for _, p := range list {
					k, v, _ := strings.Cut(p, sep)
					out[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
				return out
			}
			m.Env, m.Headers = pairs(env, "="), pairs(headers, ":")
			made, err := c.mcp.CreateMcpServer(ctx, &m)
			if err != nil {
				return err
			}
			fmt.Printf("Added MCP server %s\n", made.Id)
			return nil
		}),
	}
	add.Flags().StringVar(&m.Url, "url", "", "reach the server over Streamable HTTP at this address")
	add.Flags().StringArrayVar(&env, "env", nil, "KEY=VALUE for the command, repeatable")
	add.Flags().StringArrayVar(&headers, "header", nil, "Name: value for the address, repeatable")
	add.Flags().StringSliceVar(&m.ProjectIds, "project", nil, "only for threads in these projects")
	rm := &cobra.Command{Use: "rm ID", Short: "Delete an MCP server", Args: cobra.ExactArgs(1), RunE: run(func(ctx context.Context, c *hostClient, args []string) error {
		if err := live(c); err != nil {
			return err
		}
		_, err := c.mcp.DeleteMcpServer(ctx, &inabav1.DeleteMcpServerRequest{Id: args[0]})
		return err
	})}
	cmd.AddCommand(list, add, rm)
	return cmd
}
