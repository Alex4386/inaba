package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/store"
	"github.com/Alex4386/inaba/internal/testdir"
	"github.com/Alex4386/inaba/internal/tunnel"
)

// A personal host is its owner's alone until they invite someone by link; an administrator reaches any.
func TestHostInvites(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	c := &http.Client{}
	account := func(name string, admin bool) string {
		u, _ := s.store.AddLocalUser(name, "pw", admin, nil)
		_, secret, _ := s.store.NewToken(store.KindAPI, u.ID, "t", 0)
		return secret
	}
	alice, bob, carol, root := account("alice", false), account("bob", false), account("carol", false), account("root", true)
	aliceUser, _ := s.store.FindUser("alice")
	h, _, _ := s.store.AddHost("alice-mac", aliceUser.ID)
	listed := func(tok string) []any {
		t.Helper()
		_, res := getJSON(t, srv.URL+"/v1/hosts", tok)
		list, _ := res["hosts"].([]any)
		return list
	}
	beat := func(tok string) int {
		code, _ := postJSON(t, c, srv.URL+"/v1/presence", tok, map[string]any{"clientId": "x", "hostId": h.ID})
		return code
	}
	if len(listed(bob)) != 0 || beat(bob) != http.StatusNotFound {
		t.Fatal("bob reaches alice's host uninvited")
	}
	if code, _ := postJSON(t, c, srv.URL+"/v1/invites", carol, map[string]any{"hostId": h.ID}); code != http.StatusNotFound {
		t.Fatalf("carol inviting to alice's host: %d", code)
	}
	code, res := postJSON(t, c, srv.URL+"/v1/invites", alice, map[string]any{"hostId": h.ID})
	token, _ := res["token"].(string)
	if code != 200 || token == "" {
		t.Fatalf("alice's invite: %d %v", code, res)
	}
	if code, res := getJSON(t, srv.URL+"/v1/invites/"+token, bob); code != 200 || res["hostName"] != "alice-mac" || res["joined"] == true {
		t.Fatalf("bob looks at the invite: %d %v", code, res)
	}
	if code, res := postJSON(t, c, srv.URL+"/v1/invites/"+token+"/accept", bob, map[string]any{}); code != 200 || res["invited"] != true {
		t.Fatalf("bob joins: %d %v", code, res)
	}
	if len(listed(bob)) != 1 || beat(bob) != 200 {
		t.Fatal("bob does not reach the host he joined")
	}
	// Joined is not owning: bob cannot invite others in.
	if code, _ := postJSON(t, c, srv.URL+"/v1/invites", bob, map[string]any{"hostId": h.ID}); code != http.StatusForbidden {
		t.Fatalf("bob inviting: %d", code)
	}
	// Ended links let no one else in; who joined stays until removed.
	postJSON(t, c, srv.URL+"/v1/invites/revoke", alice, map[string]any{"hostId": h.ID})
	if code, _ := postJSON(t, c, srv.URL+"/v1/invites/"+token+"/accept", carol, map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("carol on an ended invite: %d", code)
	}
	req, _ := http.NewRequest("DELETE", srv.URL+"/v1/host-members/"+h.ID+"/"+mustFind(t, s, "bob"), nil)
	req.Header.Set("Authorization", "Bearer "+alice)
	if r, err := c.Do(req); err != nil || r.StatusCode != 200 {
		t.Fatalf("removing bob: %v %v", r, err)
	}
	if len(listed(bob)) != 0 {
		t.Fatal("bob still lists the host after removal")
	}
	// An administrator sees every host in the admin list, and reaches it, but not in their own list.
	if code, _ := getJSON(t, srv.URL+"/v1/hosts?all=true", alice); code != http.StatusForbidden {
		t.Fatalf("all hosts for someone not an administrator: %d", code)
	}
	if code, res := getJSON(t, srv.URL+"/v1/hosts?all=true", root); code != 200 || len(res["hosts"].([]any)) != 1 || len(listed(root)) != 0 || beat(root) != 200 {
		t.Fatalf("the administrator's list: %d %v", code, res)
	}
}

