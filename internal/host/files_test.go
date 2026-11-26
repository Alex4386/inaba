package host

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

func TestForkCarriesTheConversationThroughATurn(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir(), Title: "orig"})
	turn := func(id, text string) {
		t.Helper()
		if _, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: id, Text: text}); err != nil {
			t.Fatal(err)
		}
		for range 300 {
			if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: id}); got.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("turn did not end")
	}
	turn(th.Id, "first")
	turn(th.Id, "second")
	evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
	var answer uint64 // the reply to "first"
	for _, e := range evs.Events {
		if e.GetTextDelta() != nil && answer == 0 {
			answer = e.Seq
		}
	}

	fork, err := h.ForkThread(ctx, &inabav1.ForkThreadRequest{ThreadId: th.Id, Seq: answer})
	if err != nil || fork.Id == th.Id || fork.Title != "orig" || !fork.Rewound || fork.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE {
		t.Fatalf("fork: %v %v", fork, err)
	}
	if got := logLine(t, h, fork.Id); got != "user:first ended:end_turn" {
		t.Fatalf("the fork holds %q", got)
	}
	// Copied events keep their times, so a turn shows how long it took in the original.
	copied, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: fork.Id})
	timeOf := func(events []*inabav1.Event, match func(*inabav1.Event) bool) *timestamppb.Timestamp {
		i := slices.IndexFunc(events, match)
		if i < 0 {
			t.Fatal("no such event")
		}
		return events[i].Time
	}
	for _, match := range []func(*inabav1.Event) bool{
		func(e *inabav1.Event) bool { return e.GetUserMessage().GetText() == "first" },
		func(e *inabav1.Event) bool { return e.GetTurnEnded() != nil },
	} {
		if was, now := timeOf(evs.Events, match), timeOf(copied.Events, match); !proto.Equal(was, now) {
			t.Fatalf("copied event at %v, the original at %v", now.AsTime(), was.AsTime())
		}
	}
	turn(fork.Id, "third")
	evs, _ = h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: fork.Id})
	if reply := evs.Events[len(evs.Events)-2].GetTextDelta().GetText(); !strings.Contains(reply, "<user>\nfirst") || strings.Contains(reply, "second") {
		t.Fatalf("the fork's agent was sent %q", reply)
	}
	if got := logLine(t, h, th.Id); got != "user:first ended:end_turn user:second ended:end_turn" {
		t.Fatalf("the original changed: %q", got)
	}
	if _, err := h.ForkThread(ctx, &inabav1.ForkThreadRequest{ThreadId: th.Id, Seq: 999}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("fork at no event: %v", err)
	}
}

func TestForkRefusesARunningTurn(t *testing.T) {
	events := []*inabav1.Event{
		{Seq: 1, Payload: &inabav1.Event_UserMessage{UserMessage: &inabav1.UserMessage{Text: "a"}}},
		{Seq: 2, Payload: &inabav1.Event_TurnStarted{TurnStarted: &inabav1.TurnStarted{}}},
		{Seq: 3, Payload: &inabav1.Event_TextDelta{TextDelta: &inabav1.TextDelta{Text: "…"}}},
	}
	if _, err := forkEvents(events, 3); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("fork of a running turn: %v", err)
	}
}
