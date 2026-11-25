package host

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
)

func TestSummarizeUsage(t *testing.T) {
	seoul := time.FixedZone("KST", 9*60*60)
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, seoul)
	to := time.Date(2026, 11, 3, 23, 0, 0, 0, seoul)
	at := func(day, hour int) *timestamppb.Timestamp {
		return timestamppb.New(time.Date(2026, 11, day, hour, 0, 0, 0, seoul))
	}
	cost := func(ts *timestamppb.Timestamp, c float64, limits ...*inabav1.UsageLimit) *inabav1.Event {
		return &inabav1.Event{Time: ts, Payload: &inabav1.Event_UsageChanged{UsageChanged: &inabav1.UsageChanged{Usage: &inabav1.Usage{Cost: c, Currency: "USD", Limits: limits}}}}
	}
	turn := func(ts *timestamppb.Timestamp, in, out uint64) *inabav1.Event {
		return &inabav1.Event{Time: ts, Payload: &inabav1.Event_TurnEnded{TurnEnded: &inabav1.TurnEnded{Usage: &inabav1.TurnUsage{InputTokens: in, OutputTokens: out, TotalTokens: in + out}}}}
	}
	weekly := &inabav1.UsageLimit{Name: "weekly", UsedFraction: 0.4}
	logs := []usageLog{
		{meta: &inabav1.Thread{Id: "a", Title: "A", ProjectId: "p1", Provider: "codex"}, events: []*inabav1.Event{
			cost(timestamppb.New(from.Add(-time.Hour)), 1.0),      // before the window: only sets the running total
			turn(timestamppb.New(from.Add(-time.Hour)), 999, 999), // before the window: left out
			{Time: at(1, 10), Payload: &inabav1.Event_UsageChanged{UsageChanged: &inabav1.UsageChanged{Usage: &inabav1.Usage{Used: 5, Size: 10}}}}, // no cost: says nothing
			cost(at(1, 11), 1.5), // +0.5 on claude
			turn(at(1, 11), 100, 10),
			{Time: at(2, 9), Payload: &inabav1.Event_ProviderChanged{ProviderChanged: &inabav1.ProviderChanged{Provider: "codex", From: "claude"}}},
			cost(at(2, 10), 2.0, weekly), // the new provider's session counts from zero: +2.0 on codex, though above claude's 1.5
			turn(at(2, 10), 200, 20),
			turn(timestamppb.New(to.Add(time.Hour)), 999, 999), // after the window: left out
		}},
		{meta: &inabav1.Thread{Id: "b", Title: "B", Provider: "claude"}, events: []*inabav1.Event{
			// 23:30 UTC on Nov 2 is Nov 3 in Seoul.
			turn(timestamppb.New(time.Date(2026, 11, 2, 23, 30, 0, 0, time.UTC)), 1, 1),
		}},
		{meta: &inabav1.Thread{Id: "idle", Provider: "claude"}},
	}
	s := summarizeUsage(logs, from, to, seoul)

	if s.Currency != "USD" || s.Totals.Cost != 2.5 || s.Totals.InputTokens != 301 || s.Totals.Turns != 3 || s.Totals.Threads != 2 {
		t.Fatalf("totals: %s %+v", s.Currency, s.Totals)
	}
	if len(s.Days) != 3 || s.Days[0].Date != "2026-11-01" || s.Days[2].Date != "2026-11-03" {
		t.Fatalf("days: %v", s.Days)
	}
	if d := s.Days[0].Totals; d.Cost != 0.5 || d.TotalTokens != 110 || d.Threads != 1 {
		t.Fatalf("day 1: %+v", d)
	}
	if d := s.Days[2].Totals; d.Turns != 1 || d.TotalTokens != 2 {
		t.Fatalf("day 3 should hold thread b's late-UTC turn: %+v", d)
	}
	if p := s.Days[1].Providers; len(p) != 1 || p[0].Id != "codex" || p[0].Totals.Cost != 2.0 || p[0].Totals.TotalTokens != 220 {
		t.Fatalf("day 2 by provider: %v", p)
	}
	if p := s.Days[0].Providers; len(p) != 1 || p[0].Id != "claude" {
		t.Fatalf("day 1 by provider: %v", p)
	}
	byID := map[string]*inabav1.UsageTotals{}
	for _, g := range s.Providers {
		byID[g.Id] = g.Totals
	}
	if byID["claude"].Cost != 0.5 || byID["claude"].Threads != 2 || byID["codex"].Cost != 2.0 || byID["codex"].TotalTokens != 220 {
		t.Fatalf("providers: %v", s.Providers)
	}
	if len(s.Projects) != 2 || s.Projects[0].Id != "p1" || s.Projects[1].Id != "" {
		t.Fatalf("projects: %v", s.Projects)
	}
	if len(s.TopThreads) != 2 || s.TopThreads[0].ThreadId != "a" || s.TopThreads[0].Totals.Cost != 2.5 {
		t.Fatalf("top threads: %v", s.TopThreads)
	}
	if len(s.Limits) != 1 || s.Limits[0].Provider != "codex" || s.Limits[0].Limits[0].UsedFraction != 0.4 {
		t.Fatalf("limits: %v", s.Limits)
	}
}

func TestUsageSummaryWindow(t *testing.T) {
	srv, threads, _ := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	th, err := threads.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threads.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	usage := inabav1connect.NewUsageServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL)))
	var s *inabav1.UsageSummary
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if s, err = usage.GetUsageSummary(ctx, &inabav1.GetUsageSummaryRequest{}); err != nil {
			t.Fatal(err)
		}
		if s.Totals.Turns == 1 || time.Now().After(deadline) {
			break
		}
	}
	if s.Totals.Turns != 1 || s.Totals.Threads != 1 || s.Providers[0].Id != "fake" {
		t.Fatalf("a turn without reported usage still counts as a turn: %+v", s.Totals)
	}
	// A fork's copies of the original's turns are history: the turn is still counted once.
	ended, err := threads.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threads.ForkThread(ctx, &inabav1.ForkThreadRequest{ThreadId: th.Id, Seq: ended.LastSeq}); err != nil {
		t.Fatal(err)
	}
	if s, err = usage.GetUsageSummary(ctx, &inabav1.GetUsageSummaryRequest{}); err != nil || s.Totals.Turns != 1 || s.Totals.Threads != 1 {
		t.Fatalf("a fork counted the copied turn again: %+v %v", s.GetTotals(), err)
	}
	if got := s.To.AsTime().Sub(s.From.AsTime()); got != usageDefaultWindow {
		t.Fatalf("default window: %v", got)
	}

	to := time.Now()
	s, err = usage.GetUsageSummary(ctx, &inabav1.GetUsageSummaryRequest{From: timestamppb.New(to.AddDate(-1, 0, 0)), To: timestamppb.New(to)})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.To.AsTime().Sub(s.From.AsTime()); got != usageMaxWindow || len(s.Days) > 92 {
		t.Fatalf("a year should be cut to 90 days: %v, %d days", got, len(s.Days))
	}
	if _, err := usage.GetUsageSummary(ctx, &inabav1.GetUsageSummaryRequest{From: timestamppb.New(to), To: timestamppb.New(to.Add(-time.Hour))}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("from after to: %v", err)
	}

	// Served over REST too, where the relay reaches it.
	res, err := http.Get(srv.URL + "/v1/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Totals struct{ Turns int } `json:"totals"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&body) != nil || body.Totals.Turns != 1 {
		t.Fatalf("REST: %d %+v", res.StatusCode, body)
	}
}
