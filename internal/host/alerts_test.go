package host

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/pkg/module"
)

// A finished turn comes out as an alert with the thread's title, and a deleted thread as thread.deleted.
func TestWatchAlerts(t *testing.T) {
	h := New(fakeProvider{})
	handler, err := h.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(asServer(handler))
	t.Cleanup(srv.Close)
	t.Cleanup(h.Close)
	client := connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
	threads := inabav1connect.NewThreadServiceClient(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The call may not return before its first message, so open it aside and wait until it listens.
	opened := make(chan inabav1connect.AlertServiceWatchAlertsClientStream, 1)
	go func() {
		watch, err := inabav1connect.NewAlertServiceClient(client).WatchAlerts(ctx, &inabav1.WatchAlertsRequest{})
		if err != nil {
			t.Error(err)
		}
		opened <- watch
	}()
	for listening := false; !listening; time.Sleep(10 * time.Millisecond) {
		h.bus.mu.Lock()
		listening = len(h.bus.subs) > 0
		h.bus.mu.Unlock()
	}

	th, err := threads.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threads.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	watch := <-opened
	defer watch.Close()
	al, err := watch.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if al.Event != "turn.ended" || al.ThreadId != th.Id || al.ThreadTitle != "hi" || al.Automation {
		t.Fatalf("alert = %v", al)
	}
	waitIdle(t, threads, th.Id)
	if _, err := threads.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: th.Id}); err != nil {
		t.Fatal(err)
	}
	if al, err = watch.Receive(); err != nil || al.Event != "thread.deleted" || al.ThreadId != th.Id {
		t.Fatalf("alert = %v, %v", al, err)
	}
}

// A question and a failure alert; a turn someone stopped does not.
func TestAlertFor(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	th := &inabav1.Thread{Id: "t1"}
	for _, c := range []struct {
		event *inabav1.Event
		want  string
	}{
		{&inabav1.Event{Payload: &inabav1.Event_PermissionRequest{PermissionRequest: &inabav1.PermissionRequest{}}}, "input.needed"},
		{&inabav1.Event{Payload: &inabav1.Event_TurnEnded{TurnEnded: &inabav1.TurnEnded{Error: "boom"}}}, "turn.failed"},
		{&inabav1.Event{Payload: &inabav1.Event_TurnEnded{TurnEnded: &inabav1.TurnEnded{StopReason: "cancelled"}}}, ""},
	} {
		got := ""
		if al := h.alertFor(module.ThreadEvent{Thread: th, Event: c.event}); al != nil {
			got = al.Event
		}
		if got != c.want {
			t.Fatalf("%v: %q", c.event, got)
		}
	}
}