// An invite grants the role it was made with; pending ones are listed without their secrets and
// end one at a time; a plain member neither sees nor makes them.
func TestInviteRoles(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	c := &http.Client{}
	account := func(name string) (string, string) {
		u, _ := s.store.AddLocalUser(name, "pw", false, nil)
		_, secret, _ := s.store.NewToken(store.KindAPI, u.ID, "t", 0)
		return u.ID, secret
	}
	aliceID, alice := account("alice")
	bobID, bob := account("bob")
	carolID, carol := account("carol")
	h, _, _ := s.store.AddHost("alice-mac", aliceID)
	invite := func(tok string, body map[string]any) (int, string) {
		t.Helper()
		body["hostId"] = h.ID
		code, res := postJSON(t, c, srv.URL+"/v1/invites", tok, body)
		secret, _ := res["token"].(string)
		return code, secret
	}
	pending := func(tok string) (int, []any) {
		t.Helper()
		code, res := getJSON(t, srv.URL+"/v1/host-invites/"+h.ID, tok)
		list, _ := res["invites"].([]any)
		return code, list
	}
	if code, _ := invite(alice, map[string]any{"role": "owner"}); code != http.StatusBadRequest {
		t.Fatalf("an invite to own the host: %d", code)
	}
	_, mgr := invite(alice, map[string]any{"role": "manager", "note": "for bob"})
	_, plain := invite(alice, map[string]any{})
	if code, res := getJSON(t, srv.URL+"/v1/invites/"+mgr, bob); code != 200 || res["role"] != "manager" {
		t.Fatalf("bob looks at the manager invite: %d %v", code, res)
	}
	code, list := pending(alice)
	if code != 200 || len(list) != 2 {
		t.Fatalf("alice's pending invites: %d %v", code, list)
	}
	first := list[0].(map[string]any)
	if first["role"] != "manager" || first["note"] != "for bob" || first["createdBy"] != "alice" || first["expiresAt"] == nil {
		t.Fatalf("the manager invite as listed: %v", first)
	}
	for _, inv := range list {
		for _, v := range inv.(map[string]any) {
			if v == mgr || v == plain {
				t.Fatal("a listed invite shows its secret")
			}
		}
	}
	postJSON(t, c, srv.URL+"/v1/invites/"+mgr+"/accept", bob, map[string]any{})
	postJSON(t, c, srv.URL+"/v1/invites/"+plain+"/accept", carol, map[string]any{})
	if r := s.store.MemberRole(h.ID, bobID); r != store.RoleManager {
		t.Fatalf("bob joined as %q", r)
	}
	if r := s.store.MemberRole(h.ID, carolID); r != store.RoleMember {
		t.Fatalf("carol joined as %q", r)
	}
	// Opening a member's invite does not take a manager down.
	postJSON(t, c, srv.URL+"/v1/invites/"+plain+"/accept", bob, map[string]any{})
	if r := s.store.MemberRole(h.ID, bobID); r != store.RoleManager {
		t.Fatalf("bob after a member's invite: %q", r)
	}
	// A plain member neither sees nor makes invites; a manager does.
	if code, _ := pending(carol); code != http.StatusForbidden {
		t.Fatalf("carol listing invites: %d", code)
	}
	if code, _ := invite(carol, map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("carol inviting: %d", code)
	}
	if code, list := pending(bob); code != 200 || len(list) != 2 {
		t.Fatalf("bob, a manager, listing invites: %d %v", code, list)
	}
	del := func(id string) int {
		req, _ := http.NewRequest("DELETE", srv.URL+"/v1/host-invites/"+h.ID+"/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+alice)
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r.StatusCode
	}
	if del(first["id"].(string)) != 200 || del(first["id"].(string)) != http.StatusNotFound {
		t.Fatal("ending the manager invite once")
	}
	if _, list := pending(alice); len(list) != 1 || list[0].(map[string]any)["role"] != "member" {
		t.Fatalf("after ending one: %v", list)
	}
	if code, _ := getJSON(t, srv.URL+"/v1/invites/"+mgr, carol); code != http.StatusNotFound {
		t.Fatalf("an ended invite still opens: %d", code)
	}
}

func mustFind(t *testing.T, s *Server, name string) string {
	t.Helper()
	u, err := s.store.FindUser(name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// What a personal host is told each caller may do: its owner everything, someone invited in
// everything on the host but adding shared hosts (whatever their account holds), an administrator everything.
func TestInvitedPermissions(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	account := func(name string, admin bool, perms []string) (string, string) {
		u, _ := s.store.AddLocalUser(name, "pw", admin, perms)
		_, secret, _ := s.store.NewToken(store.KindAPI, u.ID, "t", 0)
		return u.ID, secret
	}
	aliceID, alice := account("alice", false, nil)
	bobID, bob := account("bob", false, []string{store.PermSharedHosts, store.PermHostSettings})
	_, root := account("root", true, nil)
	h, secret, _ := s.store.AddHost("alice-mac", aliceID)
	_ = s.store.AddMember(h.ID, bobID)

	heard, heardUser := make(chan string, 1), make(chan string, 1)
	l, err := tunnel.Dial(ctx, srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server asks what the host runs on and follows its alerts as it connects; only the
		// callers' requests count here.
		if strings.Contains(r.URL.Path, "SystemService") || strings.Contains(r.URL.Path, "AlertService") {
			http.NotFound(w, r)
			return
		}
		heardUser <- r.Header.Get(rpc.UserHeader)
		heard <- r.Header.Get(rpc.PermissionsHeader)
	}))
	for online := false; !online; time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		online = s.links[h.ID] != nil
		s.mu.Unlock()
	}
	var user string
	granted := func(tok string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/v1/hosts/"+h.ID+"/threads", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		// The host is told who calls by the server, not by the caller.
		req.Header.Set(rpc.UserHeader, "someone-else")
		if _, err := http.DefaultClient.Do(req); err != nil {
			t.Fatal(err)
		}
		user = <-heardUser
		return <-heard
	}
	if got := granted(alice); got != strings.Join(store.AllPermissions, ",")+","+rpc.PermHosts+","+rpc.PermRestart {
		t.Errorf("the owner: %q", got)
	}
	if user != aliceID {
		t.Errorf("the host is told the owner is %q, not %q", user, aliceID)
	}
	// A member uses the host's terminal anyway, so they are told everything about the host, and
	// older hosts hear host settings and housekeeping by the old name too.
	member := "projects,threads-anywhere,automations,host-settings,housekeeping,hosts"
	if got := granted(bob); got != member || user != bobID {
		t.Errorf("someone invited in, with permissions for shared hosts: %q, as %q", got, user)
	}
	if got := granted(root); !strings.Contains(got, store.PermHousekeeping) || !strings.Contains(got, rpc.PermHosts) || !strings.Contains(got, rpc.PermRestart) {
		t.Errorf("an administrator: %q", got)
	}
	// A manager differs only in looking after members and invites, which the host is not told of.
	_ = s.store.SetMember(h.ID, bobID, store.RoleManager)
	if got := granted(bob); got != member {
		t.Errorf("a manager: %q", got)
	}
}

