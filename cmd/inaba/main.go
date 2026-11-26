// inaba runs coding agents on this machine and connects them to an inaba-server.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"connectrpc.com/connect/v2"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/config"
	"github.com/Alex4386/inaba/internal/control"
	"github.com/Alex4386/inaba/internal/ezmode"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/hostconfig"
	"github.com/Alex4386/inaba/internal/mcpbridge"
	"github.com/Alex4386/inaba/internal/modules"
	"github.com/Alex4386/inaba/internal/provider/acp"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/tunnel"
	"github.com/Alex4386/inaba/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:           "inaba",
		Short:         "Run coding agents and terminals on this machine for an inaba-server",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		// Every command needs the database and socket, so settings load before any of them runs.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error { return loadSettings(cmd) },
	}
	f := root.PersistentFlags()
	f.StringVar(&configPath, "config", "", "JSON or YAML settings file read last, over the others found (default: $INABA_CONFIG; see the README)")
	f.String("data", "", "SQLite file that stores this host's projects and threads; its folder also holds worktrees and the sign-in (default: see the README)")
	f.String("socket", "", "control socket the host listens on and the other commands talk to (default: see the README)")
	root.AddCommand(loginCommand(), logoutCommand(), connectCommand(), openCommand(), projectCommand(), sessionCommand(), housekeepingCommand(), automationCommand(), moduleCommand(), mcpCommand(), mcpBridgeCommand(), updateCommand(), restartCommand(), ezmodeCommand())
	if err := root.Execute(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

// settings are the host's own; the same file's "providers" belong to hostconfig.
type settings struct {
	Server string `koanf:"server"`
	Token  string `koanf:"token"`
	Data   string `koanf:"data"`
	Socket string `koanf:"socket"`
	Listen string `koanf:"listen"`
}

// flagKeys maps each flag to the setting it overrides.
var flagKeys = map[string]string{"server": "server", "token": "token", "data": "data", "socket": "socket", "listen": "listen"}

var (
	cfg settings
	// configPath is --config, the settings file read over every other.
	configPath string
	// settingsFiles are every settings file, lowest first, and userFile the one the
	// host settings in the web UI write to.
	settingsFiles []string
	userFile      string
	// settingsRead are the files that existed, logged at startup.
	settingsRead []string
	// dataPath is the database every command works on.
	dataPath string
	// socketFile is where the other commands ask the running host; they work on the
	// database themselves only when no host is running.
	socketFile string
)

func socketPath() string { return socketFile }

// loadSettings fills cfg and the paths: defaults < file < INABA_ environment < flags given.
func loadSettings(cmd *cobra.Command) error {
	var err error
	if settingsFiles, err = config.Host.Files(configPath); err != nil {
		return err
	}
	if userFile, err = config.Host.UserFile(); err != nil {
		return err
	}
	settingsRead, err = config.Load(settings{}, config.Options{
		Files:     settingsFiles,
		Secrets:   []string{"token"},
		Overrides: config.FlagOverrides(cmd.Flags(), flagKeys),
	}, &cfg)
	if err != nil {
		return err
	}
	if cfg.Server != "" {
		if u, err := url.Parse(cfg.Server); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("server %q is not an http(s) URL", cfg.Server)
		}
	}
	dataPath = cmp.Or(cfg.Data, config.Host.DataPath())
	socketFile = cmp.Or(cfg.Socket, config.SocketPath(dataPath))
	return nil
}

// ezmodeCommand runs the host in the background; see internal/ezmode.
func ezmodeCommand() *cobra.Command {
	return ezmode.Command(func() ezmode.Spec {
		return ezmode.Spec{
			Name: "inaba", Noun: "the host", Sub: "connect",
			Socket: socketPath(), Dir: filepath.Dir(dataPath),
			SettingsFile: userFile, EnvFile: filepath.Join(filepath.Dir(userFile), "host.env"),
			Ready: func() error {
				if cfg.Token != "" || cfg.Listen != "" {
					return nil
				}
				if _, err := os.Stat(credentialsPath()); err != nil {
					return errors.New("this machine is not signed in: run inaba login --server URL first")
				}
				return nil
			},
		}
	})
}

