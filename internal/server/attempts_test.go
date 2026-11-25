package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/store"
	"github.com/Alex4386/inaba/internal/testdir"
)

func newHTTPServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "state.db")})
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return s, srv
}

func send(t *testing.T, method, url string, header map[string]string, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestLoginGuessesAreLimitedPerAccount(t *testing.T) {
	s, _ := newHTTPServer(t)
	if _, err := s.store.AddLocalUser("ann", "right-password", false, nil); err != nil {
		t.Fatal(err)
	}
	// Calls made directly carry no client address, so only the account's budget applies.
	// Refilling is stopped: bcrypt under -race takes long enough for the budget to refill mid-test.
	s.accountGuesses.every = time.Hour
	ctx := context.Background()
	login := func(name, password string) error {
		_, err := s.Login(ctx, &inabav1.LoginRequest{Username: name, Password: password})
		return err
	}
	// A success gives the budget back, so only failures in a row add up.
	for i := range 19 {
		if err := login("ann", "wrong"); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	if err := login("ann", "right-password"); err != nil {
		t.Fatalf("right password within budget: %v", err)
	}
	for i := range 20 {
		if err := login("ann", "wrong"); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("guess %d after a success: %v", i, err)
		}
	}
	// Even the right password waits once the budget is spent, and case does not reset it.
	if err := login("ANN", "right-password"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("over budget: %v", err)
	}
}

// One client alone cannot spend an account's budget, so it cannot lock the owner out.
func TestOneClientCannotLockAnAccount(t *testing.T) {
	s, _ := newHTTPServer(t)
	if ip, account := s.ipGuesses, s.accountGuesses; ip.burst >= account.burst || ip.every <= account.every {
		t.Fatal("a client's budget must be smaller and slower than an account's")
	}
}

func TestLoginGuessesAreLimitedPerClient(t *testing.T) {
	_, srv := newHTTPServer(t)
	status := 0
	// Each guess names a different account, so only the client's budget can stop them.
	for i := range 11 {
		status = send(t, "POST", srv.URL+"/v1/auth/login", nil, fmt.Sprintf(`{"username":"u%d","password":"x"}`, i))
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("11th guess from one client: %d", status)
	}
}

func TestForwardedHeadersOnlyFromTrustedProxies(t *testing.T) {
	s := &Server{cfg: Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}}}
	for _, tc := range []struct{ remote, xff, real, want string }{
		{"203.0.113.9:1234", "1.2.3.4", "", "203.0.113.9"},
		{"203.0.113.9:1234", "", "1.2.3.4", "203.0.113.9"},
		{"10.0.0.2:1234", "", "", "10.0.0.2"},
		{"10.0.0.2:1234", "1.2.3.4", "", "1.2.3.4"},
		// The client wrote the leftmost entry itself; the proxy appended what it saw.
		{"10.0.0.2:1234", "6.6.6.6, 1.2.3.4", "", "1.2.3.4"},
		{"10.0.0.2:1234", "6.6.6.6, 1.2.3.4, 10.0.0.3", "", "1.2.3.4"},
		{"10.0.0.2:1234", "6.6.6.6, 1.2.3.4:5555, 10.0.0.3", "", "1.2.3.4"},
		{"10.0.0.2:1234", "junk, 10.0.0.3", "", "10.0.0.3"},
		{"10.0.0.2:1234", "", "1.2.3.4", "1.2.3.4"},
		{"[::1]:1234", "2001:db8::1", "", "2001:db8::1"},
		{"[::ffff:10.0.0.2]:1234", "1.2.3.4", "", "1.2.3.4"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if tc.real != "" {
			r.Header.Set("X-Real-IP", tc.real)
		}
		if got := s.clientIP(r).String(); got != tc.want {
			t.Errorf("%+v: %s", tc, got)
		}
	}
}

func TestSpoofedForwardedForDoesNotDodgeTheLimit(t *testing.T) {
	_, srv := newHTTPServer(t)
	status := 0
	for i := range 11 {
		status = send(t, "POST", srv.URL+"/v1/auth/login", map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i)}, fmt.Sprintf(`{"username":"u%d","password":"x"}`, i))
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("11th guess with made-up forwarded addresses: %d", status)
	}
}

func TestTrustedProxyClientsHaveTheirOwnBudgets(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "state.db"), TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	for i := range 30 {
		if code := send(t, "POST", srv.URL+"/v1/auth/login", map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i)}, fmt.Sprintf(`{"username":"u%d","password":"x"}`, i)); code != http.StatusUnauthorized {
			t.Fatalf("client %d behind the proxy: %d", i, code)
		}
	}
}

