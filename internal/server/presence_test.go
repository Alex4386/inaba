package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Alex4386/inaba/internal/store"
	"github.com/Alex4386/inaba/internal/testdir"
)

// People on the same host see each other, and where; a personal host shows no one else.
func TestPresence(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	c := &http.Client{}
	token := func(name string) string {
		u, _ := s.store.AddLocalUser(name, "pw", false, nil)
		_, secret, _ := s.store.NewToken(store.KindAPI, u.ID, "t", 0)
		return secret
	}
	alice, bob := token("alice"), token("bob")
	shared, _, _ := s.store.AddHost("shared", "")
	bobs, _, _ := s.store.AddHost("bobs", "")
	bobUser, _ := s.store.FindUser("bob")
	s.store.UpdateHost(bobs.ID, map[string]any{"owner": bobUser.ID})

	beat := func(tok, client, host, thread string) (int, []any) {
		code, res := postJSON(t, c, srv.URL+"/v1/presence", tok, map[string]string{"clientId": client, "hostId": host, "threadId": thread})
		list, _ := res["present"].([]any)
		return code, list
	}
	if code, list := beat(bob, "b1", shared.ID, "t1"); code != 200 || len(list) != 0 {
		t.Fatalf("bob alone: %d %v", code, list)
	}
	beat(bob, "b2", shared.ID, "t2")
	code, list := beat(alice, "a1", shared.ID, "t1")
	if code != 200 || len(list) != 2 {
		t.Fatalf("alice sees: %d %v", code, list)
	}
	if first := list[0].(map[string]any); first["name"] != "bob" || first["threadId"] != "t1" {
		t.Fatalf("first: %v", first)
	}
	if code, list := beat(bob, "b1", shared.ID, "t1"); code != 200 || len(list) != 1 || list[0].(map[string]any)["name"] != "alice" {
		t.Fatalf("bob sees: %d %v (not himself)", code, list)
	}
	// Everyone else on the session, oneself in another browser tab too, is a client.
	postJSON(t, c, srv.URL+"/v1/presence", bob, map[string]any{"clientId": "b1", "hostId": shared.ID, "threadId": "t1"})
	postJSON(t, c, srv.URL+"/v1/presence", alice, map[string]any{"clientId": "a2", "hostId": shared.ID, "threadId": "t1"})
	_, res := postJSON(t, c, srv.URL+"/v1/presence", alice, map[string]any{"clientId": "a1", "hostId": shared.ID, "threadId": "t1"})
	if clients, _ := res["clients"].([]any); len(clients) != 2 {
		t.Fatalf("clients on t1 besides a1: %v", res["clients"])
	}
	if code, _ := beat(alice, "a1", bobs.ID, ""); code != http.StatusNotFound {
		t.Fatalf("alice on bob's personal host: %d", code)
	}
	if code, _ := beat("", "x", shared.ID, ""); code != http.StatusUnauthorized {
		t.Fatalf("without an account: %d", code)
	}
}

