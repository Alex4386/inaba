package acp

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/coder/acp-go-sdk"
)

func TestToolText(t *testing.T) {
	for raw, want := range map[string]string{
		`{"command":"ls -la"}`:         "ls -la",
		`{"command":["git","status"]}`: "git status",
		`{"path":"a.go"}`:              "{\n  \"path\": \"a.go\"\n}",
		`null`:                         "",
		`{}`:                           "",
	} {
		if got := toolInput(json.RawMessage(raw)); got != want {
			t.Errorf("input %s: %q", raw, got)
		}
	}
	text := sdk.ToolCallContent{Content: &sdk.ToolCallContentContent{Content: sdk.TextBlock("a.txt\nb.txt")}}
	if got := toolOutput([]sdk.ToolCallContent{text}, json.RawMessage(`"ignored"`)); got != "a.txt\nb.txt" {
		t.Errorf("content: %q", got)
	}
	if got := toolOutput(nil, json.RawMessage(`"`+"```console\\nNOTES.md\\nmain.go\\n```"+`"`)); got != "NOTES.md\nmain.go" {
		t.Errorf("fenced: %q", got)
	}
	if got := toolOutput(nil, json.RawMessage(`"plain"`)); got != "plain" {
		t.Errorf("raw string: %q", got)
	}
	// A long output is cut on a character boundary.
	long := clip(strings.Repeat("가", maxToolOutput))
	if len(long) > maxToolOutput+5 || !utf8.ValidString(long) || !strings.HasSuffix(long, "…") {
		t.Errorf("clipped to %d bytes, valid %v", len(long), utf8.ValidString(long))
	}
}
