package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/spf13/cobra"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
)

// credentials are what `inaba login` leaves for `inaba connect`.
type credentials struct {
	Server       string `json:"server"`
	HostID       string `json:"hostId"`
	HostName     string `json:"hostName"`
	RefreshToken string `json:"refreshToken"`
}

// credentialsPath sits beside the database, so each --data has its own sign-in.
func credentialsPath() string { return filepath.Join(filepath.Dir(dataPath), "credentials.json") }

func loadCredentials() (*credentials, error) {
	b, err := os.ReadFile(credentialsPath())
	if err != nil {
		return nil, err
	}
	var c credentials
	return &c, json.Unmarshal(b, &c)
}

func saveCredentials(c *credentials) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	path := credentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Written whole and readable only by its owner: the refresh token is the host's key.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hostService(server string) inabav1connect.HostServiceClient {
	return inabav1connect.NewHostServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, strings.TrimSuffix(server, "/"))))
}

// style dresses text for a terminal and leaves it plain for a pipe or a log.
type style bool

func (s style) wrap(code, text string) string {
	if !s {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}
func (s style) bold(t string) string  { return s.wrap("1", t) }
func (s style) dim(t string) string   { return s.wrap("2", t) }
func (s style) cyan(t string) string  { return s.wrap("36", t) }
func (s style) green(t string) string { return s.wrap("32", t) }

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}

func loginCommand() *cobra.Command {
	var server, name string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign this machine in to an inaba-server as a host",
		Long: "Shows a code and a link. Open the link signed in to inaba, check the code, and approve;\n" +
			"the host is then yours, and `inaba connect` connects it with no more flags.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			server = cmp.Or(server, cfg.Server)
			if server == "" {
				return errors.New("pass --server, the address you open inaba at, such as https://inaba.example.com")
			}
			if name == "" {
				name, _ = os.Hostname()
			}
			ctx := cmd.Context()
			hosts := hostService(server)
			start, err := hosts.StartHostLogin(ctx, &inabav1.StartHostLoginRequest{Name: name})
			if err != nil {
				return fmt.Errorf("starting the sign-in at %s: %w", server, err)
			}
			// Without a public URL configured, the server names the page by its path alone.
			link := start.VerificationUriComplete
			if base, err := url.Parse(server); err == nil {
				if ref, err := url.Parse(link); err == nil {
					link = base.ResolveReference(ref).String()
				}
			}
			tty := isTerminal(os.Stdout)
			st := style(tty)
			fmt.Printf("\n  To sign %s in to inaba, open\n\n      %s\n\n  and check that it shows the code  %s\n\n", st.bold(name), st.cyan(link), st.bold(start.UserCode))
			if tty && !noBrowser {
				openBrowser(link)
			}

			interval := time.Duration(max(start.IntervalSeconds, 1)) * time.Second
			deadline := start.ExpiresAt.AsTime()
			frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
			var mu sync.Mutex
			waiting := true
			if tty {
				go func() {
					for i := 0; ; i++ {
						mu.Lock()
						if !waiting {
							mu.Unlock()
							return
						}
						left := time.Until(deadline).Round(time.Second)
						fmt.Printf("\r  %s %s ", st.cyan(frames[i%len(frames)]), st.dim(fmt.Sprintf("Waiting for approval… %s left", left)))
						mu.Unlock()
						time.Sleep(100 * time.Millisecond)
					}
				}()
			} else {
				fmt.Println("  Waiting for approval…")
			}
			stop := func() {
				mu.Lock()
				waiting = false
				if tty {
					fmt.Print("\r\x1b[2K")
				}
				mu.Unlock()
			}
			for {
				select {
				case <-ctx.Done():
					stop()
					return ctx.Err()
				case <-time.After(interval):
				}
				res, err := hosts.PollHostLogin(ctx, &inabav1.PollHostLoginRequest{DeviceCode: start.DeviceCode})
				if connect.CodeOf(err) == connect.CodeResourceExhausted {
					interval += time.Second
					continue
				}
				if err != nil {
					stop()
					return err
				}
				if res.Pending {
					continue
				}
				stop()
				c := &credentials{Server: server, HostID: res.Host.Id, HostName: res.Host.Name, RefreshToken: res.RefreshToken}
				if err := saveCredentials(c); err != nil {
					return fmt.Errorf("saving the sign-in: %w", err)
				}
				kind := "yours"
				if res.Host.Shared {
					kind = "shared with every account"
				}
				fmt.Printf("  %s Signed in as host %s, %s.\n\n  Start it with  %s\n\n", st.green("✓"), st.bold(res.Host.Name), kind, st.bold("inaba connect"))
				return nil
			}
		},
	}
	f := cmd.Flags()
	f.StringVar(&server, "server", "", "inaba-server URL, such as https://inaba.example.com (default: $INABA_SERVER or the settings file)")
	f.StringVar(&name, "name", "", "what to call this host (default: the machine's hostname)")
	f.BoolVar(&noBrowser, "no-browser", false, "only print the link; do not open a browser")
	return cmd
}

func logoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget this machine's sign-in",
		Long:  "Removes the saved refresh token. The host stays on the server until it is removed there.",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			c, err := loadCredentials()
			if errors.Is(err, os.ErrNotExist) {
				fmt.Println("Not signed in.")
				return nil
			}
			if err := os.Remove(credentialsPath()); err != nil {
				return err
			}
			if c != nil {
				fmt.Printf("Signed out of %s. Remove host %s there too, so its token stops working.\n", c.Server, c.HostName)
			}
			return nil
		},
	}
}

// accessTokens trades the saved refresh token for access tokens, a new one shortly
// before the last expires.
type accessTokens struct {
	creds   *credentials
	token   string
	expires time.Time
}

func (a *accessTokens) get(ctx context.Context) (string, error) {
	if a.token != "" && time.Until(a.expires) > 5*time.Minute {
		return a.token, nil
	}
	res, err := hostService(a.creds.Server).RefreshHostToken(ctx, &inabav1.RefreshHostTokenRequest{RefreshToken: a.creds.RefreshToken})
	if err != nil {
		return "", err
	}
	a.token, a.expires = res.AccessToken, res.ExpiresAt.AsTime()
	return a.token, nil
}