func TestAttemptsStayBounded(t *testing.T) {
	a := &attempts{every: time.Hour, burst: 1}
	for i := range maxAttemptKeys + 10 {
		a.allow(fmt.Sprint(i))
	}
	if len(a.keys) > maxAttemptKeys {
		t.Fatalf("held %d busy keys", len(a.keys))
	}
}

func TestAttemptsForgetIdleKeys(t *testing.T) {
	a := &attempts{every: time.Millisecond, burst: 1}
	for i := range maxAttemptKeys {
		a.allow(fmt.Sprint(i))
	}
	time.Sleep(5 * time.Millisecond)
	a.allow("new")
	if len(a.keys) != 1 {
		t.Fatalf("held %d keys after the idle ones refilled", len(a.keys))
	}
}

func TestCrossOriginRequestsAreRefused(t *testing.T) {
	s, srv := newHTTPServer(t)
	u, err := s.store.AddLocalUser("ann", "pw", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := s.startSession(u, "")
	if err != nil {
		t.Fatal(err)
	}
	session := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	evil := map[string]string{"Cookie": session["Cookie"], "Origin": "https://evil.example"}
	if code := send(t, "POST", srv.URL+"/v1/tokens", evil, `{"name":"x"}`); code != http.StatusForbidden {
		t.Fatalf("cross-origin POST: %d", code)
	}
	if code := send(t, "POST", srv.URL+"/v1/tokens", map[string]string{"Cookie": session["Cookie"], "Sec-Fetch-Site": "cross-site"}, `{"name":"x"}`); code != http.StatusForbidden {
		t.Fatalf("cross-site POST: %d", code)
	}
	if code := send(t, "POST", srv.URL+"/v1/tokens", map[string]string{"Cookie": session["Cookie"], "Origin": srv.URL}, `{"name":"x"}`); code != http.StatusOK {
		t.Fatalf("same-origin POST: %d", code)
	}
	_, token, err := s.store.NewToken(store.KindAPI, u.ID, "ci", 0)
	if err != nil {
		t.Fatal(err)
	}
	if code := send(t, "POST", srv.URL+"/v1/tokens", map[string]string{"Authorization": "Bearer " + token}, `{"name":"y"}`); code != http.StatusOK {
		t.Fatalf("API token without Origin: %d", code)
	}
	// Webhooks carry their own secret, so the origin check stays out of their way.
	if code := send(t, "POST", srv.URL+"/hooks/nohost/a", map[string]string{"Origin": "https://evil.example"}, `{}`); code == http.StatusForbidden {
		t.Fatalf("webhook refused for its origin")
	}
}

func TestUIFrameHeaders(t *testing.T) {
	_, srv := newHTTPServer(t)
	res, err := http.Get(srv.URL + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("X-Frame-Options") != "SAMEORIGIN" || res.Header.Get("Content-Security-Policy") != "frame-ancestors 'self'" {
		t.Fatalf("default headers: %v", res.Header)
	}
	for _, tc := range []struct {
		sources  []string
		csp, xfo string
	}{
		{[]string{"'self'", "https://*.example.com", "intranet.example:8443"}, "frame-ancestors 'self' https://*.example.com intranet.example:8443", ""},
		{[]string{"'none'"}, "frame-ancestors 'none'", ""},
		{[]string{"*"}, "", ""},
	} {
		h, err := frameHeaders(tc.sources)
		if err != nil || h["Content-Security-Policy"] != tc.csp || h["X-Frame-Options"] != tc.xfo {
			t.Errorf("%v: %v %v", tc.sources, h, err)
		}
	}
	for _, bad := range [][]string{{"javascript:alert(1)"}, {"https://a.example; script-src *"}, {"'none'", "'self'"}, {"*", "'self'"}, {"ftp://a.example"}, {"'unsafe-inline'"}, {""}} {
		if _, err := frameHeaders(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
		if _, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "state.db"), FrameAncestors: bad}); err == nil {
			t.Errorf("New accepted %q", bad)
		}
	}
}

func TestCeremoniesAreCapped(t *testing.T) {
	s := &Server{ceremonies: map[string]*ceremony{}}
	for i := range maxCeremonies {
		s.ceremonies[fmt.Sprint(i)] = &ceremony{expires: time.Now().Add(time.Hour)}
	}
	if _, err := s.keepCeremony(&ceremony{}); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("over the cap: %v", err)
	}
}
