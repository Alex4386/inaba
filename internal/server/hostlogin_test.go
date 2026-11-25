package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Alex4386/inaba/internal/store"
	"github.com/Alex4386/inaba/internal/testdir"
)

// A machine signs in as a host: it gets a code without an account, a user approves it,
// and the machine picks up a refresh token that buys access tokens and nothing else.
func TestHostLoginDeviceFlow(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db"), PublicURL: "https://inaba.example"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	c := &http.Client{}
	bob, _ := s.store.AddLocalUser("bob", "pw", false, nil)
	_, bobToken, _ := s.store.NewToken(store.KindAPI, bob.ID, "t", 0)

	code, started := postJSON(t, c, srv.URL+"/v1/host-logins", "", map[string]string{"name": "laptop"})
	if code != 200 || started["verificationUriComplete"] != "https://inaba.example/ui/device?code="+started["userCode"].(string) {
		t.Fatalf("start: %d %v", code, started)
	}
	device, user := started["deviceCode"].(string), started["userCode"].(string)
	poll := func() (int, map[string]any) {
		s.mu.Lock()
		for _, l := range s.logins {
			l.lastPoll = time.Time{}
		}
		s.mu.Unlock()
		return postJSON(t, c, srv.URL+"/v1/host-logins/poll", "", map[string]string{"deviceCode": device})
	}
	if code, res := poll(); code != 200 || res["pending"] != true {
		t.Fatalf("poll before approval: %d %v", code, res)
	}
	// Approving needs an account, and a shared host the hosts permission.
	if code, _ := postJSON(t, c, srv.URL+"/v1/host-logins/"+user+"/approve", "", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("approve without an account: %d", code)
	}
	if code, _ := postJSON(t, c, srv.URL+"/v1/host-logins/"+user+"/approve", bobToken, map[string]any{"shared": true}); code != http.StatusForbidden {
		t.Fatalf("bob approves a shared host: %d", code)
	}
	// The code as a person types it, lowercase and without the dash.
	typed := user[:4] + user[5:]
	if code, res := postJSON(t, c, srv.URL+"/v1/host-logins/"+typed+"/approve", bobToken, map[string]any{}); code != 200 || res["name"] != "laptop" || res["shared"] == true {
		t.Fatalf("approve: %d %v", code, res)
	}
	code, done := poll()
	refresh, _ := done["refreshToken"].(string)
	if code != 200 || refresh == "" {
		t.Fatalf("poll after approval: %d %v", code, done)
	}
	if code, _ := poll(); code != http.StatusNotFound {
		t.Fatalf("the refresh token is handed over twice: %d", code)
	}
	code, tok := postJSON(t, c, srv.URL+"/v1/host-tokens", "", map[string]string{"refreshToken": refresh})
	access, _ := tok["accessToken"].(string)
	if code != 200 || access == "" {
		t.Fatalf("refresh: %d %v", code, tok)
	}
	// Neither of the host's tokens signs anyone in.
	for _, secret := range []string{refresh, access} {
		if code, _ := postJSON(t, c, srv.URL+"/v1/hosts", secret, map[string]string{"name": "x"}); code != http.StatusUnauthorized {
			t.Fatalf("a host token used as a user's: %d", code)
		}
	}
	// Removing the host ends its refresh token.
	hosts, _ := s.store.HostsFor(bob.ID)
	if err := s.store.DeleteHost(hosts[0].ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := postJSON(t, c, srv.URL+"/v1/host-tokens", "", map[string]string{"refreshToken": refresh}); code != http.StatusUnauthorized {
		t.Fatalf("refresh after removal: %d", code)
	}

	// A denied login tells the machine so.
	_, started = postJSON(t, c, srv.URL+"/v1/host-logins", "", map[string]string{"name": "other"})
	device = started["deviceCode"].(string)
	if code, _ := postJSON(t, c, srv.URL+"/v1/host-logins/"+started["userCode"].(string)+"/deny", bobToken, map[string]any{}); code != 200 {
		t.Fatalf("deny: %d", code)
	}
	if code, _ := poll(); code != http.StatusForbidden {
		t.Fatalf("poll after denial: %d", code)
	}
}
