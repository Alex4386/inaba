// Package server is inaba-server: sign-in, the host registry, and the relay that
// forwards API calls to hosts.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/hashicorp/yamux"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/gen/openapi"
	"github.com/Alex4386/inaba/internal/push"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/store"
	"github.com/Alex4386/inaba/internal/tunnel"
	"github.com/Alex4386/inaba/internal/webui"
)

const (
	sessionCookie = "inaba_session"
	sessionTTL    = 30 * 24 * time.Hour
)

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("-"), bcrypt.DefaultCost)

type Config struct {
	// DataPath is the SQLite file holding users, hosts and tokens.
	DataPath string
	// PublicURL is how browsers reach the server, such as https://inaba.example.com.
	// It decides the OIDC redirect address and whether cookies are marked Secure.
	PublicURL string
	// ICEURLs are the STUN servers browsers and hosts use to connect directly.
	ICEURLs []string
	OIDC    OIDCConfig
	// TrustedProxies are the reverse proxies whose X-Forwarded-For and X-Real-IP headers
	// say who the client is. From any other peer those headers are ignored.
	TrustedProxies []netip.Prefix
	// FrameAncestors are the pages that may show the web UI in a frame, as CSP
	// frame-ancestors sources: 'self', 'none', or http(s) origins and host patterns such
	// as https://*.example.com. Empty means 'self'; ["*"] sends no frame header at all.
	FrameAncestors []string
	// NotifyPrivate lets notification channels post to addresses inside the server's
	// network: loopback, private, link-local and the like.
	NotifyPrivate bool
}

type Server struct {
	inabav1connect.UnimplementedAuthServiceHandler
	inabav1connect.UnimplementedHostServiceHandler
	inabav1connect.UnimplementedPresenceServiceHandler

	cfg   Config
	store *store.Store
	oidc  *oidcFlow

	mu    sync.Mutex
	links map[string]*link
	// ceremonies are open passkey prompts, by id.
	ceremonies map[string]*ceremony
	// present is where each signed-in browser was last, by user and client id.
	present map[string]*presence
	// seats is who drives each session, by host and thread id: a client id from present.
	seats map[string]string
	// logins are machines waiting to be approved as hosts, by user code.
	logins map[string]*hostLogin
	// onboardToken is non-empty while first-time setup is open.
	onboardToken string

	// senders push to devices, by platform; vapidPublic is what browsers subscribe with.
	senders     map[string]push.Sender
	vapidPublic string
	// channels delivers to people's notification channels.
	channels *push.Channels
	// pushSlots bounds the pushes on their way at once.
	pushSlots chan struct{}
	// channelSlots bounds the channel deliveries on their way at once; deliveries tracks
	// them, so tests can wait for them.
	// shortcut: a delivery waiting for a slot is a goroutine, unbounded while questions bypass the alert limit; queue them if that is ever abused.
	channelSlots chan struct{}
	deliveries   sync.WaitGroup
	// alertLimits pace each host's alerts, by host id, under mu; kept here so a host that
	// reconnects does not start with a full budget again.
	alertLimits map[string]*rate.Limiter
	// ipGuesses and accountGuesses pace password and one-time-code checks.
	ipGuesses, accountGuesses *attempts
	// frameHeaders are the headers the web UI is served with to say who may frame it.
	frameHeaders map[string]string
}

// link is a connected host.
type link struct {
	session *yamux.Session
	proxy   *httputil.ReverseProxy
	// owner is the user a personal host serves, "" for a shared one.
	owner string
}

