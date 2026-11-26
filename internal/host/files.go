package host

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/workflow"
)

func (h *Host) ForkThread(ctx context.Context, req *inabav1.ForkThreadRequest) (*inabav1.Thread, error) {
	src, err := h.thread(req.ThreadId)
	if err != nil {
		return nil, err
	}
	meta := src.snapshot()
	src.mu.Lock()
	carried, err := forkEvents(current(src.events), req.Seq)
	src.mu.Unlock()
	if err != nil {
		return nil, err
	}
	create := &inabav1.CreateThreadRequest{Provider: meta.Provider, ProjectId: meta.ProjectId, Cwd: meta.Cwd, Title: cmpOr(req.Title, meta.Title), Options: map[string]string{}}
	for _, o := range meta.Options {
		create.Options[o.Id] = o.Value
	}
	if meta.Worktree {
		head, err := git(ctx, meta.Cwd, "rev-parse", "HEAD")
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, "git could not read the worktree's commit: "+err.Error())
		}
		create.Worktree = &inabav1.Worktree{Base: strings.TrimSpace(head)}
	}
	fork, err := h.CreateThread(ctx, create)
	if err != nil {
		return nil, err
	}
	if meta.Worktree {
		// The new worktree starts at the commit; put the files as they were when the last
		// carried turn ended, uncommitted ones included, so they match the conversation.
		var asked uint64
		for _, e := range carried {
			if e.GetUserMessage() != nil {
				asked = e.Seq
			}
		}
		if after, err := git(ctx, meta.Cwd, "rev-parse", "--verify", "-q", turnRef(meta.Id, asked)+"-after"); asked > 0 && err == nil {
			if _, err := restoreFiles(ctx, fork, strings.TrimSpace(after)); err != nil {
				slog.Warn("the fork's worktree keeps the commit's files", "thread", fork.Id, "reason", err)
			}
		}
	}
	t, err := h.thread(fork.Id)
	if err != nil {
		return nil, err
	}
	// What already happened in the original is history to the modules, not news.
	t.mu.Lock()
	t.copying = true
	t.mu.Unlock()
	for _, e := range carried {
		c := proto.Clone(e).(*inabav1.Event)
		// The original thread already counts the turn and what it used.
		if ended := c.GetTurnEnded(); ended != nil {
			ended.Usage, ended.Copied = nil, true
		}
		t.emit(&inabav1.Event{Payload: c.Payload, Time: c.Time})
	}
	t.mu.Lock()
	t.copying = false
	// The copied events carry their old times; the fork itself is new.
	t.meta.UpdatedAt = timestamppb.Now()
	t.meta.Rewound = true
	t.save.meta(t.meta)
	t.mu.Unlock()
	return t.snapshot(), nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// forkEvents returns the conversation through the end of the turn holding seq, less
// what describes the agent session rather than the conversation: a fork has its own.
func forkEvents(events []*inabav1.Event, seq uint64) ([]*inabav1.Event, error) {
	at := slices.IndexFunc(events, func(e *inabav1.Event) bool { return e.Seq == seq })
	if at < 0 {
		return nil, connect.NewError(connect.CodeNotFound, "no event at that seq in the conversation")
	}
	end := len(events)
	if next := slices.IndexFunc(events[at+1:], func(e *inabav1.Event) bool { return e.GetUserMessage() != nil }); next >= 0 {
		end = at + 1 + next
	}
	var out []*inabav1.Event
	open := false
	for _, e := range events[:end] {
		switch e.Payload.(type) {
		case *inabav1.Event_OptionsChanged, *inabav1.Event_CommandsChanged, *inabav1.Event_UsageChanged, *inabav1.Event_ModeChanged, *inabav1.Event_Raw, *inabav1.Event_LayoutChanged:
			continue
		case *inabav1.Event_TurnStarted:
			open = true
		case *inabav1.Event_TurnEnded:
			open = false
		}
		out = append(out, e)
	}
	if open {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "that turn is still running; fork once it has ended")
	}
	return out, nil
}

// workflowDir finds the folder of the workflow a thread's call started, from the thread's own
// record of the call, never from a request.
func (h *Host) workflowDir(threadID, toolCallID string) (string, error) {
	t, err := h.thread(threadID)
	if err != nil {
		return "", err
	}
	dir := ""
	t.mu.Lock()
	for _, e := range t.events {
		if c := e.GetToolCall(); c != nil && c.Id == toolCallID && c.Raw != nil {
			var update struct {
				Meta map[string]json.RawMessage `json:"_meta"`
			}
			if b, err := protojson.Marshal(c.Raw); err == nil && json.Unmarshal(b, &update) == nil {
				dir = cmpOr(dir, workflow.Dir(update.Meta))
			}
		}
	}
	t.mu.Unlock()
	if dir == "" {
		return "", connect.NewError(connect.CodeNotFound, "that call started no workflow")
	}
	return dir, nil
}

func agentTranscript(dir, agentID string) (*inabav1.GetWorkflowAgentResponse, error) {
	steps, err := workflow.Transcript(dir, agentID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "no transcript for that agent: "+err.Error())
	}
	return &inabav1.GetWorkflowAgentResponse{Steps: steps}, nil
}

func (h *Host) GetWorkflowAgent(_ context.Context, req *inabav1.GetWorkflowAgentRequest) (*inabav1.GetWorkflowAgentResponse, error) {
	dir, err := h.workflowDir(req.ThreadId, req.ToolCallId)
	if err != nil {
		return nil, err
	}
	return agentTranscript(dir, req.AgentId)
}

// workflowPoll is how often a watched agent's transcript is read again.
var workflowPoll = 2 * time.Second

func (h *Host) WatchWorkflowAgent(ctx context.Context, req *inabav1.GetWorkflowAgentRequest, stream inabav1connect.ThreadServiceWatchWorkflowAgentServerStream) error {
	dir, err := h.workflowDir(req.ThreadId, req.ToolCallId)
	if err != nil {
		return err
	}
	var last *inabav1.GetWorkflowAgentResponse
	for {
		// Done is read before the transcript, so the last one sent holds everything.
		done := slices.ContainsFunc(workflow.Agents(dir), func(a *inabav1.WorkflowAgent) bool { return a.Id == req.AgentId && a.Done })
		res, err := agentTranscript(dir, req.AgentId)
		if err != nil {
			return err
		}
		if !proto.Equal(res, last) {
			if err := stream.Send(res); err != nil {
				return err
			}
			last = res
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(workflowPoll):
		}
	}
}
