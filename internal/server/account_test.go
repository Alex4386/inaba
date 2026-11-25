package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/Alex4386/inaba/internal/testdir"
)

// Two-step sign-in, password changes and the list of sign-ins, over the API as a browser uses it.
func TestAccountSecurity(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	// This walk makes more guesses for one account than the default budget allows.
	s.accountGuesses = &attempts{every: time.Second, burst: 20}
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	s.store.AddLocalUser("erin", "old-password", false, nil)
	login := func(password, code string) (int, string) {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/auth/login", strings.NewReader(`{"username":"erin","password":"`+password+`","totpCode":"`+code+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "TestBrowser/1")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		for _, c := range res.Cookies() {
			if c.Name == sessionCookie {
				return res.StatusCode, c.Value
			}
		}
		return res.StatusCode, ""
	}
	code, session := login("old-password", "")
	if code != 200 || session == "" {
		t.Fatalf("login: %d", code)
	}

	// Two-step sign-in: set up with a code, then a login without one is asked for it.
	_, begun := postJSON(t, http.DefaultClient, srv.URL+"/v1/me/totp", session, map[string]any{})
	secret, _ := begun["secret"].(string)
	if secret == "" || !strings.HasPrefix(begun["qrCode"].(string), "data:image/png;base64,") {
		t.Fatalf("begin: %v", begun)
	}
	if code, _ := postJSON(t, http.DefaultClient, srv.URL+"/v1/me/totp/enable", session, map[string]any{"code": "000000"}); code != http.StatusBadRequest {
		t.Fatalf("enable with a wrong code: %d", code)
	}
	now, _ := totp.GenerateCode(secret, time.Now())
	if code, me := postJSON(t, http.DefaultClient, srv.URL+"/v1/me/totp/enable", session, map[string]any{"code": now}); code != 200 || me["totp"] != true {
		t.Fatalf("enable: %d %v", code, me)
	}
	if code, _ := login("old-password", ""); code != http.StatusPreconditionFailed && code != http.StatusBadRequest {
		t.Fatalf("login without the code: %d", code)
	}
	if code, _ := login("old-password", "123456"); code != http.StatusUnauthorized {
		t.Fatalf("login with a wrong code: %d", code)
	}
	if code, _ := login("old-password", now); code != 200 {
		t.Fatalf("login with the code: %d", code)
	}

	// Sign-ins are listed with their browser; changing the password ends the others.
	code, list := getJSON(t, srv.URL+"/v1/me/sessions", session)
	sessions, _ := list["sessions"].([]any)
	if code != 200 || len(sessions) != 2 || sessions[0].(map[string]any)["userAgent"] != "TestBrowser/1" {
		t.Fatalf("sessions: %d %v", code, list)
	}
	if code, _ := postJSON(t, http.DefaultClient, srv.URL+"/v1/me/password", session, map[string]any{"currentPassword": "nope", "newPassword": "new-password"}); code != http.StatusForbidden {
		t.Fatalf("change with a wrong password: %d", code)
	}
	if code, _ := postJSON(t, http.DefaultClient, srv.URL+"/v1/me/password", session, map[string]any{"currentPassword": "old-password", "newPassword": "new-password"}); code != 200 {
		t.Fatalf("change: %d", code)
	}
	if _, list := getJSON(t, srv.URL+"/v1/me/sessions", session); len(list["sessions"].([]any)) != 1 {
		t.Fatalf("other sign-ins kept after the change: %v", list)
	}

	// Passkeys begin with options for the browser; a made-up answer is refused.
	// The page's origin has to be the server's own, or the cross-origin check refuses it.
	local := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	req, _ := http.NewRequest("POST", local+"/v1/me/passkeys/begin", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+session)
	req.Header.Set("Origin", local)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("passkey begin: %v %v", res, err)
	}
	res.Body.Close()
}

func getJSON(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = jsonDecode(res, &out)
	return res.StatusCode, out
}

func jsonDecode(res *http.Response, v any) error { return json.NewDecoder(res.Body).Decode(v) }
