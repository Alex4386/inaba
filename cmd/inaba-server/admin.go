package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectinprocess"
	"github.com/spf13/cobra"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/control"
	"github.com/Alex4386/inaba/internal/server"
	"github.com/Alex4386/inaba/internal/store"
)

// adminClient reaches the server's administration API.
type adminClient struct {
	users inabav1connect.AdminServiceClient
	hosts inabav1connect.HostServiceClient
	// live reports whether a running server answered, rather than the database alone.
	live bool
}

// openAdmin talks to the running server over its control socket. With no server
// running there is nothing to ask, so the same API is served from the database
// right here in this process.
func openAdmin(ctx context.Context) (*adminClient, error) {
	transport := control.Dial(socketPath())
	live := transport != nil
	if !live {
		srv, err := server.New(ctx, server.Config{DataPath: dataPath})
		if err != nil {
			return nil, err
		}
		transport = connectinprocess.New(srv.AdminServer())
	}
	client := connect.NewClient(transport)
	return &adminClient{users: inabav1connect.NewAdminServiceClient(client), hosts: inabav1connect.NewHostServiceClient(client), live: live}, nil
}

// run wraps a command body with the admin client and plain error messages.
func run(do func(ctx context.Context, c *adminClient, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		c, err := openAdmin(cmd.Context())
		if err != nil {
			return err
		}
		return control.Plain(do(cmd.Context(), c, args))
	}
}

// table prints rows under a header, in aligned columns.
func table(header string, rows func(add func(cells ...string))) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, header)
	rows(func(cells ...string) { fmt.Fprintln(w, strings.Join(cells, "\t")) })
	return w.Flush()
}

// password returns the first line of stdin, or a generated one, and whether it was generated.
func password(fromStdin bool) (string, bool, error) {
	if !fromStdin {
		return store.RandomPassword(), true, nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		if err == nil {
			err = errors.New("empty")
		}
		return "", false, fmt.Errorf("no password on standard input: %w", err)
	}
	return line, false, nil
}

// permissionWords are the words grant and revoke take, with what each lets an account do.
type permissionWord struct {
	word, does string
	permission inabav1.Permission
}

var permissionWords = []permissionWord{
	{"shared-hosts", "add shared hosts", inabav1.Permission_PERMISSION_MANAGE_HOSTS},
	{"projects", "manage projects", inabav1.Permission_PERMISSION_MANAGE_PROJECTS},
	{"threads-anywhere", "start threads outside projects", inabav1.Permission_PERMISSION_THREADS_ANYWHERE},
	{"automations", "set up automations", inabav1.Permission_PERMISSION_AUTOMATIONS},
	{"host-settings", "change host settings", inabav1.Permission_PERMISSION_HOST_SETTINGS},
	{"housekeeping", "clean up hosts", inabav1.Permission_PERMISSION_HOUSEKEEPING},
}

// hostsWord is the word that granted everything about hosts before they were split, and still
// grants and revokes all of it, so an old command does not quietly do less.
const hostsWord = "hosts"

var hostsWords = []string{"shared-hosts", "host-settings", "housekeeping"}

// access says in words what an account may do.
func access(u *inabav1.User) string {
	if u.Admin {
		return "everything (administrator)"
	}
	words := []string{"use hosts and projects"}
	for _, w := range permissionWords {
		if slices.Contains(u.Permissions, w.permission) {
			words = append(words, w.does)
		}
	}
	return strings.Join(words, ", ")
}

func userCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage the accounts that can sign in"}
	var fromStdin bool
	// setPassword serves both add and passwd: they differ only in the call made.
	setPassword := func(use, short, done string, apply func(ctx context.Context, c *adminClient, name, password string) error) *cobra.Command {
		c := &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: run(func(ctx context.Context, c *adminClient, args []string) error {
				pw, generated, err := password(fromStdin)
				if err != nil {
					return err
				}
				if err := apply(ctx, c, args[0], pw); err != nil {
					return err
				}
				fmt.Printf(done+"\n", args[0])
				if generated {
					fmt.Printf("Password, not shown again: %s\n", pw)
				}
				return nil
			}),
		}
		c.Flags().BoolVar(&fromStdin, "password-stdin", false, "read the password from standard input instead of generating one")
		return c
	}
	var admin bool
	add := setPassword("add NAME", "Add an account that signs in with a password", "Added account %s", func(ctx context.Context, c *adminClient, name, pw string) error {
		_, err := c.users.CreateUser(ctx, &inabav1.CreateUserRequest{Name: name, Password: pw, Admin: admin})
		return err
	})
	add.Flags().BoolVar(&admin, "admin", false, "make the account an administrator")
	// change serves both grant and revoke: they differ only in adding or removing.
	valid := []string{"admin", hostsWord}
	for _, w := range permissionWords {
		valid = append(valid, w.word)
	}
	change := func(use, short string, grant bool) *cobra.Command {
		return &cobra.Command{
			Use:       use + " ACCOUNT {" + strings.Join(valid, "|") + "}...",
			Short:     short,
			Args:      cobra.MinimumNArgs(2),
			ValidArgs: valid,
			RunE: run(func(ctx context.Context, c *adminClient, args []string) error {
				list, err := c.users.ListUsers(ctx, &inabav1.ListUsersRequest{})
				if err != nil {
					return err
				}
				i := slices.IndexFunc(list.Users, func(u *inabav1.User) bool { return u.Id == args[0] || (u.Name == args[0] && !u.Oidc) })
				if i < 0 {
					return fmt.Errorf("no account %s; see `inaba-server user list`", args[0])
				}
				u := list.Users[i]
				req := &inabav1.SetAccessRequest{User: u.Id, Admin: u.Admin}
				held := map[inabav1.Permission]bool{}
				// An administrator is reported with every permission; keep only what was granted by name.
				if !u.Admin {
					for _, p := range u.Permissions {
						held[p] = true
					}
				}
				var words []string
				for _, word := range args[1:] {
					if word == hostsWord {
						words = append(words, hostsWords...)
					} else {
						words = append(words, word)
					}
				}
				for _, word := range words {
					if word == "admin" {
						req.Admin = grant
						continue
					}
					i := slices.IndexFunc(permissionWords, func(w permissionWord) bool { return w.word == word })
					if i < 0 {
						return fmt.Errorf("unknown permission %q; the permissions are %s", word, strings.Join(valid, ", "))
					}
					held[permissionWords[i].permission] = grant
				}
				for p, on := range held {
					if on {
						req.Permissions = append(req.Permissions, p)
					}
				}
				updated, err := c.users.SetAccess(ctx, req)
				if err != nil {
					return err
				}
				fmt.Printf("%s can now: %s\n", updated.Name, access(updated))
				return nil
			}),
		}
	}
	cmd.AddCommand(
		add,
		change("grant", "Grant an account a permission, or make it an administrator", true),
		change("revoke", "Take a permission away from an account", false),
		setPassword("passwd NAME", "Set an account's password and sign it out everywhere", "Changed the password of %s", func(ctx context.Context, c *adminClient, name, pw string) error {
			_, err := c.users.SetPassword(ctx, &inabav1.SetPasswordRequest{User: name, Password: pw})
			return err
		}),
		&cobra.Command{
			Use:   "list",
			Short: "List the accounts",
			Args:  cobra.NoArgs,
			RunE: run(func(ctx context.Context, c *adminClient, _ []string) error {
				res, err := c.users.ListUsers(ctx, &inabav1.ListUsersRequest{})
				if err != nil {
					return err
				}
				return table("ID\tNAME\tSIGNS IN WITH\tCAN", func(add func(...string)) {
					for _, u := range res.Users {
						how := "password"
						if u.Oidc {
							how = "OIDC"
						}
						add(u.Id, u.Name, how, access(u))
					}
				})
			}),
		},
		&cobra.Command{
			Use:   "rm ACCOUNT",
			Short: "Remove an account, its sign-ins and its API tokens",
			Args:  cobra.ExactArgs(1),
			RunE: run(func(ctx context.Context, c *adminClient, args []string) error {
				if _, err := c.users.DeleteUser(ctx, &inabav1.DeleteUserRequest{User: args[0]}); err != nil {
					return err
				}
				fmt.Printf("Removed account %s\n", args[0])
				return nil
			}),
		},
	)
	return cmd
}

func hostCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "host", Short: "Manage the hosts that may connect"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "add NAME",
			Short: "Register a host shared with every account and print the secret it connects with",
			Args:  cobra.ExactArgs(1),
			RunE: run(func(ctx context.Context, c *adminClient, args []string) error {
				res, err := c.hosts.CreateHost(ctx, &inabav1.CreateHostRequest{Name: args[0], Shared: true})
				if err != nil {
					return err
				}
				fmt.Printf("Added host %s (%s)\nSecret, not shown again: %s\n", res.Host.Name, res.Host.Id, res.Secret)
				return nil
			}),
		},
		&cobra.Command{
			Use:   "list",
			Short: "List the hosts",
			Args:  cobra.NoArgs,
			RunE: run(func(ctx context.Context, c *adminClient, _ []string) error {
				res, err := c.hosts.ListHosts(ctx, &inabav1.ListHostsRequest{})
				if err != nil {
					return err
				}
				return table("ID\tNAME\tCONNECTED", func(add func(...string)) {
					for _, h := range res.Hosts {
						// Only a running server knows who is connected.
						connected := "unknown (server not running)"
						if c.live {
							connected = map[bool]string{true: "yes", false: "no"}[h.Online]
						}
						add(h.Id, h.Name, connected)
					}
				})
			}),
		},
		&cobra.Command{
			Use:   "rm ID",
			Short: "Remove a host and disconnect it",
			Args:  cobra.ExactArgs(1),
			RunE: run(func(ctx context.Context, c *adminClient, args []string) error {
				if _, err := c.hosts.DeleteHost(ctx, &inabav1.DeleteHostRequest{Id: args[0]}); err != nil {
					return err
				}
				fmt.Printf("Removed host %s\n", args[0])
				return nil
			}),
		},
	)
	return cmd
}

func tokenCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage API tokens"}
	var user string
	add := &cobra.Command{
		Use:   "add NAME",
		Short: "Create an API token that acts as an account",
		Args:  cobra.ExactArgs(1),
		RunE: run(func(ctx context.Context, c *adminClient, args []string) error {
			res, err := c.users.CreateUserToken(ctx, &inabav1.CreateUserTokenRequest{User: user, Name: args[0]})
			if err != nil {
				return err
			}
			fmt.Printf("Added token %s (%s)\nSecret, not shown again: %s\n", res.Token.Name, res.Token.Id, res.Secret)
			return nil
		}),
	}
	add.Flags().StringVar(&user, "user", "", "the account the token acts as: an id, or the name of a password account")
	_ = add.MarkFlagRequired("user")
	cmd.AddCommand(add,
		&cobra.Command{
			Use:   "list",
			Short: "List every account's API tokens",
			Args:  cobra.NoArgs,
			RunE: run(func(ctx context.Context, c *adminClient, _ []string) error {
				res, err := c.users.ListUserTokens(ctx, &inabav1.ListUserTokensRequest{})
				if err != nil {
					return err
				}
				return table("ID\tNAME\tACCOUNT\tADDED", func(add func(...string)) {
					for _, e := range res.Tokens {
						add(e.Token.Id, e.Token.Name, e.UserName, e.Token.CreatedAt.AsTime().Local().Format("2006-01-02"))
					}
				})
			}),
		},
		&cobra.Command{
			Use:   "rm ID",
			Short: "Revoke an API token",
			Args:  cobra.ExactArgs(1),
			RunE: run(func(ctx context.Context, c *adminClient, args []string) error {
				if _, err := c.users.RevokeUserToken(ctx, &inabav1.RevokeTokenRequest{Id: args[0]}); err != nil {
					return err
				}
				fmt.Printf("Revoked token %s\n", args[0])
				return nil
			}),
		},
	)
	return cmd
}

func housekeepingCommand() *cobra.Command {
	var req inabav1.CleanUpServerRequest
	cmd := &cobra.Command{
		Use:   "housekeeping",
		Short: "Show the server database's size and expired sign-ins, and clear them",
		Long:  "Each host has its own: run inaba housekeeping on the host.",
		Args:  cobra.NoArgs,
		RunE: run(func(ctx context.Context, c *adminClient, _ []string) error {
			var hk *inabav1.ServerHousekeeping
			var err error
			if req.ExpiredTokens || req.CompactDatabase {
				hk, err = c.users.CleanUpServer(ctx, &req)
			} else {
				hk, err = c.users.GetServerHousekeeping(ctx, &inabav1.GetServerHousekeepingRequest{})
			}
			if err != nil {
				return err
			}
			fmt.Printf("Database: %d bytes\nSign-ins: %d active, %d expired tokens\n", hk.DatabaseBytes, hk.ActiveSignIns, hk.ExpiredTokens)
			return nil
		}),
	}
	cmd.Flags().BoolVar(&req.ExpiredTokens, "expired", false, "delete sign-ins and tokens past their expiry")
	cmd.Flags().BoolVar(&req.CompactDatabase, "compact", false, "give the space deleted data leaves in the database back to the disk")
	return cmd
}