// A shared host kept to its members turns others away; an administrator lets people in by role,
// and sees each account's place on every host.
func TestRestrictedSharedHost(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	c := &http.Client{}
	account := func(name string, admin bool) (string, string) {
		u, _ := s.store.AddLocalUser(name, "pw", admin, nil)
		_, secret, _ := s.store.NewToken(store.KindAPI, u.ID, "t", 0)
		return u.ID, secret
	}
	bobID, bob := account("bob", false)
	_, root := account("root", true)
	h, _, _ := s.store.AddHost("lab", "")
	reach := func() int {
		code, _ := postJSON(t, c, srv.URL+"/v1/presence", bob, map[string]any{"clientId": "b", "hostId": h.ID})
		return code
	}
	if reach() != 200 {
		t.Fatal("an open shared host turned bob away")
	}
	patch := func(tok string, body any) int {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("PATCH", srv.URL+"/v1/hosts/"+h.ID, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r.StatusCode
	}
	if patch(bob, map[string]any{"restricted": true}) == 200 {
		t.Fatal("bob restricted a shared host")
	}
	if patch(root, map[string]any{"restricted": true}) != 200 || reach() != http.StatusNotFound {
		t.Fatal("restricting did not keep bob out")
	}
	put := func(role string) int {
		b, _ := json.Marshal(map[string]string{"role": role})
		req, _ := http.NewRequest("PUT", srv.URL+"/v1/host-members/"+h.ID+"/"+bobID, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+root)
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r.StatusCode
	}
	if put("member") != 200 || reach() != 200 {
		t.Fatal("a member of a restricted host is kept out")
	}
	if code, res := getJSON(t, srv.URL+"/v1/users/bob/detail", root); code != 200 || res["hosts"].([]any)[0].(map[string]any)["role"] != "member" {
		t.Fatalf("bob's detail: %d %v", code, res)
	}
	if put("none") != 200 || reach() != http.StatusNotFound {
		t.Fatal("taken out, bob still reaches the restricted host")
	}
}

// A manager's invites end when they stop looking after the host, and one left behind no longer
// works; a member or manager changes how the host looks but does not rename or remove it.
func TestInvitesEndWithTheirMaker(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	c := &http.Client{}
	account := func(name string) (string, string) {
		u, _ := s.store.AddLocalUser(name, "pw", false, nil)
		_, secret, _ := s.store.NewToken(store.KindAPI, u.ID, "t", 0)
		return u.ID, secret
	}
	aliceID, alice := account("alice")
	bobID, bob := account("bob")
	_, carol := account("carol")
	h, _, _ := s.store.AddHost("alice-mac", aliceID)
	send := func(method, path, tok string, body any) int {
		t.Helper()
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	invite := func(note string) string {
		t.Helper()
		code, res := postJSON(t, c, srv.URL+"/v1/invites", bob, map[string]any{"hostId": h.ID, "role": "manager", "note": note})
		if code != 200 {
			t.Fatalf("bob's invite: %d %v", code, res)
		}
		return res["token"].(string)
	}
	opens := func(token string) int {
		code, _ := getJSON(t, srv.URL+"/v1/invites/"+token, carol)
		return code
	}

	_ = s.store.SetMember(h.ID, bobID, store.RoleManager)
	// The note's limit is in characters, not bytes.
	invite(strings.Repeat("가", 200))
	if code, _ := postJSON(t, c, srv.URL+"/v1/invites", bob, map[string]any{"hostId": h.ID, "note": strings.Repeat("가", 201)}); code != http.StatusBadRequest {
		t.Fatalf("a note of 201 characters: %d", code)
	}
	lowered := invite("lowered")
	if send("PUT", "/v1/host-members/"+h.ID+"/"+bobID, alice, map[string]string{"role": "member"}) != 200 || opens(lowered) != http.StatusNotFound {
		t.Fatal("bob's invite outlived his being lowered to member")
	}
	_ = s.store.SetMember(h.ID, bobID, store.RoleManager)
	removed := invite("removed")
	if code := send("DELETE", "/v1/host-members/"+h.ID+"/"+bobID, alice, nil); code != 200 || opens(removed) != http.StatusNotFound {
		t.Fatalf("bob's invite outlived his removal: %d %d", code, opens(removed))
	}
	// One left behind, by a change that skipped the above, no longer lets anyone in as a manager.
	_ = s.store.SetMember(h.ID, bobID, store.RoleManager)
	stale := invite("stale")
	_ = s.store.SetMember(h.ID, bobID, store.RoleMember)
	if opens(stale) != http.StatusNotFound {
		t.Fatal("a manager invite by someone no longer a manager still opens")
	}
	if code, _ := postJSON(t, c, srv.URL+"/v1/invites/"+stale+"/accept", carol, map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("accepting it: %d", code)
	}

	// bob, a member again, changes the look but not the name, and does not remove the host.
	if send("PATCH", "/v1/hosts/"+h.ID, bob, map[string]any{"icon": "server"}) != 200 {
		t.Fatal("a member could not change the host's icon")
	}
	if send("PATCH", "/v1/hosts/"+h.ID, bob, map[string]any{"name": "bobs"}) != http.StatusForbidden {
		t.Fatal("a member renamed the host")
	}
	_ = s.store.SetMember(h.ID, bobID, store.RoleManager)
	if send("DELETE", "/v1/hosts/"+h.ID, bob, nil) != http.StatusForbidden {
		t.Fatal("a manager removed someone's host")
	}
	if send("PATCH", "/v1/hosts/"+h.ID, alice, map[string]any{"name": "alice-mac2"}) != 200 || send("DELETE", "/v1/hosts/"+h.ID, alice, nil) != 200 {
		t.Fatal("the owner could not rename and remove the host")
	}
}

// Restarting a shared host is for administrators and those who may add shared hosts, like
// removing it; everyone else who uses it is not told they may.
func TestRestartOnSharedHost(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	account := func(name string, perms []string) string {
		u, _ := s.store.AddLocalUser(name, "pw", false, perms)
		_, secret, _ := s.store.NewToken(store.KindAPI, u.ID, "t", 0)
		return secret
	}
	keeper := account("keeper", []string{store.PermSharedHosts})
	user := account("user", []string{store.PermHostSettings, store.PermHousekeeping})
	h, secret, _ := s.store.AddHost("shared", "")

	heard := make(chan string, 1)
	l, err := tunnel.Dial(ctx, srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "SystemService") || strings.Contains(r.URL.Path, "AlertService") {
			http.NotFound(w, r)
			return
		}
		heard <- r.Header.Get(rpc.PermissionsHeader)
	}))
	for online := false; !online; time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		online = s.links[h.ID] != nil
		s.mu.Unlock()
	}
	granted := func(tok string) []string {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/v1/hosts/"+h.ID+"/threads", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if _, err := http.DefaultClient.Do(req); err != nil {
			t.Fatal(err)
		}
		return strings.Split(<-heard, ",")
	}
	if got := granted(keeper); !slices.Contains(got, rpc.PermRestart) {
		t.Errorf("one who may add shared hosts: %q", got)
	}
	if got := granted(user); slices.Contains(got, rpc.PermRestart) {
		t.Errorf("one who only uses it, with every other permission on it: %q", got)
	}
}
