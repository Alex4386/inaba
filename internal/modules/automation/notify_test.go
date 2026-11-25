package automation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/rpc"
)

// receiver records the notifications sent to it, answering with status.
type receiver struct {
	mu     sync.Mutex
	got    []map[string]any
	tries  int
	status int
	secret string
	bad    bool
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tries++
	mac := hmac.New(sha256.New, []byte(r.secret))
	mac.Write(body)
	if req.Header.Get("X-Inaba-Signature-256") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || req.Header.Get("X-Inaba-Event") == "" {
		r.bad = true
	}
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	r.got = append(r.got, m)
	w.WriteHeader(r.status)
}

func (r *receiver) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.got {
		out = append(out, m["event"].(string))
	}
	return out
}

func TestNotifiersHearOfTurnsAndQuestions(t *testing.T) {
	saved := retryDelays
	retryDelays = []time.Duration{0, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryDelays = saved })

	h, s := setup(t, "")
	ctx := rpc.Trusted(context.Background())
	all, autos, failing := &receiver{status: 204}, &receiver{status: 204}, &receiver{status: 503}
	add := func(r *receiver, n *inabav1.Notifier) string {
		t.Helper()
		srv := httptest.NewServer(r)
		t.Cleanup(srv.Close)
		n.Url = srv.URL
		res, err := s.CreateNotifier(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		r.secret = res.Secret
		return res.Notifier.Id
	}
	add(all, &inabav1.Notifier{Events: []string{"turn.ended"}})
	add(autos, &inabav1.Notifier{AutomationsOnly: true})
	failID := add(failing, &inabav1.Notifier{Events: []string{"turn.failed"}})
	if _, err := s.CreateNotifier(ctx, &inabav1.Notifier{Url: "ftp://x"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a notifier without an http address: %v", err)
	}
	if _, err := s.CreateNotifier(ctx, &inabav1.Notifier{Url: "https://x", Events: []string{"nope"}}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a notifier for an unknown event: %v", err)
	}

	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir(), Title: "work"})
	if _, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	waitFor := func(r *receiver, n int) {
		t.Helper()
		for range 300 {
			if len(r.events()) >= n {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("got %v, want %d deliveries", r.events(), n)
	}
	waitFor(all, 1)
	all.mu.Lock()
	thread := all.got[0]["thread"].(map[string]any)
	if all.bad || thread["id"] != th.Id || thread["title"] != "work" || all.got[0]["stopReason"] != "end_turn" {
		t.Fatalf("delivery: %v (bad signature: %v)", all.got[0], all.bad)
	}
	all.mu.Unlock()

	// A fork's copy of the conversation is not news.
	evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
	if _, err := h.ForkThread(ctx, &inabav1.ForkThreadRequest{ThreadId: th.Id, Seq: evs.Events[0].Seq}); err != nil {
		t.Fatal(err)
	}

	// An automation's own thread reaches the automations-only notifier too.
	a, err := s.CreateAutomation(ctx, &inabav1.Automation{Name: "a", Prompt: "p", NewThread: &inabav1.NewThread{Provider: "fake", Cwd: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := s.RunAutomation(ctx, &inabav1.RunAutomationRequest{Id: a.Automation.Id})
	waitFor(autos, 1)
	waitFor(all, 2)
	autos.mu.Lock()
	if autos.bad || autos.got[0]["thread"].(map[string]any)["labels"].(map[string]any)["automation"] != a.Automation.Id || autos.got[0]["thread"].(map[string]any)["id"] != run.ThreadId {
		t.Fatalf("automation delivery: %v", autos.got)
	}
	autos.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	if got := all.events(); len(got) != 2 {
		t.Fatalf("the all-threads notifier got %v; the fork must add nothing", got)
	}

	// A receiver that keeps failing is tried three times, and the failure kept.
	d, err := s.TestNotifier(ctx, &inabav1.TestNotifierRequest{Id: failID})
	if err != nil || d.Status != 503 || d.Error == "" || d.Event != "test" {
		t.Fatalf("test delivery: %v %v", d, err)
	}
	failing.mu.Lock()
	tries := failing.tries
	failing.mu.Unlock()
	if tries != 3 {
		t.Fatalf("tried %d times", tries)
	}
	list, _ := s.ListNotifiers(ctx, &inabav1.ListNotifiersRequest{})
	// By id: they are listed by when they were made, which a coarse clock may not tell apart.
	i := slices.IndexFunc(list.Notifiers, func(n *inabav1.Notifier) bool { return n.Id == failID })
	if len(list.Notifiers) != 3 || i < 0 || len(list.Notifiers[i].Deliveries) != 1 || list.Notifiers[i].Deliveries[0].Status != 503 {
		t.Fatalf("recorded deliveries: %v", list.Notifiers)
	}
}
