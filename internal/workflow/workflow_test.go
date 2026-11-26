package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDirOnlyFromAWorkflowFolder(t *testing.T) {
	meta := func(taskType, dir string) map[string]json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"toolResponse": map[string]any{"taskType": taskType, "transcriptDir": dir}})
		return map[string]json.RawMessage{"claudeCode": raw}
	}
	// An absolute path as the platform spells it: Windows wants a drive, and cleans to backslashes.
	ok := filepath.FromSlash("/home/a/.claude/projects/p/s/subagents/workflows/wf_1")
	if runtime.GOOS == "windows" {
		ok = `C:` + ok
	}
	for _, tc := range []struct {
		meta map[string]json.RawMessage
		want string
	}{
		{meta("local_workflow", ok), ok},
		{meta("local_workflow", ok+"/"), ok},
		{meta("local_agent", ok), ""},
		{meta("local_workflow", "/etc"), ""},
		{meta("local_workflow", "relative/subagents/workflows/wf"), ""},
		{meta("local_workflow", "/x/subagents/workflows/../../../etc"), ""},
		{nil, ""},
	} {
		if got := Dir(tc.meta); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.meta["claudeCode"], got, tc.want)
		}
	}
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgents(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "journal.jsonl", `{"type":"launched"}
{"type":"started","agentId":"a1","label":"accuracy","phase":"Review"}
{"type":"started","agentId":"a2","label":"missing","phase":"Review"}
{"type":"started","agentId":"../x","label":"sneaky","phase":"Review"}
not json
{"type":"result","agentId":"a1","result":{"summary":"two\n  names differ"}}
`)
	write(t, dir, "agent-a2.jsonl", `{"type":"assistant","timestamp":"2026-11-26T14:37:05.5Z","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"README.md"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"hm"},{"type":"tool_use","name":"Bash","input":{"command":"go   test ./..."}}]}}
{"type":"user","message":{"content":"tool output"}}
`)
	agents := Agents(dir)
	if len(agents) != 3 {
		t.Fatalf("agents = %v", agents)
	}
	if a := agents[0]; a.Label != "accuracy" || a.Phase != "Review" || !a.Done || a.Result != "two names differ" || a.Activity != "" {
		t.Errorf("done agent = %v", a)
	}
	if a := agents[1]; a.Done || a.Activity != "Bash: go test ./..." {
		t.Errorf("running agent = %v", a)
	}
	if a := agents[2]; a.Activity != "" {
		t.Errorf("an agent id naming another folder was read: %v", a)
	}
	if got := agents[1].GetStartedAt().AsTime(); !got.Equal(time.Date(2026, 11, 26, 14, 37, 5, 5e8, time.UTC)) || agents[1].EndedAt != nil {
		t.Errorf("running agent's times = %v, %v", got, agents[1].EndedAt)
	}

	// Once nothing watches the workflow, the unfinished agent is stopped and the finished one left as it was.
	stopped := Stop(dir, agents)
	if a := stopped[1]; !a.Stopped || a.Activity != "" || a.EndedAt == nil {
		t.Errorf("stopped agent = %v", a)
	}
	if stopped[0] != agents[0] || agents[1].Stopped {
		t.Error("Stop changed a finished agent or the agents it was given")
	}
	if Agents(filepath.Join(dir, "missing")) != nil {
		t.Error("a folder without a journal has agents")
	}
}

func TestTranscript(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "agent-a1.jsonl", `{"type":"user","message":{"role":"user","content":"[Workflow harness — user request] The harness relays the user request:\n  review it"}}
{"type":"user","message":{"role":"user","content":"[Workflow harness — computed task] Notice. The computed task text follows:\n  Read README.md.\n  Do not edit."}}
{"type":"attachment","message":{"content":"ignored"}}
{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hidden"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"cat README.md","description":"Read the README"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"# lepson"}]}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/nope"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"no such file","is_error":true}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Looks fine."},{"type":"tool_use","id":"t3","name":"StructuredOutput","input":{"summary":"README is accurate"}}]}}
`)
	steps, err := Transcript(dir, "a1")
	if err != nil {
		t.Fatal(err)
	}
	type step struct{ kind, text, tool, input, output string }
	want := []step{
		{"prompt", "Read README.md.\nDo not edit.", "", "", ""},
		{"tool", "", "Bash", "Read the README", "# lepson"},
		{"tool", "", "Read", "/nope", "no such file"},
		{"text", "Looks fine.", "", "", ""},
		{"text", "README is accurate", "", "", ""},
	}
	if len(steps) != len(want) {
		t.Fatalf("steps = %v", steps)
	}
	for i, s := range steps {
		if got := (step{s.Kind, s.Text, s.Tool, s.Input, s.Output}); got != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, got, want[i])
		}
	}
	if !steps[2].Failed || steps[1].Failed {
		t.Error("only the failed tool call is marked failed")
	}
	if _, err := Transcript(dir, "../a1"); err == nil {
		t.Error("an agent id naming another folder was read")
	}
}
