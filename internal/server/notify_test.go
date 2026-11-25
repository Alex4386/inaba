package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/push"
	"github.com/Alex4386/inaba/internal/store"
	"github.com/Alex4386/inaba/internal/testdir"
)

// fakeSender records who was pushed to, and answers ErrGone for endpoints marked gone.
type fakeSender struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeSender) Send(_ context.Context, d store.PushDevice, m push.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, d.UserID+":"+m.Body)
	if strings.HasSuffix(d.Endpoint, "/gone") {
		return push.ErrGone
	}
	return nil
}

func (f *fakeSender) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	slices.Sort(out)
	return out
}

func as(u *store.User) context.Context {
	return context.WithValue(context.Background(), ctxKey{}, &principal{user: u})
}

func TestFanOut(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSender{}
	s.senders = map[string]push.Sender{"web": f}
	ann, _ := s.store.AddLocalUser("ann", "pw", false, nil)
	bob, _ := s.store.AddLocalUser("bob", "pw", false, nil)
	h, _, _ := s.store.AddHost("box", "")
	for _, d := range []*store.PushDevice{
		{UserID: ann.ID, Platform: "web", Endpoint: "https://fcm.googleapis.com/ann"},
		{UserID: bob.ID, Platform: "web", Endpoint: "https://fcm.googleapis.com/bob"},
		{UserID: bob.ID, Platform: "web", Endpoint: "https://fcm.googleapis.com/gone"},
	} {
		if err := s.store.RegisterDevice(d); err != nil {
			t.Fatal(err)
		}
	}
	ended := &inabav1.Alert{Event: "turn.ended", ThreadId: "t1", ProjectId: "p1"}

	s.fanOut(h.ID, ended)
	want := []string{ann.ID + ":Finished", bob.ID + ":Finished", bob.ID + ":Finished"}
	slices.Sort(want)
	if got := f.take(); !slices.Equal(got, want) {
		t.Fatalf("everyone: %v", got)
	}
	// The push service said one of bob's was gone; it is forgotten.
	if ds, _ := s.store.Devices(bob.ID); len(ds) != 1 || ds[0].Endpoint != "https://fcm.googleapis.com/bob" {
		t.Fatalf("bob's devices: %+v", ds)
	}

	// Ann snoozes the project: a finished turn skips her, a question still reaches her.
	if _, err := (notifications{s: s}).SetSnooze(as(ann), &inabav1.Snooze{HostId: h.ID, Kind: inabav1.SnoozeKind_SNOOZE_KIND_PROJECT, TargetId: "p1", Until: timestamppb.New(time.Now().Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	s.fanOut(h.ID, ended)
	if got := f.take(); !slices.Equal(got, []string{bob.ID + ":Finished"}) {
		t.Fatalf("ann snoozed: %v", got)
	}
	s.fanOut(h.ID, &inabav1.Alert{Event: "input.needed", ThreadId: "t1", ProjectId: "p1"})
	want = []string{ann.ID + ":Needs your input", bob.ID + ":Needs your input"}
	slices.Sort(want)
	if got := f.take(); !slices.Equal(got, want) {
		t.Fatalf("input breaks through: %v", got)
	}

	// Bob has the thread open in a background tab: he does not see it, so he is pushed.
	failed := &inabav1.Alert{Event: "turn.failed", ThreadId: "t1", Error: "boom"}
	s.mu.Lock()
	s.present = map[string]*presence{bob.ID + "/c": {userID: bob.ID, hostID: h.ID, threadID: "t1", clientID: "c", seen: time.Now()}}
	s.mu.Unlock()
	s.fanOut(h.ID, failed)
	want = []string{ann.ID + ":Failed: boom", bob.ID + ":Failed: boom"}
	slices.Sort(want)
	if got := f.take(); !slices.Equal(got, want) {
		t.Fatalf("background tab: %v", got)
	}
	// On screen, he sees it already.
	s.mu.Lock()
	s.present[bob.ID+"/c"].visible = true
	s.mu.Unlock()
	s.fanOut(h.ID, failed)
	if got := f.take(); !slices.Equal(got, []string{ann.ID + ":Failed: boom"}) {
		t.Fatalf("viewer skipped: %v", got)
	}
	s.mu.Lock()
	s.present = nil
	s.mu.Unlock()

	// A thread snooze mutes bob's finished turns, until it wakes.
	if err := s.store.SetSnooze(&store.Snooze{UserID: bob.ID, HostID: h.ID, Kind: "thread", TargetID: "t1", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s.fanOut(h.ID, ended)
	if got := f.take(); len(got) != 0 {
		t.Fatalf("both snoozed: %v", got)
	}
	woke := time.Now().Add(-time.Minute)
	if err := s.store.SetSnooze(&store.Snooze{UserID: bob.ID, HostID: h.ID, Kind: "thread", TargetID: "t1", Until: &woke, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s.fanOut(h.ID, ended)
	if got := f.take(); !slices.Equal(got, []string{bob.ID + ":Finished"}) {
		t.Fatalf("woken snooze: %v", got)
	}

	// A deleted thread takes its snoozes along.
	_, _ = (notifications{s: s}).SetSnooze(as(ann), &inabav1.Snooze{HostId: h.ID, Kind: inabav1.SnoozeKind_SNOOZE_KIND_THREAD, TargetId: "t1"})
	s.fanOut(h.ID, &inabav1.Alert{Event: "thread.deleted", ThreadId: "t1"})
	if sn, _ := s.store.Snoozes(ann.ID, h.ID); len(sn) != 1 || sn[0].Kind != "project" {
		t.Fatalf("snoozes after delete: %+v", sn)
	}
}

func TestNotificationValidation(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	n := notifications{s: s}
	ann, _ := s.store.AddLocalUser("ann", "pw", false, nil)
	bob, _ := s.store.AddLocalUser("bob", "pw", false, nil)
	key := base64.RawURLEncoding.EncodeToString(append([]byte{4}, make([]byte, 64)...))
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	register := func(endpoint, p256dh, auth, label string) error {
		_, err := n.RegisterDevice(as(ann), &inabav1.RegisterDeviceRequest{Platform: inabav1.PushPlatform_PUSH_PLATFORM_WEB, Endpoint: endpoint, P256Dh: p256dh, Auth: auth, Label: label})
		return err
	}
	for _, bad := range []string{"https://evil.example/push", "http://fcm.googleapis.com/x", "https://fcm.googleapis.com:8443/x", "https://user@fcm.googleapis.com/x", "https://push.apple.com.evil.example/x", "https://169.254.169.254/latest"} {
		if err := register(bad, key, secret, ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if err := register("https://fcm.googleapis.com/x", "short", secret, ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad key: %v", err)
	}
	if err := register("https://fcm.googleapis.com/x", key, secret, strings.Repeat("a", 65)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("long label: %v", err)
	}
	if err := register("https://web.push.apple.com/x", key, secret, "Safari"); err != nil {
		t.Fatalf("good: %v", err)
	}

	// Bob's personal host is not Ann's to snooze, and is answered as missing.
	h, _, _ := s.store.AddHost("bobs", bob.ID)
	if _, err := n.SetSnooze(as(ann), &inabav1.Snooze{HostId: h.ID, Kind: inabav1.SnoozeKind_SNOOZE_KIND_THREAD, TargetId: "t"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("snooze on another's host: %v", err)
	}
	if _, err := n.ListSnoozes(as(ann), &inabav1.ListSnoozesRequest{HostId: h.ID}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("list on another's host: %v", err)
	}
	if _, err := n.SetSnooze(as(bob), &inabav1.Snooze{HostId: h.ID, Kind: inabav1.SnoozeKind_SNOOZE_KIND_THREAD, TargetId: strings.Repeat("t", 65)}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("long target: %v", err)
	}

	// A browser cannot reach the host's alert stream through the relay.
	handler, _ := s.Handler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	_, tok, _ := s.store.NewToken(store.KindAPI, bob.ID, "t", 0)
	req, _ := http.NewRequest("POST", srv.URL+"/hosts/"+h.ID+"/inaba.v1.AlertService/WatchAlerts", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/connect+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("alert stream through the relay: %d", res.StatusCode)
	}
}

func TestAlertLimit(t *testing.T) {
	s := &Server{}
	ended := &inabav1.Alert{Event: "turn.ended", ThreadId: "t1"}
	for range alertBurst {
		if !s.admitAlert("h", ended) {
			t.Fatal("dropped within the burst")
		}
	}
	// The budget is spent, and stays spent for a host that reconnects: the limiter is the server's.
	if s.admitAlert("h", ended) || s.admitAlert("h", &inabav1.Alert{Event: "thread.deleted", ThreadId: "t1"}) {
		t.Fatal("a finished turn or a deletion past the budget went out")
	}
	for _, ev := range []string{"input.needed", "turn.failed"} {
		if !s.admitAlert("h", &inabav1.Alert{Event: ev, ThreadId: "t1"}) {
			t.Fatalf("%s was dropped", ev)
		}
	}
	// Another host has its own budget.
	if !s.admitAlert("other", ended) {
		t.Fatal("one host's flood limited another")
	}
}

func TestAlertTags(t *testing.T) {
	// A later finished turn must not replace a question still on screen: one tag per kind of event.
	q := alertMessage("h", &inabav1.Alert{Event: "input.needed", ThreadId: "t1"})
	done := alertMessage("h", &inabav1.Alert{Event: "turn.ended", ThreadId: "t1"})
	failed := alertMessage("h", &inabav1.Alert{Event: "turn.failed", ThreadId: "t1"})
	if q.Tag == done.Tag || q.Tag == failed.Tag || done.Tag == failed.Tag {
		t.Fatalf("tags collide: %q %q %q", q.Tag, done.Tag, failed.Tag)
	}
	if !q.High || done.High {
		t.Fatalf("only a question stays on screen: %v %v", q.High, done.High)
	}
	if again := alertMessage("h", &inabav1.Alert{Event: "input.needed", ThreadId: "t1"}); again.Tag != q.Tag {
		t.Fatal("a newer question on the thread should replace the older one")
	}
}