func New(ctx context.Context, cfg Config) (*Server, error) {
	frames, err := frameHeaders(cfg.FrameAncestors)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DataPath)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg: cfg, store: st, links: map[string]*link{}, frameHeaders: frames,
		// A client gets ten tries at once, then five a minute. An account gets twice the
		// tries and refills twice as fast, so no single client can keep it locked.
		ipGuesses:      &attempts{every: 12 * time.Second, burst: 10},
		accountGuesses: &attempts{every: 6 * time.Second, burst: 20},
	}
	if err := s.setUpPush(); err != nil {
		return nil, err
	}
	if cfg.OIDC.Issuer != "" {
		if s.oidc, err = newOIDCFlow(ctx, cfg); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// OnboardingToken prepares first-time setup. On a server with no account it returns
// the token that lets the holder create the first one, through /onboarding#token=...;
// otherwise it returns "". The token lives only in this process.
func (s *Server) OnboardingToken() (string, error) {
	users, err := s.store.Users()
	if err != nil || len(users) > 0 {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onboardToken = store.RandomPassword() + store.RandomPassword()
	return s.onboardToken, nil
}

func (s *Server) Handler() (http.Handler, error) {
	rpcServer := connect.NewServer()
	inabav1connect.RegisterAuthServiceHandler(rpcServer, s)
	inabav1connect.RegisterHostServiceHandler(rpcServer, s)
	inabav1connect.RegisterPresenceServiceHandler(rpcServer, s)
	inabav1connect.RegisterAdminServiceHandler(rpcServer, admin{s})
	inabav1connect.RegisterNotificationServiceHandler(rpcServer, notifications{s: s})
	// The REST mapping claims "/" on its mux, so the API gets a mux of its own.
	api := http.NewServeMux()
	if err := rpc.Mount(api, rpcServer); err != nil {
		return nil, err
	}
	ui := webui.Handler()
	mux := http.NewServeMux()
	mux.HandleFunc(tunnel.Path, s.serveTunnel)
	// A host's REST API sits beside the server's own, under the same version prefix:
	// /v1/hosts/{id}/threads is the host's /v1/threads.
	mux.HandleFunc("/v1/hosts/{id}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		s.serveHost(w, r, "/v1/"+r.PathValue("rest"))
	})
	// Connect procedures carry their version in their own name (inaba.v1.Service/Method).
	mux.HandleFunc("/hosts/{id}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.PathValue("rest"), "inaba.") {
			http.NotFound(w, r)
			return
		}
		s.serveHost(w, r, "/"+r.PathValue("rest"))
	})
	// Automations' webhooks, for callers without an account: the host checks the
	// automation's own secret, which the relay would otherwise drop with Authorization.
	mux.HandleFunc("POST /hooks/{id}/{automation}", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
		r.Header.Del(hookTokenHeader)
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			r.Header.Set(hookTokenHeader, token)
		}
		s.serveHost(w, r, "/hooks/"+r.PathValue("automation"))
	})
	mux.HandleFunc("GET /openapi.yaml", serveYAML(openapi.Document))
	mux.Handle("GET /docs", http.RedirectHandler("/ui/docs.html", http.StatusFound))
	if s.oidc != nil {
		mux.HandleFunc("GET /auth/oidc/login", s.oidc.login)
		mux.HandleFunc("GET /auth/oidc/callback", func(w http.ResponseWriter, r *http.Request) { s.oidc.callback(s, w, r) })
	}
	mux.HandleFunc("GET /v1/avatars/{id}", s.serveAvatar)
	// The host itself, which the pattern above would otherwise redirect to its trailing slash.
	mux.Handle("/v1/hosts/{id}", api)
	mux.Handle("/ui/", http.StripPrefix("/ui", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the configured pages may frame the app, since a frame lets a page trick a click.
		for k, v := range s.frameHeaders {
			w.Header().Set(k, v)
		}
		ui.ServeHTTP(w, r)
	})))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isAPI(r.URL.Path):
			api.ServeHTTP(w, r)
		case r.URL.Path == "/":
			http.Redirect(w, r, "/ui/", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
	// Browsers send the session cookie with any request to this site, so a state-changing
	// request a browser marks as coming from another origin is refused. Callers that are
	// not browsers send neither Origin nor Sec-Fetch-Site and pass, as do safe methods
	// such as the tunnel's WebSocket GET and the OIDC callback.
	csrf := http.NewCrossOriginProtection()
	// Webhooks carry their own secret, not the session, so any origin may call them.
	csrf.AddInsecureBypassPattern("POST /hooks/{id}/{automation}")
	if s.cfg.PublicURL != "" {
		// A proxy that rewrites Host would otherwise make the app's own origin look foreign.
		u, err := url.Parse(s.cfg.PublicURL)
		if err != nil {
			return nil, fmt.Errorf("public URL: %w", err)
		}
		if err := csrf.AddTrustedOrigin(u.Scheme + "://" + u.Host); err != nil {
			return nil, fmt.Errorf("public URL: %w", err)
		}
	}
	return s.withClientIP(csrf.Handler(s.authenticate(mux))), nil
}

