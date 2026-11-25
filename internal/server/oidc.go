package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Alex4386/inaba/internal/store"
)

type OIDCConfig struct {
	// Issuer enables OIDC sign-in when set.
	Issuer   string
	ClientID string
	// ClientSecret is empty for a public client, which then relies on PKCE alone.
	ClientSecret string
	// Name is what the sign-in button calls the provider.
	Name string
	// Scopes asked for; empty means openid, profile and email. openid is always added.
	// A provider that sends groups only on request needs its groups scope listed here.
	Scopes []string
	// ExtraParams are added to the provider's sign-in URL, such as hd, prompt, login_hint
	// or domain_hint. Parameters the flow sets itself are refused.
	ExtraParams map[string]string

	// The access rules below must all pass, on every sign-in, new account or not.
	// An empty list is no rule.

	// AllowedDomains are the email domains that may sign in. A Google "hd" claim, when
	// present, must be one of them too.
	AllowedDomains []string
	// AllowedUsers are the email addresses that may sign in, in any case.
	AllowedUsers []string
	// AllowedGroups lets in members of any of these groups.
	AllowedGroups []string
	// GroupsClaim names the claim listing the user's groups, a string or an array;
	// empty means "groups".
	GroupsClaim string
	// AllowUnverifiedEmail lets an email rule match an address the provider has not
	// marked verified. Off by default, since an unverified address can be anyone's.
	AllowUnverifiedEmail bool
	// AdminGroups makes members of any of these groups administrators. When set, the
	// provider decides: each sign-in also takes administrator away from an OIDC account
	// that is no longer a member, unless it is the last administrator.
	AdminGroups []string
}

// reservedParams are set by the flow itself; an extra parameter must not replace them.
var reservedParams = []string{"state", "nonce", "code_challenge", "code_challenge_method", "redirect_uri", "client_id", "response_type", "scope"}

func (c OIDCConfig) validate() error {
	for k := range c.ExtraParams {
		if k == "" || slices.Contains(reservedParams, strings.ToLower(k)) {
			return fmt.Errorf("OIDC extra parameter %q is set by the sign-in flow itself", k)
		}
	}
	return nil
}

func (c OIDCConfig) hasRules() bool {
	return len(c.AllowedDomains)+len(c.AllowedUsers)+len(c.AllowedGroups) > 0
}

const oidcStateCookie = "inaba_oidc"

type oidcFlow struct {
	cfg      OIDCConfig
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	secure   bool
}

func newOIDCFlow(ctx context.Context, cfg Config) (*oidcFlow, error) {
	if cfg.PublicURL == "" {
		return nil, fmt.Errorf("OIDC sign-in needs the public URL, for its redirect address")
	}
	if err := cfg.OIDC.validate(); err != nil {
		return nil, err
	}
	provider, err := oidc.NewProvider(ctx, cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC issuer %s: %w", cfg.OIDC.Issuer, err)
	}
	scopes := cfg.OIDC.Scopes
	if len(scopes) == 0 {
		scopes = []string{"profile", "email"}
	}
	if !slices.Contains(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	if !cfg.OIDC.hasRules() {
		slog.Warn("OIDC sign-in has no allowed_domains, allowed_users or allowed_groups: any account the identity provider accepts may sign in")
	}
	return &oidcFlow{
		cfg: cfg.OIDC,
		oauth: oauth2.Config{
			ClientID: cfg.OIDC.ClientID, ClientSecret: cfg.OIDC.ClientSecret,
			Endpoint:    provider.Endpoint(),
			RedirectURL: strings.TrimSuffix(cfg.PublicURL, "/") + "/auth/oidc/callback",
			Scopes:      scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.OIDC.ClientID}),
		secure:   strings.HasPrefix(cfg.PublicURL, "https://"),
	}, nil
}

func (f *oidcFlow) login(w http.ResponseWriter, r *http.Request) {
	state, pkce, nonce := oauth2.GenerateVerifier(), oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	// The cookie ties the callback to the browser that started the sign-in.
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: state + "." + pkce + "." + nonce, Path: "/auth/oidc", MaxAge: 600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: f.secure,
	})
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(pkce), oidc.Nonce(nonce)}
	for k, v := range f.cfg.ExtraParams {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	http.Redirect(w, r, f.oauth.AuthCodeURL(state, opts...), http.StatusFound)
}

