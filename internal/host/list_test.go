package host

import (
	"context"
	"testing"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// The list leaves out what only a thread's own screen shows; GetThread still has all of it.
func TestListThreadsIsLight(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	ctx := Trusted(context.Background())
	created, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir(), Title: "named"})
	if err != nil {
		t.Fatal(err)
	}
	th, _ := h.thread(created.Id)
	th.mu.Lock()
	th.meta.Commands = []*inabav1.Command{{Name: "review"}}
	th.meta.Options = []*inabav1.SessionOption{{Id: "model"}}
	th.meta.Layout = `{"docks":[]}`
	th.meta.Usage = &inabav1.Usage{Used: 1, Size: 2}
	th.meta.Queue = []*inabav1.QueuedMessage{{Id: "q", Text: "next", Attachments: []*inabav1.Attachment{{Name: "shot.png", Data: []byte("png")}}}}
	th.meta.Labels = map[string]string{"pinned": "true"}
	th.mu.Unlock()

	list, err := h.ListThreads(ctx, &inabav1.ListThreadsRequest{})
	if err != nil || len(list.Threads) != 1 {
		t.Fatalf("ListThreads = %v, %v", list, err)
	}
	got := list.Threads[0]
	if got.Commands != nil || got.Options != nil || got.Usage != nil || len(got.Queue[0].Attachments[0].Data) != 0 {
		t.Fatalf("heavy parts in the list: %v", got)
	}
	if got.Title != "named" || got.Layout != `{"docks":[]}` || got.Labels["pinned"] != "true" || got.Queue[0].Text != "next" || got.Queue[0].Attachments[0].Name != "shot.png" {
		t.Fatalf("the list lost what the sidebar reads: %v", got)
	}
	full, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: created.Id})
	if len(full.Commands) != 1 || len(full.Options) != 1 || full.Layout == "" || full.Usage == nil || len(full.Queue[0].Attachments[0].Data) != 3 {
		t.Fatalf("listing changed the thread: %v", full)
	}
}