// frameSource is a CSP host-source: an optional http(s) scheme, a host that may start
// with "*.", and an optional port.
var frameSource = regexp.MustCompile(`^(https?://)?(\*\.)?[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*(:([0-9]{1,5}|\*))?$`)

// frameHeaders checks the frame-ancestors sources and returns the headers that carry them.
func frameHeaders(sources []string) (map[string]string, error) {
	if len(sources) == 0 {
		sources = []string{"'self'"}
	}
	if len(sources) == 1 && sources[0] == "*" {
		return nil, nil
	}
	for _, v := range sources {
		alone := v == "'none'" || v == "*"
		if (alone && len(sources) > 1) || !(alone || v == "'self'" || frameSource.MatchString(v)) {
			return nil, fmt.Errorf("frame ancestor %q: use 'self', 'none', * alone, or an http(s) origin such as https://*.example.com", v)
		}
	}
	h := map[string]string{"Content-Security-Policy": "frame-ancestors " + strings.Join(sources, " ")}
	// Old browsers know only X-Frame-Options, which cannot name other sites; it is sent
	// for the default alone, so it never contradicts the policy.
	if len(sources) == 1 && sources[0] == "'self'" {
		h["X-Frame-Options"] = "SAMEORIGIN"
	}
	return h, nil
}

const (
	// The largest webhook body passed on to a host; the host takes no more.
	maxHookBody = 1 << 20
	// Where a webhook caller's Bearer token travels to the host; see host.serveHook.
	hookTokenHeader = "X-Inaba-Hook-Token"
)

// Paths that work without signing in. Everything else under an API prefix needs it;
// the web UI's own files do not, since the sign-in page is one of them.
var public = []string{
	"/v1/config", "/v1/auth/login", "/v1/auth/onboard",
	inabav1connect.AuthServiceGetConfigProcedure, inabav1connect.AuthServiceLoginProcedure, inabav1connect.AuthServiceOnboardProcedure,
	// A machine signing in as a host has no account; it holds a device code or a refresh token.
	"/v1/host-logins", "/v1/host-logins/poll", "/v1/host-tokens",
	inabav1connect.HostServiceStartHostLoginProcedure, inabav1connect.HostServicePollHostLoginProcedure, inabav1connect.HostServiceRefreshHostTokenProcedure,
	// Signing in with a passkey starts without an account.
	"/v1/auth/passkey/begin", "/v1/auth/passkey",
	inabav1connect.AuthServiceBeginPasskeyLoginProcedure, inabav1connect.AuthServiceFinishPasskeyLoginProcedure,
}

// isAPI reports whether a path belongs to the server's own API, REST or Connect.
func isAPI(path string) bool {
	return strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/inaba.v1.")
}

type ctxKey struct{}

type trustedKey struct{}

// trust marks every call on a server as coming from the local control socket, whose
// caller is an administrator by virtue of having opened it.
func trust(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
		return next(context.WithValue(ctx, trustedKey{}, true), spec, stream)
	}
}

// require fails unless the caller may do what the permission covers. A call that is
// neither from a signed-in account nor from the control socket is refused.
func require(ctx context.Context, permission string) error {
	if ctx.Value(trustedKey{}) != nil {
		return nil
	}
	if p := currentUser(ctx); p != nil && p.user.Can(permission) {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, "an administrator has to allow your account to manage "+permission)
}

func isAdmin(ctx context.Context) bool {
	p := currentUser(ctx)
	return p != nil && p.user != nil && p.user.Admin
}

func requireAdmin(ctx context.Context) error {
	if ctx.Value(trustedKey{}) != nil {
		return nil
	}
	if p := currentUser(ctx); p != nil && p.user.Admin {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, "only an administrator can manage accounts")
}

type principal struct {
	user  *store.User
	token *store.Token
}

