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

func TestChannels(t *testing.T) {
	var mu sync.Mutex
	var heard []string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		heard = append(heard, r.URL.Path+":"+r.Header.Get("X-Inaba-Event"))
		w.WriteHeader(status)
	}))
	defer srv.Close()
	// The receiver is on loopback, which only an operator's setting lets channels reach.
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db"), NotifyPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	s.senders = map[string]push.Sender{"web": &fakeSender{}}
	n := notifications{s: s}
	take := func() []string {
		s.deliveries.Wait()
		mu.Lock()
		defer mu.Unlock()
		out := heard
		heard = nil
		slices.Sort(out)
		return out
	}
	ann, _ := s.store.AddLocalUser("ann", "pw", false, nil)
	bob, _ := s.store.AddLocalUser("bob", "pw", false, nil)
	h, _, _ := s.store.AddHost("box", "")
	webhook := func(path string) *inabav1.NotificationChannel {
		return &inabav1.NotificationChannel{Label: path, Config: &inabav1.NotificationChannel_Webhook{Webhook: &inabav1.WebhookChannel{Url: srv.URL + path}}}
	}

	all, err := n.CreateChannel(as(ann), webhook("/all"))
	if err != nil || all.Secret == "" || all.Channel.Kind != "webhook" || !all.Channel.Enabled {
		t.Fatalf("create: %+v %v", all, err)
	}
	autos := webhook("/autos")
	autos.AutomationsOnly, autos.Events = true, []string{"turn.ended"}
	if _, err := n.CreateChannel(as(ann), autos); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*inabav1.NotificationChannel{{Label: "none"}, {Events: []string{"nope"}, Config: webhook("/x").Config}, {Config: &inabav1.NotificationChannel_Discord{Discord: &inabav1.DiscordChannel{WebhookUrl: srv.URL}}}} {
		if _, err := n.CreateChannel(as(ann), bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("bad channel %v: %v", bad, err)
		}
	}

	// Bob sees none of Ann's channels and cannot touch them.
	if res, _ := n.ListChannels(as(bob), &inabav1.ListChannelsRequest{}); len(res.Channels) != 0 {
		t.Fatalf("bob sees %v", res.Channels)
	}
	id := all.Channel.Id
	if _, err := n.DeleteChannel(as(bob), &inabav1.DeleteChannelRequest{Id: id}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob deletes: %v", err)
	}
	if _, err := n.UpdateChannel(as(bob), &inabav1.NotificationChannel{Id: id}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob updates: %v", err)
	}
	if _, err := n.TestChannel(as(bob), &inabav1.TestChannelRequest{Id: id}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob tests: %v", err)
	}

	// A finished turn goes to the channel for every thread; the other wants automations' only.
	s.fanOut(h.ID, &inabav1.Alert{Event: "turn.ended", ThreadId: "t1", ProjectId: "p1"})
	if got := take(); !slices.Equal(got, []string{"/all:turn.ended"}) {
		t.Fatalf("plain thread: %v", got)
	}
	s.fanOut(h.ID, &inabav1.Alert{Event: "turn.ended", ThreadId: "t1", ProjectId: "p1", Automation: true})
	if got := take(); !slices.Equal(got, []string{"/all:turn.ended", "/autos:turn.ended"}) {
		t.Fatalf("automation's thread: %v", got)
	}
	s.fanOut(h.ID, &inabav1.Alert{Event: "input.needed", ThreadId: "t1", ProjectId: "p1", Automation: true})
	if got := take(); !slices.Equal(got, []string{"/all:input.needed"}) {
		t.Fatalf("events filter: %v", got)
	}

	// A snooze mutes finished turns on channels too, but not questions.
	_ = s.store.SetSnooze(&store.Snooze{UserID: ann.ID, HostID: h.ID, Kind: "project", TargetID: "p1", CreatedAt: time.Now()})
	s.fanOut(h.ID, &inabav1.Alert{Event: "turn.ended", ThreadId: "t1", ProjectId: "p1"})
	if got := take(); len(got) != 0 {
		t.Fatalf("snoozed: %v", got)
	}
	s.fanOut(h.ID, &inabav1.Alert{Event: "input.needed", ThreadId: "t1", ProjectId: "p1"})
	if got := take(); !slices.Equal(got, []string{"/all:input.needed"}) {
		t.Fatalf("question through snooze: %v", got)
	}
	// Looking at the thread, Ann hears nothing anywhere.
	s.mu.Lock()
	s.present = map[string]*presence{ann.ID + "/c": {userID: ann.ID, hostID: h.ID, threadID: "t1", clientID: "c", seen: time.Now(), visible: true}}
	s.mu.Unlock()
	s.fanOut(h.ID, &inabav1.Alert{Event: "input.needed", ThreadId: "t1", ProjectId: "p1"})
	if got := take(); len(got) != 0 {
		t.Fatalf("viewing: %v", got)
	}

	// Turned off, it hears nothing but tests; a test's outcome is kept.
	if c, err := n.UpdateChannel(as(ann), &inabav1.NotificationChannel{Id: id, Label: "off"}); err != nil || c.Enabled || c.Label != "off" || c.GetWebhook().GetUrl() != srv.URL+"/all" {
		t.Fatalf("update: %+v %v", c, err)
	}
	s.fanOut(h.ID, &inabav1.Alert{Event: "turn.failed", ThreadId: "t2"})
	if got := take(); len(got) != 0 {
		t.Fatalf("turned off: %v", got)
	}
	status = http.StatusForbidden
	d, err := n.TestChannel(as(ann), &inabav1.TestChannelRequest{Id: id})
	if err != nil || d.Status != 403 || d.Error == "" || d.Event != "test" {
		t.Fatalf("test: %+v %v", d, err)
	}
	if got := take(); !slices.Equal(got, []string{"/all:test"}) {
		t.Fatalf("test heard: %v", got)
	}
	res, _ := n.ListChannels(as(ann), &inabav1.ListChannelsRequest{})
	if len(res.Channels) != 2 || res.Channels[0].LastDelivery.GetStatus() != 403 || res.Channels[0].LastDelivery.GetEvent() != "test" {
		t.Fatalf("recorded: %+v", res.Channels)
	}

	// At most 20 an account.
	for range store.MaxChannels - 2 {
		if _, err := n.CreateChannel(as(ann), webhook("/more")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := n.CreateChannel(as(ann), webhook("/more")); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("21st: %v", err)
	}
	if _, err := n.DeleteChannel(as(ann), &inabav1.DeleteChannelRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
}
