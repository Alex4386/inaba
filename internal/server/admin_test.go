package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/control"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/hostconfig"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/store"
	"github.com/Alex4386/inaba/internal/testdir"
	"github.com/Alex4386/inaba/internal/tunnel"
)

// The CLI's path to a running server: the admin API over the control socket.
func TestAdminOverControlSocket(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	// Unix socket paths are short; the test's own temp dir can be too long for one.
	tmp := "/tmp"
	if runtime.GOOS == "windows" {
		tmp = ""
	}
	dir, err := os.MkdirTemp(tmp, "inaba")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "c.sock")
	if control.Dial(path) != nil {
		t.Fatal("dialed a socket nobody listens on")
	}
	l, err := control.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go control.Serve(l, s.AdminServer())
	if st, err := os.Stat(path); runtime.GOOS != "windows" && (err != nil || st.Mode().Perm() != 0o600) {
		t.Fatalf("socket permissions: %v %v", st, err)
	}
	if _, err := control.Listen(path); err == nil {
		t.Fatal("a second instance took over a live socket")
	}

	transport := control.Dial(path)
	if transport == nil {
		t.Fatal("could not dial the control socket")
	}
	client := connect.NewClient(transport)
	users := inabav1connect.NewAdminServiceClient(client)

	u, err := users.CreateUser(ctx, &inabav1.CreateUserRequest{Name: "ann", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.CreateUser(ctx, &inabav1.CreateUserRequest{Name: "ann", Password: "x"}); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate account: %v", err)
	}
	// What the CLI prints for that failure.
	_, err = users.SetPassword(ctx, &inabav1.SetPasswordRequest{User: "nobody", Password: "x"})
	if got := control.Plain(err).Error(); got != "no password account nobody" {
		t.Fatalf("plain error = %q", got)
	}
	// The account made over the socket signs in through the public API.
	if _, err := s.Login(ctx, &inabav1.LoginRequest{Username: "ann", Password: "pw"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	created, err := users.CreateUserToken(ctx, &inabav1.CreateUserTokenRequest{User: "ann", Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := users.ListUserTokens(ctx, &inabav1.ListUserTokensRequest{})
	if len(list.Tokens) != 1 || list.Tokens[0].UserName != "ann" {
		t.Fatalf("tokens = %v", list.Tokens)
	}
	if _, err := users.DeleteUser(ctx, &inabav1.DeleteUserRequest{User: u.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.LookupToken(created.Secret); err == nil {
		t.Fatal("token outlived its account")
	}
}

// What an account may do is what an administrator allowed it, on every path in.
func TestPermissionsGateHostsProjectsAndAccounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := New(ctx, Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// The onboarded account is the administrator; bob is an ordinary account.
	token, _ := s.OnboardingToken()
	if u, err := s.Onboard(ctx, &inabav1.OnboardRequest{Token: token, Username: "root", Password: "long-enough"}); err != nil || !u.Admin {
		t.Fatalf("onboarded account: %v %v", u, err)
	}
	bob, _ := s.store.AddLocalUser("bob", "pw", false, nil)
	as := func(name string) (string, *http.Client) {
		u, _ := s.store.FindUser(name)
		_, secret, _ := s.store.NewToken(store.KindAPI, u.ID, "t", 0)
		return secret, &http.Client{}
	}
	rootToken, client := as("root")
	bobToken, _ := as("bob")
	call := func(token, method, path string, body any) int {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		// A caller cannot hand itself permissions.
		req.Header.Set(rpc.PermissionsHeader, "projects,hosts")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	name := map[string]string{"name": "h"}

	if got := call(bobToken, "POST", "/v1/hosts", map[string]any{"name": "h", "shared": true}); got != http.StatusForbidden {
		t.Fatalf("bob adds a shared host: %d", got)
	}
	// Bob's own host is his alone: the administrator neither sees nor reaches it.
	if got := call(bobToken, "POST", "/v1/hosts", name); got != 200 {
		t.Fatalf("bob adds a personal host: %d", got)
	}
	bobs, _ := s.store.HostsFor(bob.ID)
	if len(bobs) != 1 || bobs[0].Owner != bob.ID {
		t.Fatalf("bob's hosts: %+v", bobs)
	}
	if roots, _ := s.store.HostsFor("root-id"); len(roots) != 0 {
		t.Fatalf("someone else sees bob's host: %+v", roots)
	}
	mine := "/v1/hosts/" + bobs[0].ID
	// An administrator reaches any host, as the admin panel's host list leads them to; it is offline here.
	if got := call(rootToken, "GET", mine+"/threads", nil); got != http.StatusServiceUnavailable {
		t.Fatalf("root reaches bob's host: %d", got)
	}
	if got := call(bobToken, "GET", mine+"/threads", nil); got != http.StatusServiceUnavailable {
		t.Fatalf("bob reaches his offline host: %d", got)
	}
	if got := call(bobToken, "PATCH", mine, map[string]string{"name": "bob's laptop", "icon": "laptop", "image": "data:image/png;base64,iVBORw0KGgo="}); got != 200 {
		t.Fatalf("bob renames his host: %d", got)
	}
	if got := call(bobToken, "PATCH", mine, map[string]string{"image": "data:text/html;base64,PHNjcmlwdD4="}); got != http.StatusBadRequest {
		t.Fatalf("bob gives his host a picture that is not one: %d", got)
	}
	if h, _ := s.store.Host(strings.TrimPrefix(mine, "/v1/hosts/")); h.Name != "bob's laptop" || h.Icon != "laptop" || h.Image == "" {
		t.Fatalf("after renaming: %+v", h)
	}
	// An administrator looks after any host, personal ones included.
	if got := call(rootToken, "PATCH", mine, map[string]string{"name": "bob's laptop"}); got != 200 {
		t.Fatalf("root renames bob's host: %d", got)
	}
	if got := call(bobToken, "DELETE", mine, nil); got != 200 {
		t.Fatalf("bob removes his host: %d", got)
	}
	if got := call(bobToken, "GET", "/v1/users", nil); got != http.StatusForbidden {
		t.Fatalf("bob lists accounts: %d", got)
	}
	if got := call(bobToken, "PUT", "/v1/users/bob/access", map[string]any{"admin": true}); got != http.StatusForbidden {
		t.Fatalf("bob makes himself administrator: %d", got)
	}
	if got := call(bobToken, "GET", "/v1/hosts", nil); got != 200 {
		t.Fatalf("bob lists hosts: %d", got)
	}

	// A host for the project checks, connected through the real tunnel.
	h, hostSecret, _ := s.store.AddHost("laptop", "")
	hst := host.New(echoProvider{})
	defer hst.Close()
	if err := hst.LoadProjects(filepath.Join(testdir.New(t), "h.db")); err != nil {
		t.Fatal(err)
	}
	config := &hostconfig.Service{Path: filepath.Join(t.TempDir(), "config.json"), Host: hst}
	if err := hst.AddService(func(s *connect.Server) { inabav1connect.RegisterProviderConfigServiceHandler(s, config) }); err != nil {
		t.Fatal(err)
	}
	hostHandler, _ := hst.Handler()
	l, err := tunnel.Dial(ctx, srv.URL, hostSecret)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go http.Serve(l, hostHandler)
	for online := false; !online; time.Sleep(10 * time.Millisecond) {
		res, _ := s.ListHosts(ctx, nil)
		online = res.Hosts[0].Online
	}
	base := "/v1/hosts/" + h.ID
	dir := t.TempDir()
	project := map[string]string{"cwd": dir}

	if got := call(bobToken, "POST", base+"/projects", project); got != http.StatusForbidden {
		t.Fatalf("bob adds a project: %d", got)
	}
	if got := call(bobToken, "POST", "/hosts/"+h.ID+inabav1connect.ProjectServiceCreateProjectProcedure, project); got != http.StatusForbidden {
		t.Fatalf("bob adds a project over Connect: %d", got)
	}
	if got := call(bobToken, "POST", base+"/threads", map[string]string{"provider": "echo", "cwd": dir}); got != http.StatusForbidden {
		t.Fatalf("bob starts a session outside any project: %d", got)
	}
	if got := call(rootToken, "POST", base+"/projects", project); got != 200 {
		t.Fatalf("the administrator adds a project: %d", got)
	}
	list, _ := hst.ListProjects(ctx, nil)
	if got := call(bobToken, "POST", base+"/threads", map[string]string{"provider": "echo", "projectId": list.Projects[0].Id}); got != 200 {
		t.Fatalf("bob starts a session in an existing project: %d", got)
	}

	// The administrator allows bob to manage projects, and only that.
	if got := call(rootToken, "PUT", "/v1/users/"+bob.ID+"/access", map[string]any{"permissions": []string{"PERMISSION_MANAGE_PROJECTS"}}); got != 200 {
		t.Fatalf("grant: %d", got)
	}
	if got := call(bobToken, "POST", base+"/projects", project); got != 200 {
		t.Fatalf("bob adds a project once allowed: %d", got)
	}
	if got := call(bobToken, "POST", "/v1/hosts", map[string]any{"name": "h", "shared": true}); got != http.StatusForbidden {
		t.Fatalf("bob adds a shared host with only the projects permission: %d", got)
	}
	// A shared host is the administrators' to set up: bob works on it but changes none of it.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", base + "/provider-configs", nil},
		{"PUT", base + "/provider-configs/x", map[string]any{"command": []string{"x"}, "enabled": true}},
		{"PUT", base + "/modules/files", map[string]any{"enabled": false}},
		{"PATCH", "/v1/hosts/" + h.ID, map[string]any{"name": "mine now"}},
		{"DELETE", "/v1/hosts/" + h.ID, nil},
	} {
		if got := call(bobToken, c.method, c.path, c.body); got != http.StatusForbidden {
			t.Fatalf("bob %s %s on the shared host: %d", c.method, c.path, got)
		}
	}
	if got := call(rootToken, "GET", base+"/provider-configs", nil); got != 200 {
		t.Fatalf("the administrator reads the shared host's providers: %d", got)
	}
	// There is always an administrator left.
	if got := call(rootToken, "PUT", "/v1/users/root/access", map[string]any{"admin": false}); got != http.StatusBadRequest {
		t.Fatalf("the last administrator steps down: %d", got)
	}
	if got := call(rootToken, "DELETE", "/v1/users/root", nil); got != http.StatusBadRequest {
		t.Fatalf("the last administrator is removed: %d", got)
	}
}

func postStatus(h http.Handler, path string) int {
	req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestOnboardingCreatesTheFirstAccountOnce(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	// Setup is closed until the server opens it.
	if _, err := s.Onboard(ctx, &inabav1.OnboardRequest{Token: "", Username: "ann", Password: "long-enough"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("onboarding before a token exists: %v", err)
	}
	token, err := s.OnboardingToken()
	if err != nil || token == "" {
		t.Fatalf("token = %q, %v", token, err)
	}
	if cfg, _ := s.GetConfig(ctx, nil); !cfg.NeedsOnboarding {
		t.Fatal("config does not ask for onboarding")
	}
	if _, err := s.Onboard(ctx, &inabav1.OnboardRequest{Token: "wrong", Username: "ann", Password: "long-enough"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := s.Onboard(ctx, &inabav1.OnboardRequest{Token: token, Username: "ann", Password: "short"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("short password: %v", err)
	}
	if _, err := s.Onboard(ctx, &inabav1.OnboardRequest{Token: token, Username: "ann", Password: "long-enough"}); err != nil {
		t.Fatal(err)
	}
	// The link works once.
	if _, err := s.Onboard(ctx, &inabav1.OnboardRequest{Token: token, Username: "eve", Password: "long-enough"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("second use of the link: %v", err)
	}
	if cfg, _ := s.GetConfig(ctx, nil); cfg.NeedsOnboarding {
		t.Fatal("config still asks for onboarding")
	}
	if _, err := s.Login(ctx, &inabav1.LoginRequest{Username: "ann", Password: "long-enough"}); err != nil {
		t.Fatalf("login as the onboarded account: %v", err)
	}
	// A server that already has accounts opens no setup.
	if token, _ := s.OnboardingToken(); token != "" {
		t.Fatal("onboarding reopened on a server with accounts")
	}
}

func TestCreateUserWithAccessAndServerHousekeeping(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	a := admin{s}
	trusted := context.WithValue(context.Background(), trustedKey{}, true)

	u, err := a.CreateUser(trusted, &inabav1.CreateUserRequest{Name: "ops", Password: "pw", Permissions: []inabav1.Permission{inabav1.Permission_PERMISSION_MANAGE_HOSTS}})
	if err != nil || u.Admin || len(u.Permissions) != 1 {
		t.Fatalf("user with a permission: %v %v", u, err)
	}
	if u, err := a.CreateUser(trusted, &inabav1.CreateUserRequest{Name: "boss", Password: "pw", Admin: true}); err != nil || !u.Admin {
		t.Fatalf("administrator: %v %v", u, err)
	}

	// One sign-in that works, one that has expired.
	if _, _, err := s.store.NewToken(store.KindSession, u.Id, "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.store.NewToken(store.KindSession, u.Id, "", time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, err := a.GetServerHousekeeping(context.Background(), &inabav1.GetServerHousekeepingRequest{}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("housekeeping for someone not signed in: %v", err)
	}
	hk, err := a.GetServerHousekeeping(trusted, &inabav1.GetServerHousekeepingRequest{})
	if err != nil || hk.ExpiredTokens != 1 || hk.ActiveSignIns != 1 || hk.DatabaseBytes == 0 {
		t.Fatalf("housekeeping = %v %v", hk, err)
	}
	hk, err = a.CleanUpServer(trusted, &inabav1.CleanUpServerRequest{ExpiredTokens: true, CompactDatabase: true})
	if err != nil || hk.ExpiredTokens != 0 || hk.ActiveSignIns != 1 {
		t.Fatalf("after clean-up = %v %v", hk, err)
	}
}