func currentUser(ctx context.Context) *principal {
	p, _ := ctx.Value(ctxKey{}).(*principal)
	return p
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !(isAPI(p) || strings.HasPrefix(p, "/hosts/")) || slices.Contains(public, p) {
			next.ServeHTTP(w, r)
			return
		}
		secret, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !bearer {
			if c, err := r.Cookie(sessionCookie); err == nil {
				secret = c.Value
			}
		}
		// Any failure to find the token or its user reads as not signed in.
		t, err := s.store.LookupToken(secret)
		var u *store.User
		// A host's tokens stand for the host, never for a user.
		if err == nil && (t.Kind == store.KindSession || t.Kind == store.KindAPI) {
			u, _ = s.store.User(t.Owner)
		}
		if u == nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in, or send an API token as a Bearer token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, &principal{user: u, token: t})))
	})
}

// writeError answers in the JSON shape Connect clients read errors from.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// startSession signs a user in and returns the cookie that carries the session.
// startSession signs a user in from the browser named by agent and returns the cookie.
func (s *Server) startSession(u *store.User, agent string) (*http.Cookie, error) {
	if len(agent) > 256 {
		agent = agent[:256]
	}
	_, secret, err := s.store.NewToken(store.KindSession, u.ID, agent, sessionTTL)
	if err != nil {
		return nil, err
	}
	return &http.Cookie{
		Name: sessionCookie, Value: secret, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: strings.HasPrefix(s.cfg.PublicURL, "https://"),
	}, nil
}

func (s *Server) GetConfig(context.Context, *inabav1.GetConfigRequest) (*inabav1.GetConfigResponse, error) {
	s.mu.Lock()
	onboarding := s.onboardToken != ""
	s.mu.Unlock()
	if onboarding {
		// An account made from the CLI in the meantime ends setup too.
		users, err := s.store.Users()
		onboarding = err == nil && len(users) == 0
	}
	return &inabav1.GetConfigResponse{
		OidcEnabled: s.oidc != nil, OidcName: s.cfg.OIDC.Name, IceUrls: s.cfg.ICEURLs, NeedsOnboarding: onboarding,
	}, nil
}

func (s *Server) Onboard(ctx context.Context, req *inabav1.OnboardRequest) (*inabav1.User, error) {
	if req.Username == "" || len(req.Password) < 8 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "choose a username and a password of at least 8 characters")
	}
	// Held across the whole check-and-create, so two requests cannot both be first.
	s.mu.Lock()
	defer s.mu.Unlock()
	users, err := s.store.Users()
	if err != nil {
		return nil, internal("could not check the accounts", err)
	}
	if s.onboardToken == "" || len(users) > 0 || subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.onboardToken)) != 1 {
		return nil, connect.NewError(connect.CodePermissionDenied, "this setup link is not valid any more; sign in, or restart the server for a new link")
	}
	u, err := s.store.AddLocalUser(req.Username, req.Password, true, nil)
	if err != nil {
		return nil, internal("could not save the account", err)
	}
	s.onboardToken = ""
	cookie, err := s.startSession(u, "")
	if err != nil {
		return nil, internal("could not save the session", err)
	}
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		info.ResponseHeader().Add("Set-Cookie", cookie.String())
	}
	return userMessage(u), nil
}

func (s *Server) Login(ctx context.Context, req *inabav1.LoginRequest) (*inabav1.User, error) {
	// One check covers both steps, so each guess at the password or the code costs one try.
	if err := s.allowGuess(ctx, req.Username); err != nil {
		return nil, err
	}
	u, err := s.store.LocalUser(req.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, internal("could not check the account", err)
	}
	// Compare against a dummy hash for unknown names, so timing does not reveal them.
	hash := dummyHash
	if u != nil {
		hash = []byte(u.PasswordHash)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) != nil || u == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "wrong username or password")
	}
	// The second step, for an account that has it: asked for only once the password is right.
	if u.TOTPSecret != "" {
		if req.TotpCode == "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition, "enter the code from your authenticator app")
		}
		if !totp.Validate(strings.TrimSpace(req.TotpCode), u.TOTPSecret) {
			return nil, connect.NewError(connect.CodeUnauthenticated, "that code does not match")
		}
	}
	s.guessed(req.Username)
	return s.signIn(ctx, u)
}

// internal is an error the caller cannot fix; the cause stays in the server.
func internal(message string, cause error) error {
	return connect.NewError(connect.CodeInternal, message).WithCause(cause)
}

