package host

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/structpb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// A rewound conversation goes on in a new agent session that is handed what is left of it.
func TestRewindStartsAFreshSessionWithTheKeptConversation(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	turn := func(text string) {
		t.Helper()
		if _, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: text}); err != nil {
			t.Fatal(err)
		}
		for range 300 {
			if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); got.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("turn did not end")
	}
	seqOf := func(text string) uint64 {
		evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
		for _, e := range evs.Events {
			if m := e.GetUserMessage(); m != nil && m.Text == text {
				return e.Seq
			}
		}
		t.Fatalf("no message %q", text)
		return 0
	}
	turn("first")
	turn("second")

	res, err := h.Rewind(ctx, &inabav1.RewindRequest{ThreadId: th.Id, Seq: seqOf("second")})
	if err != nil || res.Message.Text != "second" || !res.Thread.Rewound || res.Thread.NativeSessionId != "" {
		t.Fatalf("rewind: %v %v", res, err)
	}
	if _, err := h.Rewind(ctx, &inabav1.RewindRequest{ThreadId: th.Id, Seq: seqOf("second")}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("rewinding to a message already taken back: %v", err)
	}
	if _, err := h.Rewind(ctx, &inabav1.RewindRequest{ThreadId: th.Id, Seq: seqOf("first"), RestoreFiles: true}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("restoring files of a thread without a worktree: %v", err)
	}

	turn("third")
	evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
	reply := evs.Events[len(evs.Events)-2].GetTextDelta().GetText()
	// The fake agent echoes what it is sent, and says which thread it resumed: none.
	if !strings.Contains(reply, "<user>\nfirst\n</user>") || strings.Contains(reply, "second") || !strings.HasSuffix(reply, "third resumed=") {
		t.Fatalf("the agent was sent %q", reply)
	}
	var kept []string
	for _, e := range current(evs.Events) {
		if m := e.GetUserMessage(); m != nil {
			kept = append(kept, m.Text)
		}
	}
	if !slices.Equal(kept, []string{"first", "third"}) {
		t.Fatalf("conversation now holds %v", kept)
	}
	if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); got.Rewound {
		t.Fatal("the next turn after that would get the history again")
	}
}

func TestAttachmentsAreChecked(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), nil, 0o644)
	for _, c := range []struct {
		a  *inabav1.Attachment
		ok bool
	}{
		{&inabav1.Attachment{Path: "a.go"}, true},
		{&inabav1.Attachment{Name: "p.png", MimeType: "image/png", Data: []byte{1}}, true},
		{&inabav1.Attachment{Path: "../etc/passwd"}, false},
		{&inabav1.Attachment{Path: "/etc/passwd"}, false},
		{&inabav1.Attachment{Path: "missing.go"}, false},
		{&inabav1.Attachment{Name: "x.zip", MimeType: "application/zip", Data: []byte{1}}, true},
		{&inabav1.Attachment{Name: "big.png", MimeType: "image/png", Data: make([]byte, maxImageBytes+1)}, false},
		{&inabav1.Attachment{}, false},
	} {
		if err := checkAttachments(dir, nil, []*inabav1.Attachment{c.a}); (err == nil) != c.ok {
			t.Errorf("%v: %v", c.a.Path+c.a.Name, err)
		}
	}
	zip := &inabav1.Attachment{Name: "x.zip", MimeType: "application/zip", Data: []byte{1}}
	big := &inabav1.Attachment{Name: "big.pdf", MimeType: "application/pdf", Data: make([]byte, maxFileBytes+1)}
	if err := checkAttachments(dir, nil, []*inabav1.Attachment{big}); err == nil {
		t.Error("a file over the per-file cap was taken")
	}
	half := &inabav1.Attachment{Name: "half.pdf", MimeType: "application/pdf", Data: make([]byte, maxFileBytes)}
	if err := checkAttachments(dir, nil, []*inabav1.Attachment{half, half, zip}); err == nil {
		t.Error("files over the total cap were taken")
	}
}