func connectCommand() *cobra.Command {
	var logFile string
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Connect this machine to an inaba-server and run its threads",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if logFile != "" {
				f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil {
					return fmt.Errorf("log file: %w", err)
				}
				// slog's default handler writes through the log package.
				log.SetOutput(f)
			}
			serverURL, token, listen := cfg.Server, cfg.Token, cfg.Listen
			slog.Info("settings", "files", settingsRead)
			// Agents that cannot reach HTTP MCP servers get them through this binary.
			if exe, err := os.Executable(); err == nil {
				acp.MCPBridge = []string{exe, "mcp-bridge"}
			}
			// A static --token connects as it is; otherwise the sign-in from `inaba login` buys access tokens.
			tokens := func(context.Context) (string, error) { return token, nil }
			if token == "" && (serverURL != "" || listen == "") {
				creds, err := loadCredentials()
				if errors.Is(err, os.ErrNotExist) {
					return errors.New("this machine is not signed in: run inaba login --server URL first")
				}
				if err != nil {
					return fmt.Errorf("sign-in %s: %w", credentialsPath(), err)
				}
				serverURL = cmp.Or(serverURL, creds.Server)
				tokens = (&accessTokens{creds: creds}).get
			}
			h := host.New()
			providers := &hostconfig.Service{Files: settingsFiles, Path: userFile, Host: h}
			if err := providers.Apply(); err != nil {
				return err
			}
			if err := h.OpenData(dataPath); err != nil {
				return fmt.Errorf("database %s: %w", dataPath, err)
			}
			handler, err := h.Handler()
			if err != nil {
				return err
			}
			if err := h.AddService(func(s *connect.Server) { inabav1connect.RegisterProviderConfigServiceHandler(s, providers) }); err != nil {
				return err
			}
			mods, err := modules.Start(h, modules.Builtin)
			if err != nil {
				return fmt.Errorf("modules: %w", err)
			}

			if err := config.PrepareSocket(socketPath()); err != nil {
				return err
			}
			socket, err := control.Listen(socketPath())
			if err != nil {
				return err
			}
			defer os.Remove(socketPath())
			go func() {
				// Closed on purpose only for a restart, which says so itself.
				if err := control.ServeHandler(socket, h.ControlHandler()); err != nil && !errors.Is(err, net.ErrClosed) {
					slog.Error("control socket stopped", "error", err)
				}
			}()

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if listen != "" {
				open, err := localOnly(listen, handler)
				if err != nil {
					return err
				}
				slog.Warn("serving the API without authentication", "address", listen)
				go func() {
					if err := http.ListenAndServe(listen, open); err != nil {
						slog.Error("local listener stopped", "error", err)
					}
				}()
			}
			if serverURL != "" {
				go keepConnected(ctx, serverURL, tokens, handler)
			}
			restart := false
			select {
			case <-ctx.Done():
			case <-h.Restarts():
				restart = true
			}
			mods.Close()
			h.Close()
			if restart {
				// What the deferred calls would do, which an exec skips.
				socket.Close()
				os.Remove(socketPath())
				stop()
				slog.Info("starting again")
				return reexec()
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.String("server", "", "inaba-server URL, such as https://inaba.example.com (default: $INABA_SERVER, else the one signed in to)")
	f.String("token", "", "this host's secret, from `inaba-server host add`; prefer $INABA_TOKEN, since others can see flags; without it, the sign-in from `inaba login` is used")
	f.String("listen", "", "also serve the API here with every permission and no sign-in, for local development; loopback addresses only, such as 127.0.0.1:8081")
	f.StringVar(&logFile, "log-file", "", "append the log to this file instead of writing it to the terminal, for where there is none to see it")
	return cmd
}

// localOnly wraps handler for --listen, which grants every permission with no sign-in:
// it must listen on loopback only, answer only requests addressed to a loopback name
// (so a web page cannot reach it through DNS rebinding), and refuse cross-origin
// browser requests (so a web page cannot call it from the visitor's browser).
func localOnly(listen string, handler http.Handler) (http.Handler, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, fmt.Errorf("--listen %q: %w", listen, err)
	}
	if !isLoopback(host) {
		return nil, fmt.Errorf("--listen %q: the unauthenticated API listens on loopback only, such as 127.0.0.1:8081", listen)
	}
	open := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = strings.Trim(r.Host, "[]")
		}
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			http.Error(w, "this API answers only localhost", http.StatusForbidden)
			return
		}
		// With no server to say who is calling, local development gets every permission.
		r.Header.Set(rpc.PermissionsHeader, rpc.PermProjects+","+rpc.PermThreadsAnywhere+","+rpc.PermAutomations+","+rpc.PermHostSettings+","+rpc.PermHousekeeping)
		handler.ServeHTTP(w, r)
	})
	return http.NewCrossOriginProtection().Handler(open), nil
}

// isLoopback reports whether host is localhost or a loopback IP address.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// keepConnected keeps the host attached to the server, reconnecting when the link drops.
func keepConnected(ctx context.Context, serverURL string, tokens func(context.Context) (string, error), handler http.Handler) {
	for ctx.Err() == nil {
		var l net.Listener
		token, err := tokens(ctx)
		if err == nil {
			l, err = tunnel.Dial(ctx, serverURL, token)
		}
		if err == nil {
			slog.Info("connected to the server", "server", serverURL)
			err = http.Serve(l, handler)
			l.Close()
		}
		slog.Warn("not connected to the server; retrying", "error", err)
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
		}
	}
}

// mcpBridgeCommand serves an HTTP MCP server on stdio; the host gives it to agents that
// only start MCP servers as commands.
func mcpBridgeCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "mcp-bridge URL",
		Short:  "Serve the MCP server at URL on stdio",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			headers, err := mcpbridge.HeadersFromEnv()
			if err != nil {
				return err
			}
			return mcpbridge.Run(cmd.Context(), args[0], headers, &mcp.StdioTransport{})
		},
	}
}