func (f *oidcFlow) callback(s *Server, w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(oidcStateCookie)
	parts := strings.Split(cookieValue(c), ".")
	if err != nil || len(parts) != 3 || parts[0] == "" || r.URL.Query().Get("state") != parts[0] {
		ssoFailed(w, http.StatusBadRequest, "expired")
		return
	}
	pkce, nonce := parts[1], parts[2]
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Path: "/auth/oidc", MaxAge: -1})

	tok, err := f.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(pkce))
	if err != nil {
		slog.Warn("OIDC code exchange failed", "error", err)
		ssoFailed(w, http.StatusBadGateway, "provider")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	idToken, err := f.verifier.Verify(r.Context(), rawID)
	if err == nil && idToken.Nonce != nonce {
		err = errors.New("nonce does not match")
	}
	if err != nil {
		slog.Warn("OIDC ID token refused", "error", err)
		ssoFailed(w, http.StatusBadGateway, "provider")
		return
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		ssoFailed(w, http.StatusBadGateway, "provider")
		return
	}
	subject := f.cfg.Issuer + " " + idToken.Subject
	groups := stringList(claims[cmp.Or(f.cfg.GroupsClaim, "groups")])
	if reason := f.refusal(claims, groups); reason != "" {
		// The reason stays here: telling the browser would tell a stranger which rule to get around.
		slog.Warn("OIDC sign-in refused", "subject", subject, "ip", callerIP(r.Context()), "reason", reason)
		ssoFailed(w, http.StatusForbidden, "refused")
		return
	}
	name, _ := claims["preferred_username"].(string)
	if name == "" {
		name, _ = claims["email"].(string)
	}
	if name == "" {
		name = idToken.Subject
	}

	u, err := s.store.OIDCUser(subject, name)
	if err == nil && len(f.cfg.AdminGroups) > 0 {
		u, err = f.syncAdmin(s, u, groups)
	}
	var cookie *http.Cookie
	if err == nil {
		cookie, err = s.startSession(u, r.UserAgent())
	}
	if err != nil {
		slog.Error("OIDC sign-in could not be saved", "subject", subject, "error", err)
		ssoFailed(w, http.StatusInternalServerError, "session")
		return
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

// refusal says which access rule the claims fail, "" when they pass them all.
func (f *oidcFlow) refusal(claims map[string]any, groups []string) string {
	email, _ := claims["email"].(string)
	email = strings.ToLower(email)
	if len(f.cfg.AllowedDomains) > 0 || len(f.cfg.AllowedUsers) > 0 {
		if email == "" {
			return "no email claim"
		}
		// Some providers send the flag as a string.
		if v := claims["email_verified"]; !f.cfg.AllowUnverifiedEmail && v != true && v != "true" {
			return "email not verified"
		}
	}
	if len(f.cfg.AllowedDomains) > 0 {
		_, domain, _ := strings.Cut(email, "@")
		if !containsFold(f.cfg.AllowedDomains, domain) {
			return "email domain not allowed"
		}
		if hd, ok := claims["hd"].(string); ok && !containsFold(f.cfg.AllowedDomains, hd) {
			return "hosted domain not allowed"
		}
	}
	if len(f.cfg.AllowedUsers) > 0 && !containsFold(f.cfg.AllowedUsers, email) {
		return "email not allowed"
	}
	if len(f.cfg.AllowedGroups) > 0 && !slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(f.cfg.AllowedGroups, g) }) {
		return "not in an allowed group"
	}
	return ""
}

// syncAdmin makes an account's administrator flag follow its admin-group membership.
func (f *oidcFlow) syncAdmin(s *Server, u *store.User, groups []string) (*store.User, error) {
	admin := slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(f.cfg.AdminGroups, g) })
	if u.Admin == admin {
		return u, nil
	}
	// What the account may do without administrator, kept across the change.
	plain := *u
	plain.Admin = false
	updated, err := s.store.SetAccess(u.ID, admin, plain.Granted())
	if errors.Is(err, store.ErrLastAdmin) {
		slog.Warn("OIDC account left the admin groups but stays administrator, as the last one", "user", u.ID)
		return u, nil
	}
	if err == nil {
		slog.Info("OIDC admin groups changed an account", "user", u.ID, "admin", admin)
	}
	return updated, err
}

// stringList reads a claim that is a string or an array of strings.
func stringList(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func containsFold(list []string, v string) bool {
	return slices.ContainsFunc(list, func(e string) bool { return strings.EqualFold(e, v) })
}

func cookieValue(c *http.Cookie) string {
	if c == nil {
		return ""
	}
	return c.Value
}

// ssoFailed sends the browser back to the sign-in page, which says what went wrong in the
// person's language; the status stays for scripts and logs. code is one of a fixed set
// ("expired", "provider", "refused", "session"), never text from the request.
func ssoFailed(w http.ResponseWriter, status int, code string) {
	back := "/ui/?sso_error=" + code
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><meta http-equiv="refresh" content="0;url=%s"><p>Sign-in did not finish. <a href="%s">Back to sign in</a></p>`, back, back)
}