func (s *Server) Logout(ctx context.Context, _ *inabav1.LogoutRequest) (*inabav1.LogoutResponse, error) {
	p := currentUser(ctx)
	if p.token.Kind == store.KindSession {
		if err := s.deleteToken(p.token.ID, p.user.ID); err != nil {
			return nil, err
		}
	}
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		info.ResponseHeader().Add("Set-Cookie", (&http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1}).String())
	}
	return &inabav1.LogoutResponse{}, nil
}

func (s *Server) WhoAmI(ctx context.Context, _ *inabav1.WhoAmIRequest) (*inabav1.User, error) {
	return userMessage(currentUser(ctx).user), nil
}

func tokenMessage(t *store.Token) *inabav1.Token {
	return &inabav1.Token{Id: t.ID, Name: t.Name, CreatedAt: timestamppb.New(t.CreatedAt)}
}

func (s *Server) CreateToken(ctx context.Context, req *inabav1.CreateTokenRequest) (*inabav1.CreateTokenResponse, error) {
	t, secret, err := s.store.NewToken(store.KindAPI, currentUser(ctx).user.ID, req.Name, 0)
	if err != nil {
		return nil, internal("could not save the token", err)
	}
	return &inabav1.CreateTokenResponse{Token: tokenMessage(t), Secret: secret}, nil
}

func (s *Server) ListTokens(ctx context.Context, _ *inabav1.ListTokensRequest) (*inabav1.ListTokensResponse, error) {
	tokens, err := s.store.Tokens(store.KindAPI, currentUser(ctx).user.ID)
	if err != nil {
		return nil, internal("could not read the tokens", err)
	}
	res := &inabav1.ListTokensResponse{}
	for i := range tokens {
		res.Tokens = append(res.Tokens, tokenMessage(&tokens[i]))
	}
	return res, nil
}

func (s *Server) RevokeToken(ctx context.Context, req *inabav1.RevokeTokenRequest) (*inabav1.RevokeTokenResponse, error) {
	return &inabav1.RevokeTokenResponse{}, s.deleteToken(req.Id, currentUser(ctx).user.ID)
}

// deleteToken removes a token of the given owner.
func (s *Server) deleteToken(id, owner string) error {
	err := s.store.DeleteToken(id, owner)
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, "no token "+id)
	}
	if err != nil {
		return internal("could not save the change", err)
	}
	return nil
}

// Every signed-in user can reach every shared host; a personal host only its owner.

func (s *Server) hostMessage(h *store.Host) *inabav1.Host {
	s.mu.Lock()
	_, online := s.links[h.ID]
	s.mu.Unlock()
	m := &inabav1.Host{Id: h.ID, Name: h.Name, Online: online, CreatedAt: timestamppb.New(h.CreatedAt), Shared: h.Owner == "", Os: h.OS, Arch: h.Arch, Icon: h.Icon, Image: h.Image}
	if h.LastSeen != nil {
		m.LastSeen = timestamppb.New(*h.LastSeen)
	}
	if len(h.System) > 0 {
		system := &inabav1.SystemInfo{}
		if proto.Unmarshal(h.System, system) == nil {
			m.System = system
		}
	}
	return m
}

var validPlatform = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// userID is the signed-in caller's id, "" for the control socket or a caller without an account.
func userID(ctx context.Context) string {
	if p := currentUser(ctx); p != nil && p.user != nil {
		return p.user.ID
	}
	return ""
}

func (s *Server) CreateHost(ctx context.Context, req *inabav1.CreateHostRequest) (*inabav1.CreateHostResponse, error) {
	owner := ""
	if req.Shared {
		if err := require(ctx, store.PermSharedHosts); err != nil {
			return nil, err
		}
	} else if owner = userID(ctx); owner == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a personal host belongs to an account; register a shared one here instead")
	}
	if req.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	h, secret, err := s.store.AddHost(req.Name, owner)
	if err != nil {
		return nil, internal("could not save the host", err)
	}
	return &inabav1.CreateHostResponse{Host: s.hostMessage(h), Secret: secret}, nil
}