func TestFileNameIsSafe(t *testing.T) {
	for in, want := range map[string]string{
		"notes.txt":         "notes.txt",
		"../../etc/passwd":  "passwd",
		`..\..\win.ini`:     "win.ini",
		"..":                "attachment",
		"":                  "attachment",
		".env":              "env",
		"a\x00b\nc.txt":     "abc.txt",
		"/":                 "attachment",
		"보고서.pdf":           "보고서.pdf",
		"evil\u202etxt.exe": "eviltxt.exe",
	} {
		if got := fileName(in); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A file that is not an image is written into the thread's folder, hidden from git,
// and the agent is given its path; from then on the message and the queue keep only
// that path and the size, not the bytes. Deleting the thread removes the files.
func TestFileAttachmentsReachTheAgentAsPaths(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	cwd := t.TempDir()
	if _, err := git(ctx, cwd, "init", "-q"); err != nil {
		t.Skip("git is not available:", err)
	}
	th, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "hang"}); err != nil {
		t.Fatal(err)
	}
	// Sent while the first turn runs, so it waits in the queue.
	att := &inabav1.Attachment{Name: "../../notes.txt", MimeType: "text/plain", Data: []byte("hello file")}
	queued, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "read it", Attachments: []*inabav1.Attachment{att}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id})
	if len(got.Queue) != 1 || got.Queue[0].Id != queued.QueuedId {
		t.Fatalf("queue: %v", got.Queue)
	}
	if q := got.Queue[0].Attachments[0]; len(q.Data) != 0 || q.GetSize() != 10 || !strings.HasPrefix(q.Path, uploadsDir(th.Id)) {
		t.Fatalf("the queued attachment is %v", q)
	}
	if _, err := h.SendQueuedNow(ctx, &inabav1.SendQueuedNowRequest{ThreadId: th.Id, QueuedId: queued.QueuedId}); err != nil {
		t.Fatal(err)
	}
	var said string
	var msg *inabav1.Attachment
	for range 300 {
		evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
		for _, e := range evs.Events {
			if d := e.GetTextDelta(); d != nil && strings.HasPrefix(d.Text, "file:") && said == "" {
				said = d.Text
			}
			if m := e.GetUserMessage(); m != nil && len(m.Attachments) == 1 {
				msg = m.Attachments[0]
			}
		}
		if said != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	dir := filepath.Join(cwd, uploadsDir(th.Id))
	path, content, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(said, "file:"), "\n"), "=")
	if content != "hello file" || filepath.Base(path) != "notes.txt" || !strings.HasPrefix(path, dir+string(filepath.Separator)) {
		t.Fatalf("agent was told %q", said)
	}
	if msg.Name != "../../notes.txt" || len(msg.Data) != 0 || msg.GetSize() != 10 {
		t.Fatalf("the message's attachment is %v", msg)
	}
	if st, _ := git(ctx, cwd, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Fatalf("uploads show in git status: %q", st)
	}
	hideUploads(ctx, cwd) // a second time adds nothing
	exclude, _ := os.ReadFile(filepath.Join(cwd, ".git", "info", "exclude"))
	if strings.Count(string(exclude), uploadsIgnore) != 1 {
		t.Fatalf("info/exclude reads %q", exclude)
	}
	if _, err := h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: th.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("uploads folder after delete: %v", err)
	}
}

type otherProvider struct{ fakeProvider }

func (otherProvider) ID() string { return "other" }

func TestSwitchingProviderHandsOverTheConversation(t *testing.T) {
	h := New(fakeProvider{}, otherProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	turn := func(text string) string {
		t.Helper()
		if _, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: text}); err != nil {
			t.Fatal(err)
		}
		for range 300 {
			if got, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: th.Id}); got.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE {
				evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
				return evs.Events[len(evs.Events)-2].GetTextDelta().GetText()
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("turn did not end")
		return ""
	}
	turn("first")
	if _, err := h.UpdateThread(ctx, &inabav1.UpdateThreadRequest{ThreadId: th.Id, Provider: proto.String("nope")}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("switching to an unknown provider: %v", err)
	}
	got, err := h.UpdateThread(ctx, &inabav1.UpdateThreadRequest{ThreadId: th.Id, Provider: proto.String("other")})
	if err != nil || got.Provider != "other" || got.HandoffFrom != "fake" {
		t.Fatalf("switch: %v %v", got, err)
	}
	// The new agent starts without the old one's session, and is sent the conversation first.
	reply := turn("second")
	if !strings.Contains(reply, "another agent (fake)") || !strings.Contains(reply, "<user>\nfirst\n</user>") || !strings.HasSuffix(reply, "User message:\nsecond resumed=") {
		t.Fatalf("the new agent was sent %q", reply)
	}
	if reply := turn("third"); reply != "echo third resumed=" {
		t.Fatalf("the turn after the handoff was sent %q", reply)
	}
}

