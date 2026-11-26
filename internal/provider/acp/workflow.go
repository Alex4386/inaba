package acp

import (
	"time"

	"google.golang.org/protobuf/proto"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/workflow"
)

// Claude Code runs a workflow (ultracode) in the background: the tool call returns at once,
// and ACP says nothing more until the agent reports the result. The host reads the
// workflow's files and shows its agents as updates of the call that started it.

const (
	workflowPoll = 2 * time.Second
	// The journal has no end marker: once every agent has a result and none has started
	// for this long, the workflow is taken to be over.
	workflowSettle = time.Minute
	// A workflow that writes nothing for this long is left alone.
	workflowIdle = 30 * time.Minute
)

// watchWorkflow sends the workflow's agents as updates of the call id until it is over,
// idle, or the session closes.
func (s *session) watchWorkflow(id, dir string) {
	tick := time.NewTicker(workflowPoll)
	defer tick.Stop()
	var last []*inabav1.WorkflowAgent
	changed, settled := time.Now(), time.Time{}
	for {
		select {
		case <-s.closed:
			return
		case <-tick.C:
		}
		agents := workflow.Agents(dir)
		if !agentsEqual(agents, last) {
			last, changed = agents, time.Now()
			s.emit(&inabav1.Event{Payload: &inabav1.Event_ToolCall{ToolCall: &inabav1.ToolCall{Id: id, Agents: agents}}})
		}
		allDone := len(agents) > 0
		for _, a := range agents {
			allDone = allDone && a.Done
		}
		switch {
		case !allDone:
			settled = time.Time{}
		case settled.IsZero():
			settled = time.Now()
		case time.Since(settled) > workflowSettle:
			return
		}
		if time.Since(changed) > workflowIdle {
			return
		}
	}
}

func agentsEqual(a, b []*inabav1.WorkflowAgent) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !proto.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}