func (s *Server) ListHosts(ctx context.Context, req *inabav1.ListHostsRequest) (*inabav1.ListHostsResponse, error) {
	if req.GetAll() {
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
	}
	hosts, err := s.store.HostsFor(userID(ctx))
	if ctx.Value(trustedKey{}) != nil || req.GetAll() {
		// The control socket answers to whoever runs the server, who sees every host; so does the admin panel.
		hosts, err = s.store.Hosts()
	}
	if err != nil {
		return nil, internal("could not read the hosts", err)
	}
	res := &inabav1.ListHostsResponse{}
	for i := range hosts {
		res.Hosts = append(res.Hosts, s.hostFor(ctx, &hosts[i]))
	}
	return res, nil
}

// reachable returns a host the caller may reach; one they cannot is answered as missing, so its
// id gives nothing away.
func (s *Server) reachable(ctx context.Context, id string) (*store.Host, error) {
	h, err := s.store.Host(id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && s.placeOf(ctx, h) == "") {
		return nil, connect.NewError(connect.CodeNotFound, "no host "+id)
	}
	if err != nil {
		return nil, internal("could not read the host", err)
	}
	return h, nil
}

// manageable returns a host whose look the caller may change: anyone who may change its settings.
func (s *Server) manageable(ctx context.Context, id string) (*store.Host, error) {
	h, err := s.reachable(ctx, id)
	if err != nil {
		return nil, err
	}
	if p := s.placeOf(ctx, h); p != placeOwner && p != placeAdmin && !slices.Contains(s.hostGrants(ctx, h, currentUser(ctx).user), store.PermHostSettings) {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may not change this host's settings")
	}
	return h, nil
}

// removable returns a host the caller may rename or remove: their own, any as an administrator,
// or a shared one with the shared-hosts permission. A member or manager of someone's host works
// in it but does not get to take it away from its owner.
func (s *Server) removable(ctx context.Context, id string) (*store.Host, error) {
	h, err := s.reachable(ctx, id)
	if err != nil {
		return nil, err
	}
	if p := s.placeOf(ctx, h); p == placeOwner || p == placeAdmin {
		return h, nil
	}
	if h.Owner == "" {
		if err := require(ctx, store.PermSharedHosts); err != nil {
			return nil, err
		}
		return h, nil
	}
	return nil, connect.NewError(connect.CodePermissionDenied, "only the host's owner renames or removes it")
}

var (
	validIcon  = regexp.MustCompile(`^[a-z0-9-]{0,32}$`)
	validImage = regexp.MustCompile(`^data:image/(png|jpeg|webp);base64,[A-Za-z0-9+/=]+$`)
)

const maxHostImage = 256 << 10

func (s *Server) UpdateHost(ctx context.Context, req *inabav1.UpdateHostRequest) (*inabav1.Host, error) {
	changes := map[string]any{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > 64 {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a name is 1 to 64 characters")
		}
		changes["name"] = name
	}
	if req.Icon != nil {
		if !validIcon.MatchString(*req.Icon) {
			return nil, connect.NewError(connect.CodeInvalidArgument, "an icon is named in lowercase letters, digits and -")
		}
		changes["icon"] = *req.Icon
	}
	if req.Image != nil {
		// Only a picture, and a small one: it is shown to everyone the host is shown to.
		if *req.Image != "" && (len(*req.Image) > maxHostImage*4/3+64 || !validImage.MatchString(*req.Image)) {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a picture is a PNG, JPEG or WebP image of at most 256 KiB")
		}
		changes["image"] = *req.Image
	}
	find := s.manageable
	if req.Name != nil || req.Restricted != nil {
		find = s.removable
	}
	h, err := find(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if req.Restricted != nil {
		if !isAdmin(ctx) && ctx.Value(trustedKey{}) == nil {
			return nil, connect.NewError(connect.CodePermissionDenied, "only an administrator keeps a shared host to its members")
		}
		if h.Owner != "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a personal host is kept to its members already")
		}
		changes["restricted"] = *req.Restricted
	}
	if len(changes) > 0 {
		if err := s.store.UpdateHost(h.ID, changes); err != nil {
			return nil, internal("could not save the change", err)
		}
		if h, err = s.store.Host(h.ID); err != nil {
			return nil, internal("could not read the host", err)
		}
	}
	return s.hostMessage(h), nil
}

