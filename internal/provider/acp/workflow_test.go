package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/coder/acp-go-sdk"
)

func TestWorkflowDirOnlyFromAWorkflowFolder(t *testing.T) {
	meta := func(taskType, dir string) sdk.Meta {
		raw, _ := json.Marshal(map[string]any{"toolResponse": map[string]any{"taskType": taskType, "transcriptDir": dir}})
		return sdk.Meta{"claudeCode": raw}
	}
	ok := "/home/a/.claude/projects/p/s/subagents/workflows/wf_1"
	for _, tc := range []struct {
		meta sdk.Meta
		want string
	}{
		{meta("local_workflow", ok), ok},
		{meta("local_workflow", ok+"/"), ok},
		{meta("local_agent", ok), ""},
		{meta("local_workflow", "/etc"), ""},
		{meta("local_workflow", "relative/subagents/workflows/wf"), ""},
		{meta("local_workflow", "/x/subagents/workflows/../../../etc"), ""},
		{sdk.Meta{}, ""},
	} {
		if got := workflowDir(tc.meta); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.meta["claudeCode"], got, tc.want)
		}
	}
}

func TestReadWorkflow(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("journal.jsonl", `{"type":"launched"}
{"type":"started","agentId":"a1","label":"accuracy","phase":"Review"}
{"type":"started","agentId":"a2","label":"missing","phase":"Review"}
{"type":"started","agentId":"../x","label":"sneaky","phase":"Review"}
not json
{"type":"result","agentId":"a1","result":{"summary":"two\n  names differ"}}
`)
	write("agent-a2.jsonl", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"README.md"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"hm"},{"type":"tool_use","name":"Bash","input":{"command":"go   test ./..."}}]}}
{"type":"user","message":{"content":"tool output"}}
`)
	agents := readWorkflow(dir)
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
	if readWorkflow(filepath.Join(dir, "missing")) != nil {
		t.Error("a folder without a journal has agents")
	}
}
