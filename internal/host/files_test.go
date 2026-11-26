package host

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
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

// A workflow agent's transcript is read from the folder the thread's own record of the call
// names, and only for a call that started a workflow.
func TestGetWorkflowAgent(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	dir := filepath.Join(t.TempDir(), "subagents", "workflows", "wf_1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "agent-a1.jsonl"), []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`+"\n"), 0o600)
	raw, err := structpb.NewValue(map[string]any{"_meta": map[string]any{"claudeCode": map[string]any{"toolResponse": map[string]any{"taskType": "local_workflow", "transcriptDir": dir}}}})
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := h.thread(th.Id)
	tr.emit(&inabav1.Event{Payload: &inabav1.Event_ToolCall{ToolCall: &inabav1.ToolCall{Id: "w", Title: "Workflow"}}})
	tr.emit(&inabav1.Event{Payload: &inabav1.Event_ToolCall{ToolCall: &inabav1.ToolCall{Id: "w", Raw: raw}}})
	tr.emit(&inabav1.Event{Payload: &inabav1.Event_ToolCall{ToolCall: &inabav1.ToolCall{Id: "plain", Title: "Bash"}}})

	res, err := h.GetWorkflowAgent(ctx, &inabav1.GetWorkflowAgentRequest{ThreadId: th.Id, ToolCallId: "w", AgentId: "a1"})
	if err != nil || len(res.Steps) != 1 || res.Steps[0].Text != "done" {
		t.Fatalf("transcript: %v %v", res, err)
	}
	for _, req := range []*inabav1.GetWorkflowAgentRequest{
		{ThreadId: th.Id, ToolCallId: "plain", AgentId: "a1"},
		{ThreadId: th.Id, ToolCallId: "w", AgentId: "../a1"},
		{ThreadId: th.Id, ToolCallId: "w", AgentId: "a2"},
	} {
		if _, err := h.GetWorkflowAgent(ctx, req); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%v: %v", req, err)
		}
	}
}

// Server streams come out over REST as JSON messages one after another, and as server-sent
// events to a client that asks for them.
func TestStreamsOverRESTAndSSE(t *testing.T) {
	srv, threads, _ := setup(t)
	ctx := context.Background()
	th, err := threads.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threads.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, threads, th.Id)
	get := func(path, accept string) (*http.Response, *bufio.Reader) {
		t.Helper()
		reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		t.Cleanup(cancel)
		req, _ := http.NewRequestWithContext(reqCtx, "GET", srv.URL+path, nil)
		req.Header.Set("Accept", accept)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res, bufio.NewReader(res.Body)
	}

	// Plain REST streaming: the first event as a JSON object.
	res, body := get("/v1/threads/"+th.Id+"/events/watch", "application/json")
	var first struct {
		Seq string `json:"seq"`
	}
	if err := json.NewDecoder(body).Decode(&first); err != nil || res.StatusCode != 200 || first.Seq != "1" {
		t.Fatalf("REST stream: %d %v %+v", res.StatusCode, err, first)
	}

	// SSE: one event per message, each a data line of JSON.
	res, body = get("/v1/threads/"+th.Id+"/events/watch?afterSeq=1", "text/event-stream")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE response: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	line, _ := body.ReadString('\n')
	blank, _ := body.ReadString('\n')
	var ev struct {
		Seq string `json:"seq"`
	}
	if !strings.HasPrefix(line, "data: ") || blank != "\n" || json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil || ev.Seq != "2" {
		t.Fatalf("SSE event: %q %q", line, blank)
	}

	// An error before the stream starts keeps its status, not an event stream.
	if res, _ := get("/v1/threads/nope/events/watch", "text/event-stream"); res.StatusCode != http.StatusNotFound || res.Header.Get("Content-Type") == "text/event-stream" {
		t.Fatalf("missing thread over SSE: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

// Watching a workflow agent sends its transcript as it grows and ends once it is done,
// which SSE reports with an end event.
func TestWatchWorkflowAgentOverSSE(t *testing.T) {
	workflowPoll = 20 * time.Millisecond
	t.Cleanup(func() { workflowPoll = 2 * time.Second })
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	handler, err := h.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(asServer(handler))
	t.Cleanup(srv.Close)
	ctx := Trusted(context.Background())
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	dir := filepath.Join(t.TempDir(), "subagents", "workflows", "wf_1")
	os.MkdirAll(dir, 0o700)
	agentFile := filepath.Join(dir, "agent-a1.jsonl")
	os.WriteFile(agentFile, []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"looking"}]}}`+"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(`{"type":"started","agentId":"a1","label":"x"}`+"\n"), 0o600)
	raw, _ := structpb.NewValue(map[string]any{"_meta": map[string]any{"claudeCode": map[string]any{"toolResponse": map[string]any{"taskType": "local_workflow", "transcriptDir": dir}}}})
	tr, _ := h.thread(th.Id)
	tr.emit(&inabav1.Event{Payload: &inabav1.Event_ToolCall{ToolCall: &inabav1.ToolCall{Id: "w", Raw: raw}}})

	reqCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, "GET", srv.URL+"/v1/threads/"+th.Id+"/tools/w/agents/a1/watch", nil)
	req.Header.Set("Accept", "text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body := bufio.NewReader(res.Body)
	next := func() string {
		t.Helper()
		var ev strings.Builder
		for {
			line, err := body.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended early: %v (%q)", err, ev.String())
			}
			if line == "\n" {
				return ev.String()
			}
			ev.WriteString(line)
		}
	}
	if ev := next(); !strings.Contains(ev, "looking") {
		t.Fatalf("first event: %q", ev)
	}
	// The agent says more and finishes.
	f, _ := os.OpenFile(agentFile, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"found it"}]}}` + "\n")
	f.Close()
	os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(`{"type":"started","agentId":"a1","label":"x"}`+"\n"+`{"type":"result","agentId":"a1","result":"ok"}`+"\n"), 0o600)
	if ev := next(); !strings.Contains(ev, "found it") {
		t.Fatalf("second event: %q", ev)
	}
	if ev := next(); !strings.HasPrefix(ev, "event: end\n") {
		t.Fatalf("last event: %q", ev)
	}
}