func TestHandoffTextKeepsTheNewestTurns(t *testing.T) {
	long := strings.Repeat("<user>\nold\n</user>\n", maxHandoff/10) + "<user>\nnewest\n</user>\n"
	got := handoffText(long)
	if len(got) > maxHandoff+64 || !strings.HasSuffix(got, "<user>\nnewest\n</user>\n") || !strings.Contains(got, "left out for length") {
		t.Fatalf("trimmed to %d bytes: %q…", len(got), got[:80])
	}
	if handoffText("short") != "short" {
		t.Fatal("a short transcript is kept whole")
	}
}

// Opening a thread does not carry its pasted images or the providers' raw records; the
// log keeps them, and an image comes on its own when asked for.
func TestSubscribeLeavesHeavyPartsToTheLog(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	th, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nnot really")
	if _, err := h.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: th.Id, Text: "look", Attachments: []*inabav1.Attachment{{Name: "shot.png", MimeType: "image/png", Data: png}}}); err != nil {
		t.Fatal(err)
	}
	var seq uint64
	evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
	for _, e := range evs.Events {
		if m := e.GetUserMessage(); m != nil {
			seq = e.Seq
			sent := light(e).GetUserMessage().Attachments[0]
			if len(sent.Data) != 0 || sent.GetSize() != uint64(len(png)) || sent.Name != "shot.png" {
				t.Fatalf("the image as sent: %v", sent)
			}
			if len(m.Attachments[0].Data) != len(png) {
				t.Fatal("lightening an event changed the log")
			}
		}
	}
	if seq == 0 {
		t.Fatal("no user message in the log")
	}
	got, err := h.GetAttachment(ctx, &inabav1.GetAttachmentRequest{ThreadId: th.Id, Seq: seq})
	if err != nil || string(got.Data) != string(png) {
		t.Fatalf("GetAttachment = %v, %v", got, err)
	}
	for _, bad := range []*inabav1.GetAttachmentRequest{{ThreadId: th.Id, Seq: seq, Index: 1}, {ThreadId: th.Id, Seq: 0}, {ThreadId: th.Id, Seq: 99999}} {
		if _, err := h.GetAttachment(ctx, bad); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("GetAttachment(%v) = %v", bad, err)
		}
	}

	raw, _ := structpb.NewValue(map[string]any{"big": "record"})
	call := &inabav1.Event{Payload: &inabav1.Event_ToolCall{ToolCall: &inabav1.ToolCall{Id: "c", Title: "t", Raw: raw}}}
	if c := light(call).GetToolCall(); c.Raw != nil || c.Title != "t" || call.GetToolCall().Raw == nil {
		t.Fatalf("tool call as sent %v, kept %v", c, call)
	}
	plain := &inabav1.Event{Payload: &inabav1.Event_TextDelta{TextDelta: &inabav1.TextDelta{Text: "x"}}}
	if light(plain) != plain {
		t.Fatal("an event with nothing to leave out was copied")
	}
}

// A long thread opens at its last turns, and reading back goes a turn at a time, starting
// mid-turn only in a turn too long to read whole.
func TestTurnStartBeginsAtAUserMessage(t *testing.T) {
	th := &thread{}
	// Turns begin at seqs 1, 4 and 9.
	for i := uint64(1); i <= 10; i++ {
		e := &inabav1.Event{Seq: i, Payload: &inabav1.Event_TextDelta{TextDelta: &inabav1.TextDelta{Text: "x"}}}
		if i == 1 || i == 4 || i == 9 {
			e.Payload = &inabav1.Event_UserMessage{UserMessage: &inabav1.UserMessage{Text: "hi"}}
		}
		th.events = append(th.events, e)
	}
	for _, c := range []struct {
		before uint64
		n      int
		want   uint64
	}{
		{0, 2, 8},  // the last turn, from seq 9
		{0, 4, 3},  // seq 7 is in the turn from seq 4, within 4 more
		{0, 3, 7},  // seq 8 is more than 3 into its turn: start there
		{0, 1, 8},  // seq 10 is within 1 more of its turn's start
		{0, 50, 0}, // all of it
		{8, 3, 3},  // before seq 8: the turn from seq 4
		{9, 1, 7},  // before the last turn, not within reach of its start
		{1, 5, 0},  // nothing before the first event
		{99, 2, 8}, // past the end reads as the end
	} {
		if got := th.turnStart(c.before, c.n); got != c.want {
			t.Errorf("turnStart(%d, %d) = %d, want %d", c.before, c.n, got, c.want)
		}
	}
}
