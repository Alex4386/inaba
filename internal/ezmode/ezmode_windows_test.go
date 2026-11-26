package ezmode

import (
	"encoding/xml"
	"strings"
	"testing"
)

// The task runs inaba headless, logging to a file, as the user at their sign-in.
func TestEzTask(t *testing.T) {
	text := taskText(Spec{Name: "inaba"}, `C:\Program Files\a&b\inaba.exe`, []string{"connect"}, `PC\a`, `C:\Users\a\AppData\Local\inaba`, `C:\Users\a\AppData\Local\inaba\inaba.log`)
	var task struct {
		Trigger string `xml:"Triggers>LogonTrigger>UserId"`
		Command string `xml:"Actions>Exec>Command"`
		Args    string `xml:"Actions>Exec>Arguments"`
		Limit   string `xml:"Settings>ExecutionTimeLimit"`
	}
	// The declaration says UTF-16, which the file will be; the text here is not yet.
	if err := xml.Unmarshal([]byte(strings.Replace(text, `encoding="UTF-16"`, "", 1)), &task); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if task.Trigger != `PC\a` || !strings.HasSuffix(strings.ToLower(task.Command), `\system32\conhost.exe`) || task.Limit != "PT0S" {
		t.Errorf("%+v", task)
	}
	if want := `--headless "C:\Program Files\a&b\inaba.exe" connect --log-file C:\Users\a\AppData\Local\inaba\inaba.log`; task.Args != want {
		t.Errorf("arguments %s, want %s", task.Args, want)
	}
}
