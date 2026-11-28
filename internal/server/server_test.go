package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/modules"
	"github.com/Alex4386/inaba/internal/testdir"
	"github.com/Alex4386/inaba/internal/tunnel"
	"github.com/Alex4386/inaba/pkg/provider"
)

type echoProvider struct{}

func (echoProvider) ID() string { return "echo" }
func (echoProvider) Open(_ context.Context, _ provider.Config, emit provider.Emit) (provider.Session, error) {
	return echoSession{emit}, nil
}

type echoSession struct{ emit provider.Emit }

func (echoSession) NativeSessionID() string { return "n" }
func (s echoSession) Prompt(_ context.Context, p provider.Prompt) (string, error) {
	s.emit(&inabav1.Event{Payload: &inabav1.Event_TextDelta{TextDelta: &inabav1.TextDelta{Text: "echo " + p.Text}}})
	return "end_turn", nil
}
func (echoSession) Interrupt(context.Context) error               { return nil }
func (echoSession) Respond(context.Context, string, string) error { return nil }
func (echoSession) Close() error                                  { return nil }

func postJSON(t *testing.T, c *http.Client, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// Signs in, registers a host, connects it, and drives a turn on it through the relay.
func TestLoginHostAndRelayedTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := New(ctx, Config{DataPath: filepath.Join(testdir.New(t), "state.db")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.AddLocalUser("admin", "pw", false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.SetAccess("admin", true, nil); err != nil {
		t.Fatal(err)
	}
	handler, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Not for search engines, and said to anyone, signed in or not.
	if res, err := http.Get(srv.URL + "/robots.txt"); err != nil || res.StatusCode != 200 {
		t.Fatalf("robots.txt: %v %v", res, err)
	} else if body, _ := io.ReadAll(res.Body); string(body) != "User-agent: *\nDisallow: /\n" {
		t.Fatalf("robots.txt: %q", body)
	}

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}

	if code, _ := postJSON(t, browser, srv.URL+"/v1/hosts", "", map[string]string{"name": "h"}); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create host: %d", code)
	}
	if code, _ := postJSON(t, browser, srv.URL+"/v1/hosts/x/threads", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated relay: %d", code)
	}
	if code, _ := postJSON(t, browser, srv.URL+"/v1/auth/login", "", map[string]string{"username": "admin", "password": "nope"}); code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", code)
	}
	if code, body := postJSON(t, browser, srv.URL+"/v1/auth/login", "", map[string]string{"username": "admin", "password": "pw"}); code != 200 || body["name"] != "admin" {
		t.Fatalf("login: %d %v", code, body)
	}

	// The session cookie now signs requests; mint an API token for the scripted part.
	_, tok := postJSON(t, browser, srv.URL+"/v1/tokens", "", map[string]string{"name": "ci"})
	apiToken, _ := tok["secret"].(string)
	if !strings.HasPrefix(apiToken, "inaba_api_") {
		t.Fatalf("create token: %v", tok)
	}
	plain := &http.Client{}
	_, created := postJSON(t, plain, srv.URL+"/v1/hosts", apiToken, map[string]any{"name": "laptop", "shared": true})
	hostID, _ := created["host"].(map[string]any)["id"].(string)
	hostSecret, _ := created["secret"].(string)

	// A host secret is not a user credential.
	if code, _ := postJSON(t, plain, srv.URL+"/v1/hosts", hostSecret, map[string]string{"name": "x"}); code != http.StatusUnauthorized {
		t.Fatalf("host secret used as a user token: %d", code)
	}
	if code, _ := postJSON(t, plain, srv.URL+"/v1/hosts/"+hostID+"/threads", apiToken, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("relay to an offline host: %d", code)
	}
	// Said apart from an error of the host's own, so the web waits for the host rather than show it.
	offline, _ := http.NewRequest("POST", srv.URL+"/v1/hosts/"+hostID+"/threads", nil)
	offline.Header.Set("Authorization", "Bearer "+apiToken)
	if res, err := plain.Do(offline); err != nil || res.Header.Get(unreachableHeader) != "unreachable" {
		t.Fatalf("relay to an offline host does not say the host was unreachable: %v %v", res, err)
	}

	h := host.New(echoProvider{})
	defer h.Close()
	hostHandler, _ := h.Handler()
	mods, err := modules.Start(h, modules.Builtin)
	if err != nil {
		t.Fatal(err)
	}
	defer mods.Close()
	// The host must never see the user's credentials.
	// The server opens several streams to the host at once, so the handler runs concurrently.
	var leakedMu sync.Mutex
	var leaked string
	l, err := tunnel.Dial(ctx, srv.URL, hostSecret)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedMu.Lock()
		leaked += r.Header.Get("Authorization") + r.Header.Get("Cookie")
		leakedMu.Unlock()
		hostHandler.ServeHTTP(w, r)
	}))
	for online := false; !online; time.Sleep(10 * time.Millisecond) {
		res, _ := s.ListHosts(ctx, nil)
		online = res.Hosts[0].Online
		// The host says what it runs on as it connects.
		if online && (res.Hosts[0].Os != runtime.GOOS || res.Hosts[0].Arch != runtime.GOARCH) {
			t.Fatalf("platform: %q %q", res.Hosts[0].Os, res.Hosts[0].Arch)
		}
	}
	// What it runs on is asked once it connects, and kept, with when it was here, for while it is away.
	for i := 0; ; i++ {
		res, _ := s.ListHosts(ctx, nil)
		if res.Hosts[0].GetSystem().GetHostname() != "" && res.Hosts[0].LastSeen != nil {
			break
		}
		if i == 200 {
			t.Fatalf("the host's system was never recorded: %v", res.Hosts[0])
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Typed client through the relay, as the web UI uses it.
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+apiToken)
	threads := inabav1connect.NewThreadServiceClient(connect.NewClient(connecthttp.NewTransport(plain, srv.URL+"/hosts/"+hostID)))
	cwd := t.TempDir()
	th, err := threads.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "echo", Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	// A module's own REST route goes through the same relay: the files panel's downloads and pictures.
	if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("raw"), 0o644); err != nil {
		t.Fatal(err)
	}
	rawReq, _ := http.NewRequest("GET", srv.URL+"/v1/hosts/"+hostID+"/threads/"+th.Id+"/raw?path=a.txt", nil)
	rawReq.Header.Set("Authorization", "Bearer "+apiToken)
	if res, err := plain.Do(rawReq); err != nil {
		t.Fatal(err)
	} else if b, _ := io.ReadAll(res.Body); res.StatusCode != 200 || string(b) != "raw" {
		res.Body.Close()
		t.Fatalf("raw file through the relay: %d %q", res.StatusCode, b)
	} else {
		res.Body.Close()
	}
	// REST through the relay, as a script uses it.
	if code, body := postJSON(t, plain, srv.URL+"/v1/hosts/"+hostID+"/threads/"+th.Id+"/turns", apiToken, map[string]string{"text": "hi"}); code != 200 {
		t.Fatalf("start turn over REST: %d %v", code, body)
	}
	stream, err := threads.Subscribe(ctx, &inabav1.SubscribeRequest{ThreadId: th.Id})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var text string
	for {
		ev, err := stream.Receive()
		if err != nil {
			t.Fatal(err)
		}
		text += ev.GetTextDelta().GetText()
		if ev.GetTurnEnded() != nil {
			break
		}
	}
	// The host's API has one address: the old nested form is gone.
	if code, _ := postJSON(t, plain, srv.URL+"/hosts/"+hostID+"/v1/threads", apiToken, nil); code != http.StatusNotFound {
		t.Fatalf("nested version path: %d", code)
	}
	if text != "echo hi" {
		t.Fatalf("relayed text = %q", text)
	}
	leakedMu.Lock()
	seen := leaked
	leakedMu.Unlock()
	if seen != "" {
		t.Fatalf("credentials reached the host: %q", seen)
	}

	// An automation's webhook takes calls from outside, with no account: only its secret.
	code, made := postJSON(t, plain, srv.URL+"/v1/hosts/"+hostID+"/automations", apiToken, map[string]any{
		"name": "hook", "prompt": "got {{payload}}", "enabled": true, "webhook": true, "threadId": th.Id})
	hookSecret, _ := made["webhookSecret"].(string)
	hookID, _ := made["automation"].(map[string]any)["id"].(string)
	if code != 200 || hookSecret == "" {
		t.Fatalf("create automation over REST: %d %v", code, made)
	}
	if code, _ := postJSON(t, plain, srv.URL+"/hooks/"+hostID+"/"+hookID, apiToken, map[string]int{"n": 1}); code != http.StatusUnauthorized {
		t.Fatalf("webhook called with a user token: %d", code)
	}
	if code, _ := postJSON(t, plain, srv.URL+"/hooks/"+hostID+"/"+hookID, hookSecret, map[string]int{"n": 1}); code != http.StatusAccepted {
		t.Fatalf("webhook: %d", code)
	}
	for text = ""; !strings.Contains(text, `echo got {"n":1}`); time.Sleep(10 * time.Millisecond) {
		res, _ := threads.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
		text = ""
		for _, e := range res.Events {
			text += e.GetTextDelta().GetText()
		}
	}
	leakedMu.Lock()
	seenAgain := leaked
	leakedMu.Unlock()
	if seenAgain != "" {
		t.Fatalf("credentials reached the host: %q", seenAgain)
	}

	// Logging out ends the cookie session.
	if code, _ := postJSON(t, browser, srv.URL+"/v1/auth/logout", "", nil); code != 200 {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := postJSON(t, browser, srv.URL+"/v1/tokens", "", map[string]string{"name": "x"}); code != http.StatusUnauthorized {
		t.Fatalf("session still valid after logout: %d", code)
	}
}
