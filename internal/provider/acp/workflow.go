package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/coder/acp-go-sdk"
	"google.golang.org/protobuf/proto"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// Claude Code runs a workflow (ultracode) in the background: the tool call returns at
// once, and ACP says nothing more until the agent reports the result. Its journal and each
// agent's transcript are files on this machine, so the host reads them and shows the agents
// as updates of the call that started them.
//
// shortcut: Claude Code's own file layout, read as it is today; it has no stable format, so
// a change there leaves the call without agents rather than failing anything.

const (
	workflowPoll = 2 * time.Second
	// The journal has no end marker: once every agent has a result and none has started
	// for this long, the workflow is taken to be over.
	workflowSettle = time.Minute
	// A workflow that writes nothing for this long is left alone.
	workflowIdle = 30 * time.Minute
)

// workflowDir is the folder of a background workflow's journal, from a Claude Code tool
// call's _meta, or "" for any other call.
func workflowDir(meta sdk.Meta) string {
	var cc struct {
		ToolResponse struct {
			TaskType      string `json:"taskType"`
			TranscriptDir string `json:"transcriptDir"`
		} `json:"toolResponse"`
	}
	if raw, ok := meta["claudeCode"]; !ok || json.Unmarshal(raw, &cc) != nil || cc.ToolResponse.TaskType != "local_workflow" {
		return ""
	}
	dir := filepath.Clean(cc.ToolResponse.TranscriptDir)
	// Only a folder where Claude Code keeps workflows is read, whatever the call claims.
	sep := string(filepath.Separator)
	if !filepath.IsAbs(dir) || !strings.Contains(dir+sep, sep+"subagents"+sep+"workflows"+sep) {
		return ""
	}
	return dir
}

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
		agents := readWorkflow(dir)
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

// readWorkflow reads the agents from the journal, in the order they started, with what
// each running one is doing now.
func readWorkflow(dir string) []*inabav1.WorkflowAgent {
	f, err := os.Open(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var agents []*inabav1.WorkflowAgent
	byID := map[string]*inabav1.WorkflowAgent{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var line struct {
			Type    string          `json:"type"`
			AgentID string          `json:"agentId"`
			Label   string          `json:"label"`
			Phase   string          `json:"phase"`
			Result  json.RawMessage `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil || line.AgentID == "" {
			continue
		}
		a := byID[line.AgentID]
		if a == nil {
			a = &inabav1.WorkflowAgent{Id: line.AgentID}
			byID[line.AgentID] = a
			agents = append(agents, a)
		}
		switch line.Type {
		case "started":
			a.Label, a.Phase = line.Label, line.Phase
		case "result":
			a.Done, a.Result = true, resultText(line.Result)
		}
	}
	for _, a := range agents {
		if !a.Done && validAgentID(a.Id) {
			a.Activity = latestActivity(filepath.Join(dir, "agent-"+a.Id+".jsonl"))
		}
	}
	return agents
}

// validAgentID keeps a journal's agent id from naming a file outside the folder.
func validAgentID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\.`)
}

// resultText is an agent's result as one short line: a string as it is, an object by
// its summary if it has one, else as JSON.
func resultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var obj struct {
			Summary *string `json:"summary"`
		}
		if json.Unmarshal(raw, &obj) == nil && obj.Summary != nil {
			text = *obj.Summary
		} else {
			text = string(raw)
		}
	}
	return shorten(strings.Join(strings.Fields(text), " "), 300)
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// latestActivity is the newest tool call in an agent's transcript, read from its tail.
func latestActivity(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const tail = 256 << 10
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		_, _ = f.Seek(-tail, io.SeekEnd)
	}
	data, _ := io.ReadAll(f)
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type  string         `json:"type"`
					Name  string         `json:"name"`
					Input map[string]any `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &entry) != nil || entry.Type != "assistant" {
			continue
		}
		for j := len(entry.Message.Content) - 1; j >= 0; j-- {
			if c := entry.Message.Content[j]; c.Type == "tool_use" {
				return shorten(c.Name+toolBrief(c.Input), 120)
			}
		}
	}
	return ""
}

// toolBrief is the part of a tool's input that says what it is doing.
func toolBrief(input map[string]any) string {
	for _, key := range []string{"description", "command", "file_path", "pattern", "path", "url", "query"} {
		if v, ok := input[key].(string); ok && v != "" {
			return ": " + strings.Join(strings.Fields(v), " ")
		}
	}
	return ""
}