// A picture is saved as one's own, served to anyone signed in, and shown with presence.
func TestAvatar(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	c := &http.Client{}
	u, _ := s.store.AddLocalUser("dana", "pw", false, nil)
	_, tok, _ := s.store.NewToken(store.KindAPI, u.ID, "t", 0)
	patch := func(body map[string]any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("PATCH", srv.URL+"/v1/me", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	code, me := patch(map[string]any{"avatar": "data:image/png;base64," + png})
	url, _ := me["avatarUrl"].(string)
	if code != 200 || url == "" {
		t.Fatalf("set: %d %v", code, me)
	}
	get := func(token string) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+url, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode, res.Header.Get("Content-Type")
	}
	if code, ct := get(tok); code != 200 || ct != "image/png" {
		t.Fatalf("serve: %d %s", code, ct)
	}
	if code, _ := get(""); code != http.StatusUnauthorized {
		t.Fatalf("serve without an account: %d", code)
	}
	if code, _ := patch(map[string]any{"avatar": "data:text/html;base64,PHNjcmlwdD4="}); code != http.StatusBadRequest {
		t.Fatalf("a page as a picture: %d", code)
	}
	if code, me := patch(map[string]any{"avatar": ""}); code != 200 || (me["avatarUrl"] != nil && me["avatarUrl"] != "") {
		t.Fatalf("remove: %d %v", code, me)
	}
	if code, _ := get(tok); code != http.StatusNotFound {
		t.Fatalf("serve after removing: %d", code)
	}
}

// One browser drives a session: a free seat goes to the first who asks, a held one only to
// someone who saw who held it, and it frees itself when its holder leaves.
func TestDriverSeat(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	c := &http.Client{}
	u, _ := s.store.AddLocalUser("alice", "pw", false, nil)
	_, tok, _ := s.store.NewToken(store.KindAPI, u.ID, "t", 0)
	host, _, _ := s.store.AddHost("shared", "")
	beat := func(client, thread string, take bool, from string) string {
		t.Helper()
		code, res := postJSON(t, c, srv.URL+"/v1/presence", tok, map[string]any{"clientId": client, "hostId": host.ID, "threadId": thread, "take": take, "from": from})
		if code != 200 {
			t.Fatalf("heartbeat: %d %v", code, res)
		}
		d, _ := res["driver"].(string)
		return d
	}
	beatRelease := func(client, thread string) string {
		t.Helper()
		_, res := postJSON(t, c, srv.URL+"/v1/presence", tok, map[string]any{"clientId": client, "hostId": host.ID, "threadId": thread, "release": true})
		d, _ := res["driver"].(string)
		return d
	}
	if d := beat("a", "t1", false, ""); d != "" {
		t.Fatalf("no one asked, yet %q drives", d)
	}
	if d := beat("a", "t1", true, ""); d != "a" {
		t.Fatalf("first to ask: %q", d)
	}
	if d := beat("b", "t1", true, ""); d != "a" {
		t.Fatalf("a second asking for a free seat that was just taken: %q", d)
	}
	if d := beat("b", "t1", true, "a"); d != "b" {
		t.Fatalf("taking over from the holder one saw: %q", d)
	}
	// Only its holder can give it up.
	if d := beatRelease("a", "t1"); d != "b" {
		t.Fatalf("released by someone not holding it: %q", d)
	}
	if d := beatRelease("b", "t1"); d != "" {
		t.Fatalf("released by its holder: %q", d)
	}
	// Having handed it back, b is not waiting for it: a is the one left to take it.
	if _, res := postJSON(t, c, srv.URL+"/v1/presence", tok, map[string]any{"clientId": "a", "hostId": host.ID, "threadId": "t1"}); len(res["clients"].([]any)) != 0 {
		t.Fatalf("a sees others waiting for the seat: %v", res["clients"])
	}
	beat("b", "t1", true, "")
	// Its holder going elsewhere frees the seat.
	beat("b", "t2", false, "")
	if d := beat("a", "t1", false, ""); d != "" {
		t.Fatalf("after the holder left: %q", d)
	}

	// Someone else's seat is not one's to take; an administrator may take it, or hand it on.
	bobUser, _ := s.store.AddLocalUser("bob", "pw", false, nil)
	_, bobTok, _ := s.store.NewToken(store.KindAPI, bobUser.ID, "t", 0)
	root, _ := s.store.AddLocalUser("root", "pw", true, nil)
	_, rootTok, _ := s.store.NewToken(store.KindAPI, root.ID, "t", 0)
	as := func(tok, client string, body map[string]any) string {
		t.Helper()
		body["clientId"], body["hostId"], body["threadId"] = client, host.ID, "t3"
		code, res := postJSON(t, c, srv.URL+"/v1/presence", tok, body)
		if code != 200 {
			t.Fatalf("heartbeat: %d %v", code, res)
		}
		d, _ := res["driver"].(string)
		return d
	}
	if d := as(tok, "a", map[string]any{"take": true}); d != "a" {
		t.Fatalf("alice on a free seat: %q", d)
	}
	if d := as(bobTok, "bob1", map[string]any{"take": true, "from": "a"}); d != "a" {
		t.Fatalf("bob took alice's seat: %q", d)
	}
	as(rootTok, "root1", map[string]any{})
	if d := as(rootTok, "root1", map[string]any{"giveTo": "bob1", "from": "a"}); d != "bob1" {
		t.Fatalf("an administrator handing the seat to bob: %q", d)
	}
	if d := as(rootTok, "root1", map[string]any{"take": true, "from": "bob1"}); d != "root1" {
		t.Fatalf("an administrator taking the seat: %q", d)
	}
}
