package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alex4386/inaba/internal/store"
	"github.com/Alex4386/inaba/internal/testdir"
)

// mockIdP is a small OIDC provider: discovery, keys, an authorize endpoint that approves
// at once, and a token endpoint that checks the client and PKCE before signing an ID token.
// shortcut: hand-rolled because no maintained mock lets each test forge a wrong issuer,
// audience or nonce; it signs RS256 only.
type mockIdP struct {
	*httptest.Server
	key *rsa.PrivateKey
	// secret is the client secret the token endpoint wants; "" for a public client.
	secret string

	mu sync.Mutex
	// claims go into the next ID token, over the defaults.
	claims map[string]any
	// codes are the outstanding authorization requests, by code.
	codes map[string]url.Values
}

var testKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

func newMockIdP(t *testing.T, secret string) *mockIdP {
	t.Helper()
	m := &mockIdP{key: testKey(), secret: secret, codes: map[string]url.Values{}, claims: map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": m.URL, "authorization_endpoint": m.URL + "/authorize", "token_endpoint": m.URL + "/token",
			"jwks_uri": m.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k", "alg": "RS256", "use": "sig",
			"n": b64(m.key.N.Bytes()), "e": b64(big.NewInt(int64(m.key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := "code-" + q.Get("state")
		m.mu.Lock()
		m.codes[code] = q
		m.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /token", m.token)
	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Close)
	return m
}

func (m *mockIdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	m.mu.Lock()
	req, ok := m.codes[r.PostForm.Get("code")]
	delete(m.codes, r.PostForm.Get("code"))
	claims := map[string]any{"iss": m.URL, "aud": "inaba", "sub": "alice", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
	for k, v := range m.claims {
		claims[k] = v
	}
	m.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || id != "inaba" || secret != m.secret || req.Get("code_challenge_method") != "S256" || b64(sum[:]) != req.Get("code_challenge") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	if _, set := claims["nonce"]; !set {
		claims["nonce"] = req.Get("nonce")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": m.sign(claims)})
}

func (m *mockIdP) sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	input := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return input + "." + b64(sig)
}

func (m *mockIdP) set(claims map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claims = claims
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// newOIDCServer starts the server's full handler signing in through idp.
func newOIDCServer(t *testing.T, idp *mockIdP, cfg OIDCConfig) (*Server, *httptest.Server) {
	t.Helper()
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	cfg.Issuer, cfg.ClientID, cfg.ClientSecret = idp.URL, "inaba", idp.secret
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "state.db"), PublicURL: srv.URL, OIDC: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if handler, err = s.Handler(); err != nil {
		t.Fatal(err)
	}
	return s, srv
}

// browser is a client that keeps cookies and lets the test follow each redirect.
func browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func get(t *testing.T, c *http.Client, u string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	// The IdP sends the browser back with a top-level cross-site navigation.
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

// startLogin runs the flow up to the callback URL the IdP sends the browser back to.
func startLogin(t *testing.T, c *http.Client, srv *httptest.Server) (authorize *url.URL, callback string) {
	t.Helper()
	res := get(t, c, srv.URL+"/auth/oidc/login")
	authorize, err := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusFound || err != nil {
		t.Fatalf("login: %d %v", res.StatusCode, err)
	}
	res = get(t, c, authorize.String())
	return authorize, res.Header.Get("Location")
}

// signInOIDC runs the whole flow and returns the callback's answer.
func signInOIDC(t *testing.T, srv *httptest.Server) *http.Response {
	t.Helper()
	c := browser()
	_, callback := startLogin(t, c, srv)
	return get(t, c, callback)
}

func signedInOK(res *http.Response) bool {
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/ui/" {
		return false
	}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return true
		}
	}
	return false
}

func TestOIDCConfidentialAndPublicClients(t *testing.T) {
	for _, secret := range []string{"s3cret", ""} {
		idp := newMockIdP(t, secret)
		s, srv := newOIDCServer(t, idp, OIDCConfig{})
		for i := range 2 {
			if res := signInOIDC(t, srv); !signedInOK(res) {
				t.Fatalf("secret %q, sign-in %d: %d %s", secret, i, res.StatusCode, res.Header.Get("Location"))
			}
		}
		// The second sign-in found the account the first one made.
		if users, err := s.store.Users(); err != nil || len(users) != 1 {
			t.Fatalf("secret %q: %d accounts, %v", secret, len(users), err)
		}
	}
}

func TestOIDCRefusesTamperedFlows(t *testing.T) {
	idp := newMockIdP(t, "s3cret")
	_, srv := newOIDCServer(t, idp, OIDCConfig{})
	cases := map[string]func(c *http.Client, callback string) string{
		"state mismatch": func(_ *http.Client, callback string) string {
			return strings.Replace(callback, "state=", "state=x", 1)
		},
		"missing cookie": func(c *http.Client, callback string) string {
			c.Jar, _ = cookiejar.New(nil)
			return callback
		},
		"wrong PKCE verifier": func(c *http.Client, callback string) string {
			u, _ := url.Parse(srv.URL + "/auth/oidc")
			for _, k := range c.Jar.Cookies(u) {
				if k.Name == oidcStateCookie {
					p := strings.Split(k.Value, ".")
					p[1] = "wrong-verifier-wrong-verifier-wrong-verifier-x"
					c.Jar.SetCookies(u, []*http.Cookie{{Name: oidcStateCookie, Value: strings.Join(p, "."), Path: "/auth/oidc"}})
				}
			}
			return callback
		},
	}
	for name, tamper := range cases {
		c := browser()
		_, callback := startLogin(t, c, srv)
		want := http.StatusBadRequest
		if name == "wrong PKCE verifier" {
			want = http.StatusBadGateway
		}
		if res := get(t, c, tamper(c, callback)); signedInOK(res) || res.StatusCode != want {
			t.Errorf("%s: %d, want %d", name, res.StatusCode, want)
		}
	}
}

func TestOIDCRefusesForgedIDTokens(t *testing.T) {
	idp := newMockIdP(t, "s3cret")
	_, srv := newOIDCServer(t, idp, OIDCConfig{})
	for name, claims := range map[string]map[string]any{
		"wrong issuer":   {"iss": "https://elsewhere.example"},
		"wrong audience": {"aud": "someone-else"},
		"wrong nonce":    {"nonce": "replayed"},
		"no nonce":       {"nonce": ""},
	} {
		idp.set(claims)
		if res := signInOIDC(t, srv); signedInOK(res) || res.StatusCode != http.StatusBadGateway {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
}

func TestOIDCAccessRules(t *testing.T) {
	ann := map[string]any{"email": "Ann@Example.com", "email_verified": true}
	with := func(base map[string]any, kv ...any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	cases := []struct {
		name   string
		cfg    OIDCConfig
		claims map[string]any
		ok     bool
	}{
		{"no rules", OIDCConfig{}, map[string]any{}, true},
		{"domain", OIDCConfig{AllowedDomains: []string{"example.com"}}, ann, true},
		{"other domain", OIDCConfig{AllowedDomains: []string{"example.org"}}, ann, false},
		{"lookalike domain", OIDCConfig{AllowedDomains: []string{"example.com"}}, with(ann, "email", "ann@evil-example.com"), false},
		{"hd matches", OIDCConfig{AllowedDomains: []string{"example.com"}}, with(ann, "hd", "example.com"), true},
		{"hd differs", OIDCConfig{AllowedDomains: []string{"example.com"}}, with(ann, "hd", "other.com"), false},
		{"user", OIDCConfig{AllowedUsers: []string{"ann@example.com"}}, ann, true},
		{"other user", OIDCConfig{AllowedUsers: []string{"bob@example.com"}}, ann, false},
		{"unverified", OIDCConfig{AllowedUsers: []string{"ann@example.com"}}, with(ann, "email_verified", false), false},
		{"verified missing", OIDCConfig{AllowedUsers: []string{"ann@example.com"}}, map[string]any{"email": "ann@example.com"}, false},
		{"verified as a string", OIDCConfig{AllowedUsers: []string{"ann@example.com"}}, with(ann, "email_verified", "true"), true},
		{"unverified allowed", OIDCConfig{AllowedUsers: []string{"ann@example.com"}, AllowUnverifiedEmail: true}, with(ann, "email_verified", false), true},
		{"no email", OIDCConfig{AllowedDomains: []string{"example.com"}}, map[string]any{"email_verified": true}, false},
		{"group array", OIDCConfig{AllowedGroups: []string{"dev"}}, map[string]any{"groups": []string{"ops", "dev"}}, true},
		{"group string", OIDCConfig{AllowedGroups: []string{"dev"}}, map[string]any{"groups": "dev"}, true},
		{"no group", OIDCConfig{AllowedGroups: []string{"dev"}}, map[string]any{"groups": []string{"ops"}}, false},
		{"custom claim", OIDCConfig{AllowedGroups: []string{"dev"}, GroupsClaim: "roles"}, map[string]any{"roles": []string{"dev"}, "groups": []string{"x"}}, true},
		{"all rules pass", OIDCConfig{AllowedDomains: []string{"example.com"}, AllowedUsers: []string{"ann@example.com"}, AllowedGroups: []string{"dev"}}, with(ann, "groups", "dev"), true},
		{"one rule fails", OIDCConfig{AllowedDomains: []string{"example.com"}, AllowedUsers: []string{"ann@example.com"}, AllowedGroups: []string{"dev"}}, with(ann, "groups", "ops"), false},
	}
	for _, tc := range cases {
		idp := newMockIdP(t, "")
		idp.set(tc.claims)
		_, srv := newOIDCServer(t, idp, tc.cfg)
		res := signInOIDC(t, srv)
		if signedInOK(res) != tc.ok {
			t.Errorf("%s: status %d, want signed in %v", tc.name, res.StatusCode, tc.ok)
		}
		if !tc.ok && res.StatusCode != http.StatusForbidden {
			t.Errorf("%s: refused with %d", tc.name, res.StatusCode)
		}
	}
}

func TestOIDCRulesApplyToExistingAccounts(t *testing.T) {
	idp := newMockIdP(t, "")
	idp.set(map[string]any{"groups": "dev"})
	_, srv := newOIDCServer(t, idp, OIDCConfig{AllowedGroups: []string{"dev"}})
	if !signedInOK(signInOIDC(t, srv)) {
		t.Fatal("first sign-in refused")
	}
	idp.set(map[string]any{"groups": "former"})
	if signedInOK(signInOIDC(t, srv)) {
		t.Fatal("an account that left the group signed in again")
	}
}

func TestOIDCAdminGroups(t *testing.T) {
	idp := newMockIdP(t, "")
	s, srv := newOIDCServer(t, idp, OIDCConfig{AdminGroups: []string{"admins"}})
	account := func() *store.User {
		users, err := s.store.Users()
		for i := range users {
			if users[i].OIDCSubject != "" {
				return &users[i]
			}
		}
		t.Fatalf("no OIDC account: %v", err)
		return nil
	}
	idp.set(map[string]any{"groups": []string{"admins"}})
	if !signedInOK(signInOIDC(t, srv)) || !account().Admin {
		t.Fatal("an admin-group member was not made administrator")
	}
	// The last administrator keeps the role after leaving the group.
	idp.set(map[string]any{"groups": []string{}})
	if !signedInOK(signInOIDC(t, srv)) || !account().Admin {
		t.Fatal("the last administrator was demoted")
	}
	if _, err := s.store.AddLocalUser("root", "password", true, nil); err != nil {
		t.Fatal(err)
	}
	if !signedInOK(signInOIDC(t, srv)) || account().Admin {
		t.Fatal("an account that left the admin groups stayed administrator")
	}
}

func TestOIDCExtraParams(t *testing.T) {
	idp := newMockIdP(t, "")
	_, srv := newOIDCServer(t, idp, OIDCConfig{ExtraParams: map[string]string{"hd": "example.com", "prompt": "select_account"}, Scopes: []string{"email", "groups"}})
	authorize, _ := startLogin(t, browser(), srv)
	q := authorize.Query()
	if q.Get("hd") != "example.com" || q.Get("prompt") != "select_account" || q.Get("nonce") == "" || q.Get("scope") != "openid email groups" {
		t.Fatalf("authorize URL: %s", authorize)
	}
	for _, key := range []string{"state", "Redirect_URI", "nonce", "code_challenge", "scope", "client_id", "response_type", "code_challenge_method"} {
		_, err := New(context.Background(), Config{
			DataPath: filepath.Join(testdir.New(t), "state.db"), PublicURL: "https://inaba.example",
			OIDC: OIDCConfig{Issuer: idp.URL, ClientID: "inaba", ExtraParams: map[string]string{key: "x"}},
		})
		if err == nil {
			t.Errorf("extra parameter %s accepted", key)
		}
	}
}