func (s *Server) DeleteHost(ctx context.Context, req *inabav1.DeleteHostRequest) (*inabav1.DeleteHostResponse, error) {
	if _, err := s.removable(ctx, req.Id); err != nil {
		return nil, err
	}
	err := s.store.DeleteHost(req.Id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "no host "+req.Id)
	}
	if err != nil {
		return nil, internal("could not save the change", err)
	}
	s.mu.Lock()
	l := s.links[req.Id]
	delete(s.links, req.Id)
	s.mu.Unlock()
	if l != nil {
		_ = l.session.Close()
	}
	return &inabav1.DeleteHostResponse{}, nil
}

// serveTunnel is where a host connects. It holds the connection until it drops.
func (s *Server) serveTunnel(w http.ResponseWriter, r *http.Request) {
	secret, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	t, err := s.store.LookupToken(secret)
	if err != nil || (t.Kind != store.KindHost && t.Kind != store.KindHostAccess) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "unknown host secret")
		return
	}
	hostID := t.Owner
	host, err := s.store.Host(hostID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "unknown host")
		return
	}
	// What the host runs on, kept for the web UI's icons; anything odd is left out.
	if os, arch, ok := strings.Cut(r.Header.Get(tunnel.PlatformHeader), "/"); ok && validPlatform.MatchString(os) && validPlatform.MatchString(arch) {
		host.OS, host.Arch = os, arch
		if err := s.store.SetHostPlatform(hostID, os, arch); err != nil {
			slog.Warn("could not record the host's platform", "host", hostID, "error", err)
		}
	}
	session, err := tunnel.Accept(w, r)
	if err != nil {
		return
	}
	l := &link{session: session, owner: host.Owner, proxy: &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "host"
			// serveHost already set the path the host should see on the incoming request.
			pr.Out.URL.Path, pr.Out.URL.RawPath = pr.In.URL.Path, ""
			// The host trusts the server's check; the user's credentials stay here.
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			// The host enforces these. Set, never passed through, so a caller cannot claim more.
			// A webhook call has no account behind it, and so no permissions.
			var granted []string
			var user string
			if p := currentUser(pr.In.Context()); p != nil && p.user != nil {
				user = p.user.ID
				// As the host stands now, not as it was when it connected: roles and restrictions change.
				if h, err := s.store.Host(hostID); err == nil {
					granted = s.grantsOn(pr.In.Context(), h, p.user)
				}
			}
			pr.Out.Header.Set(rpc.PermissionsHeader, strings.Join(granted, ","))
			pr.Out.Header.Set(rpc.UserHeader, user)
		},
		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) { return session.Open() },
		},
		// Streams must reach the client as they are produced.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeError(w, http.StatusBadGateway, "unavailable", "the host did not answer")
		},
	}}
	s.mu.Lock()
	old := s.links[hostID]
	s.links[hostID] = l
	s.mu.Unlock()
	if old != nil {
		_ = old.session.Close()
	}
	slog.Info("host connected", "host", hostID)
	go s.recordSystem(hostID, l)
	go s.watchAlerts(hostID, l)
	<-session.CloseChan()
	if err := s.store.SawHost(hostID, nil); err != nil {
		slog.Warn("could not record when the host left", "host", hostID, "error", err)
	}
	s.mu.Lock()
	if s.links[hostID] == l {
		delete(s.links, hostID)
	}
	s.mu.Unlock()
	slog.Info("host disconnected", "host", hostID)
}

// serveHost forwards a request to the host named in its path, as hostPath on the host.
func (s *Server) serveHost(w http.ResponseWriter, r *http.Request, hostPath string) {
	// The alert stream is the server's own line to the host; a browser has no business on it.
	if strings.Contains(hostPath, inabav1connect.AlertServiceName) {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	l := s.links[r.PathValue("id")]
	s.mu.Unlock()
	// Someone else's personal host is answered as missing. A webhook call, which has
	// no account, is let through: the host checks the automation's own secret.
	h, err := s.store.Host(r.PathValue("id"))
	if err == nil && !strings.HasPrefix(hostPath, "/hooks/") && s.placeOf(r.Context(), h) == "" {
		writeError(w, http.StatusNotFound, "not_found", "no host "+r.PathValue("id"))
		return
	}
	if l == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the host is not connected")
		return
	}
	r = r.Clone(r.Context())
	r.URL.Path, r.URL.RawPath = hostPath, ""
	l.proxy.ServeHTTP(w, r)
}

func serveYAML(doc []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(doc)
	}
}
