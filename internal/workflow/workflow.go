// Package workflow reads the files of a Claude Code background workflow (ultracode): its
// journal of agents and each agent's own transcript. ACP says nothing about a workflow
// while it runs, so these files are how inaba shows one.
//
// shortcut: Claude Code's own file layout, read as it is today; it has no stable format, so
// a change there leaves a call without agents rather than failing anything.
package workflow

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// Dir is the folder of a background workflow, from a Claude Code tool call's _meta, or ""
// for any other call.
func Dir(meta map[string]json.RawMessage) string {
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

// Agents reads the agents from the journal, in the order they started, with what each
// running one is doing now.
func Agents(dir string) []*inabav1.WorkflowAgent {
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
			a.Done, a.Result = true, oneLine(resultText(line.Result), 300)
		}
	}
	for _, a := range agents {
		if !validID(a.Id) {
			continue
		}
		path := filepath.Join(dir, "agent-"+a.Id+".jsonl")
		a.StartedAt = startedAt(path)
		if a.Done {
			a.EndedAt = lastWrite(path)
		} else {
			a.Activity = latestActivity(path)
		}
	}
	return agents
}

// Stop marks the agents that never finished as stopped, at their last write, for a workflow
// no longer watched; the others are returned as they are.
func Stop(dir string, agents []*inabav1.WorkflowAgent) []*inabav1.WorkflowAgent {
	out := make([]*inabav1.WorkflowAgent, len(agents))
	for i, a := range agents {
		out[i] = a
		if a.Done || a.Stopped {
			continue
		}
		c := proto.Clone(a).(*inabav1.WorkflowAgent)
		c.Stopped, c.Activity = true, ""
		if validID(a.Id) {
			c.EndedAt = lastWrite(filepath.Join(dir, "agent-"+a.Id+".jsonl"))
		}
		out[i] = c
	}
	return out
}

// startedAt is when an agent began: the time of its transcript's first entry.
func startedAt(path string) *timestamppb.Timestamp {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	// The first entry is the task, which can be long, but is one line.
	line, _ := bufio.NewReaderSize(f, 64<<10).ReadBytes('\n')
	var e struct {
		Timestamp time.Time `json:"timestamp"`
	}
	if json.Unmarshal(line, &e) != nil || e.Timestamp.IsZero() {
		return nil
	}
	return timestamppb.New(e.Timestamp)
}

// lastWrite is when an agent's transcript last changed, which for a finished agent is when it finished.
func lastWrite(path string) *timestamppb.Timestamp {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return timestamppb.New(st.ModTime())
}

// validID keeps an agent id from naming a file outside the folder.
func validID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\.`)
}

// resultText is an agent's result as text: a string as it is, an object by its summary if
// it has one, else as JSON.
func resultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var obj struct {
		Summary *string `json:"summary"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Summary != nil {
		return *obj.Summary
	}
	return string(raw)
}

// oneLine folds whitespace and cuts text at n characters.
func oneLine(s string, n int) string {
	return shorten(strings.Join(strings.Fields(s), " "), n)
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// An entry of an agent's transcript, as far as it is read here.
type entry struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// blocks is a message's content as blocks; a plain string is one text block.
func (e entry) blocks() []block {
	var text string
	if json.Unmarshal(e.Message.Content, &text) == nil {
		return []block{{Type: "text", Text: text}}
	}
	var out []block
	_ = json.Unmarshal(e.Message.Content, &out)
	return out
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
		var e entry
		if json.Unmarshal(lines[i], &e) != nil || e.Type != "assistant" {
			continue
		}
		bs := e.blocks()
		for j := len(bs) - 1; j >= 0; j-- {
			if bs[j].Type == "tool_use" {
				return oneLine(bs[j].Name+brief(bs[j].Input, ": "), 120)
			}
		}
	}
	return ""
}

// brief is the part of a tool's input that says what it is doing, after sep.
func brief(input map[string]any, sep string) string {
	for _, key := range []string{"description", "command", "file_path", "pattern", "path", "url", "query", "prompt"} {
		if v, ok := input[key].(string); ok && v != "" {
			return sep + v
		}
	}
	return ""
}

// Limits on what a transcript returns, so a long-running agent stays a reasonable read.
// What the agent was asked and said is sent whole: it is markdown, which cut short shows wrong.
const (
	maxSteps  = 400
	maxOutput = 8 << 10
)

// harness starts the messages Claude Code's workflow harness wraps a task in.
const harness = "[Workflow harness"

// Transcript is an agent's conversation so far: the task, what it said, and its tool calls
// with what each gave back. Its newest steps are kept when there are too many.
func Transcript(dir, agentID string) ([]*inabav1.WorkflowStep, error) {
	if !validID(agentID) {
		return nil, errors.New("no such agent")
	}
	f, err := os.Open(filepath.Join(dir, "agent-"+agentID+".jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr := transcript{tools: map[string]*inabav1.WorkflowStep{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var e entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		// Other entries (attachments, API bookkeeping) are not the conversation.
		switch e.Type {
		case "user":
			tr.user(e.blocks())
		case "assistant":
			tr.assistant(e.blocks())
		}
	}
	steps := tr.steps
	if len(steps) > maxSteps {
		steps = steps[len(steps)-maxSteps:]
	}
	return steps, sc.Err()
}

// transcript builds an agent's steps, entry by entry.
type transcript struct {
	steps []*inabav1.WorkflowStep
	// The tool calls by id, for their results to find.
	tools map[string]*inabav1.WorkflowStep
}

func (tr *transcript) add(s *inabav1.WorkflowStep) { tr.steps = append(tr.steps, s) }

// user takes what the harness sent the agent: its task, and what its tools gave back.
func (tr *transcript) user(blocks []block) {
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if task, ok := taskText(b.Text); ok {
				tr.add(&inabav1.WorkflowStep{Kind: "prompt", Text: task})
			}
		case "tool_result":
			if t := tr.tools[b.ToolUseID]; t != nil {
				t.Output, t.Failed = shorten(resultBody(b.Content), maxOutput), b.IsError
			}
		}
	}
}

// assistant takes what the agent said and the tools it called; its thinking stays out.
func (tr *transcript) assistant(blocks []block) {
	for _, b := range blocks {
		switch {
		case b.Type == "text" && strings.TrimSpace(b.Text) != "":
			tr.add(&inabav1.WorkflowStep{Kind: "text", Text: b.Text})
		case b.Type == "tool_use" && b.Name == "StructuredOutput":
			// How a workflow agent answers: its result, said rather than called.
			out, _ := json.Marshal(b.Input)
			tr.add(&inabav1.WorkflowStep{Kind: "text", Text: resultText(out)})
		case b.Type == "tool_use":
			t := &inabav1.WorkflowStep{Kind: "tool", Tool: b.Name, Input: oneLine(brief(b.Input, ""), 300)}
			tr.tools[b.ID] = t
			tr.add(t)
		}
	}
}

// taskText is the task of a user message: the harness relays the user's own request,
// which the thread already shows, and then the task with a notice before it.
func taskText(text string) (string, bool) {
	if !strings.HasPrefix(text, harness) {
		return text, strings.TrimSpace(text) != ""
	}
	if strings.Contains(text[:min(len(text), 80)], "user request") {
		return "", false
	}
	// The task follows the notice, every line of it indented by two spaces.
	_, task, ok := strings.Cut(text, "follows:\n")
	if !ok {
		return text, true
	}
	lines := strings.Split(task, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, "  ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), true
}

// resultBody is a tool result's text, from a string or a list of text blocks.
func resultBody(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []block
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
