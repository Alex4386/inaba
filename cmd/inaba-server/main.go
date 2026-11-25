// inaba-server is the control server: users sign in to it, hosts connect to it,
// and it relays API calls to them.
package main

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Alex4386/inaba/internal/config"
	"github.com/Alex4386/inaba/internal/control"
	"github.com/Alex4386/inaba/internal/server"
	"github.com/Alex4386/inaba/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:           "inaba-server",
		Short:         "The inaba control server",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		// Every command needs the database and socket, so settings load before any of them runs.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error { return loadSettings(cmd) },
	}
	f := root.PersistentFlags()
	f.StringVar(&configFlag, "config", "", "JSON or YAML settings file read last, over the others found (default: $INABA_CONFIG; see the README)")
	f.String("data", "", "SQLite file that stores users, hosts and tokens (default: see the README)")
	f.String("socket", "", "control socket the server listens on and the other commands talk to (default: see the README)")
	root.AddCommand(serveCommand(), userCommand(), hostCommand(), tokenCommand(), housekeepingCommand())
	if err := root.Execute(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

// settings is everything the server reads from its file, environment and flags.
type settings struct {
	Listen     string   `koanf:"listen"`
	PublicURL  string   `koanf:"public_url"`
	Data       string   `koanf:"data"`
	Socket     string   `koanf:"socket"`
	ICEURLs    []string `koanf:"ice_urls"`
	STUNEnable bool     `koanf:"stun_enable"`
	STUNListen string   `koanf:"stun_listen"`
	// TrustedProxies are CIDRs or single addresses, parsed into trustedProxies.
	TrustedProxies []string `koanf:"trusted_proxies"`
	FrameAncestors []string `koanf:"frame_ancestors"`
	OIDC           struct {
		Issuer         string            `koanf:"issuer"`
		ClientID       string            `koanf:"client_id"`
		ClientSecret   string            `koanf:"client_secret"`
		Name           string            `koanf:"name"`
		Scopes         []string          `koanf:"scopes"`
		ExtraParams    map[string]string `koanf:"extra_params"`
		AllowedDomains []string          `koanf:"allowed_domains"`
		AllowedUsers   []string          `koanf:"allowed_users"`
		AllowedGroups  []string          `koanf:"allowed_groups"`
		GroupsClaim    string            `koanf:"groups_claim"`
		// EmailVerifiedRequired defaults to true here, where the default is explicit;
		// server.Config takes its inverse so a zero Config stays strict.
		EmailVerifiedRequired bool     `koanf:"email_verified_required"`
		AdminGroups           []string `koanf:"admin_groups"`
	} `koanf:"oidc"`
}

// lists are the settings the environment gives as comma-separated lists.
var lists = map[string]bool{
	"ice_urls": true, "trusted_proxies": true, "frame_ancestors": true,
	"oidc.scopes": true, "oidc.allowed_domains": true, "oidc.allowed_users": true, "oidc.allowed_groups": true, "oidc.admin_groups": true,
}

// flagKeys maps each flag to the setting it overrides.
var flagKeys = map[string]string{
	"data": "data", "socket": "socket", "listen": "listen", "public-url": "public_url", "ice-url": "ice_urls", "stun-enable": "stun_enable", "stun-listen": "stun_listen",
	"trusted-proxy": "trusted_proxies", "frame-ancestor": "frame_ancestors",
	"oidc-issuer": "oidc.issuer", "oidc-client-id": "oidc.client_id", "oidc-client-secret": "oidc.client_secret", "oidc-name": "oidc.name",
	"oidc-scope": "oidc.scopes", "oidc-extra-param": "oidc.extra_params", "oidc-allowed-domain": "oidc.allowed_domains",
	"oidc-allowed-user": "oidc.allowed_users", "oidc-allowed-group": "oidc.allowed_groups", "oidc-groups-claim": "oidc.groups_claim",
	"oidc-email-verified-required": "oidc.email_verified_required", "oidc-admin-group": "oidc.admin_groups",
}

var (
	configFlag string
	cfg        settings
	// trustedProxies is cfg.TrustedProxies parsed.
	trustedProxies []netip.Prefix
	// settingsRead are the settings files that existed, logged at startup.
	settingsRead []string
	// dataPath is the database every command works on.
	dataPath string
	// socketFile is where the admin commands ask the running server; they work on
	// the database themselves only when no server is running.
	socketFile string
)

func socketPath() string { return socketFile }

// loadSettings fills cfg, dataPath and socketFile: defaults < file < systemd
// credential < INABA_ environment < flags given on the command line.
func loadSettings(cmd *cobra.Command) error {
	defaults := settings{Listen: ":8080", STUNListen: ":3478"}
	defaults.OIDC.Name = "SSO"
	defaults.OIDC.EmailVerifiedRequired = true
	files, err := config.Server.Files(configFlag)
	if err != nil {
		return err
	}
	settingsRead, err = config.Load(defaults, config.Options{
		Files:       files,
		Secrets:     []string{"oidc.client_secret"},
		Lists:       lists,
		Sections:    []string{"oidc"},
		Credentials: map[string]string{"oidc_client_secret": "oidc.client_secret"},
		Overrides:   config.FlagOverrides(cmd.Flags(), flagKeys),
	}, &cfg)
	if err != nil {
		return err
	}
	if cfg.PublicURL != "" {
		if u, err := url.Parse(cfg.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("public_url %q is not an http(s) URL", cfg.PublicURL)
		}
	}
	if cfg.OIDC.Issuer != "" && cfg.OIDC.ClientID == "" {
		return errors.New("OIDC sign-in needs oidc.client_id as well as oidc.issuer")
	}
	if trustedProxies, err = parseProxies(cfg.TrustedProxies); err != nil {
		return err
	}
	dataPath = cmp.Or(cfg.Data, config.Server.DataPath())
	socketFile = cmp.Or(cfg.Socket, config.SocketPath(dataPath))
	return nil
}

// parseProxies reads trusted_proxies: CIDRs, or single addresses meaning just that one.
func parseProxies(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return nil, fmt.Errorf("trusted_proxies %q is neither a CIDR nor an IP address", s)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// publicSTUN is advertised when ice_urls is not set and the built-in STUN server is off.
const publicSTUN = "stun:stun.cloudflare.com:3478"

// iceURLs is what browsers and hosts are told to use: the configured servers as they are,
// else only the built-in one if it is on (stunListen is not empty), else publicSTUN. The
// built-in one can be advertised only under a public name, so without one there is none.
func iceURLs(configured []string, publicURL, stunListen string) []string {
	if len(configured) > 0 {
		return configured
	}
	if stunListen == "" {
		return []string{publicSTUN}
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	_, port, _ := net.SplitHostPort(stunListen)
	return []string{"stun:" + net.JoinHostPort(u.Hostname(), port)}
}

func serveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the web UI and API, and accept hosts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			listen, stunListen := cfg.Listen, cfg.STUNListen
			if !cfg.STUNEnable {
				stunListen = ""
			} else if stunListen == "" {
				return errors.New("stun_enable needs stun_listen")
			}
			slog.Info("settings", "files", settingsRead)
			o := cfg.OIDC
			srvCfg := server.Config{DataPath: dataPath, PublicURL: cfg.PublicURL, ICEURLs: cfg.ICEURLs,
				TrustedProxies: trustedProxies, FrameAncestors: cfg.FrameAncestors,
				OIDC: server.OIDCConfig{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: o.ClientSecret, Name: o.Name,
					Scopes: o.Scopes, ExtraParams: o.ExtraParams, AllowedDomains: o.AllowedDomains, AllowedUsers: o.AllowedUsers,
					AllowedGroups: o.AllowedGroups, GroupsClaim: o.GroupsClaim, AllowUnverifiedEmail: !o.EmailVerifiedRequired, AdminGroups: o.AdminGroups}}
			if stunListen != "" {
				conn, err := net.ListenPacket("udp", stunListen)
				if err != nil {
					return err
				}
				go server.ServeSTUN(conn)
			}
			srvCfg.ICEURLs = iceURLs(cfg.ICEURLs, cfg.PublicURL, stunListen)
			if len(srvCfg.ICEURLs) == 0 {
				slog.Warn("the built-in STUN server is on but not offered to anyone; set public_url, or ice_urls")
			}
			if err := config.PrepareSocket(socketPath()); err != nil {
				return err
			}
			srv, err := server.New(cmd.Context(), srvCfg)
			if err != nil {
				return err
			}
			token, err := srv.OnboardingToken()
			if err != nil {
				return err
			}
			if token != "" {
				base := srvCfg.PublicURL
				if base == "" {
					base = "http://" + listen
					if strings.HasPrefix(listen, ":") {
						base = "http://localhost" + listen
					}
				}
				// The token rides in the fragment, which browsers do not send to servers or proxies.
				slog.Info("no accounts yet; open this link to create the first one", "url", strings.TrimSuffix(base, "/")+"/ui/onboarding#token="+token)
			}
			handler, err := srv.Handler()
			if err != nil {
				return err
			}
			socket, err := control.Listen(socketPath())
			if err != nil {
				return err
			}
			defer os.Remove(socketPath())
			go func() {
				if err := control.Serve(socket, srv.AdminServer()); err != nil {
					slog.Error("control socket stopped", "error", err)
				}
			}()
			// Plain HTTP/2 lets a TLS-terminating proxy multiplex the UI's long-lived streams.
			protocols := new(http.Protocols)
			protocols.SetHTTP1(true)
			protocols.SetUnencryptedHTTP2(true)
			httpServer := &http.Server{Addr: listen, Handler: handler, Protocols: protocols, ReadHeaderTimeout: 10 * time.Second}
			// Stop on a signal by returning, so the socket above is removed.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			go func() {
				<-ctx.Done()
				httpServer.Close()
			}()
			slog.Info("listening", "address", listen, "data", dataPath, "socket", socketPath())
			if err := httpServer.ListenAndServe(); err != nil && ctx.Err() == nil {
				return err
			}
			return nil
		},
	}
	// The defaults shown match loadSettings'; only flags actually given override the file and environment.
	f := cmd.Flags()
	f.String("listen", ":8080", "address to serve on")
	f.String("public-url", "", "URL browsers use to reach this server, such as https://inaba.example.com")
	f.StringSlice("ice-url", nil, "STUN URL for direct connections, such as stun:stun.example.com:3478; repeatable")
	f.Bool("stun-enable", false, "run the built-in STUN server and offer it to browsers and hosts; its UDP port must be reachable from outside")
	f.String("stun-listen", ":3478", "UDP address of the built-in STUN server, with --stun-enable")
	f.String("oidc-issuer", "", "OIDC issuer URL; enables OIDC sign-in. Anyone the identity provider accepts gets an account, and shared hosts are open to every account unless made members-only, so limit who the provider lets in")
	f.String("oidc-client-id", "", "OIDC client id")
	f.String("oidc-client-secret", "", "OIDC client secret; prefer $INABA_OIDC__CLIENT_SECRET or a systemd credential, since others can see flags")
	f.String("oidc-name", "SSO", "what the sign-in button calls the OIDC provider")
	f.StringSlice("oidc-scope", nil, "OIDC scope to ask for besides openid (default: profile, email); repeatable")
	f.StringToString("oidc-extra-param", nil, "extra parameter for the provider's sign-in URL, such as hd=example.com; repeatable")
	f.StringSlice("oidc-allowed-domain", nil, "email domain that may sign in through OIDC; repeatable")
	f.StringSlice("oidc-allowed-user", nil, "email address that may sign in through OIDC; repeatable")
	f.StringSlice("oidc-allowed-group", nil, "group whose members may sign in through OIDC; repeatable")
	f.String("oidc-groups-claim", "", "ID token claim listing the user's groups (default: groups)")
	f.Bool("oidc-email-verified-required", true, "email rules match only addresses the provider marked verified")
	f.StringSlice("oidc-admin-group", nil, "group whose members are administrators; when set, the provider decides who is one; repeatable")
	f.StringSlice("trusted-proxy", nil, "reverse proxy, as a CIDR or address, whose X-Forwarded-For says who the client is; repeatable")
	f.StringSlice("frame-ancestor", nil, "page allowed to show the UI in a frame, as a CSP frame-ancestors source (default: 'self'); repeatable")
	return cmd
}
