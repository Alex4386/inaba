package host

import (
	"context"
	"path/filepath"
	"testing"
	"unicode/utf16"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/testdir"
)

func TestSearchFindsMessagesAsTheThreadShowsThem(t *testing.T) {
	ctx := Trusted(context.Background())
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	if err := h.OpenData(filepath.Join(testdir.New(t), "inaba.db")); err != nil {
		t.Fatal(err)
	}
	newThread := func() *thread {
		th, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		tt, _ := h.thread(th.Id)
		return tt
	}
	user := func(tt *thread, text string) uint64 {
		tt.emit(&inabav1.Event{Payload: &inabav1.Event_UserMessage{UserMessage: &inabav1.UserMessage{Text: text}}})
		return tt.snapshot().LastSeq
	}
	delta := func(tt *thread, kind inabav1.TextDelta_Kind, text string) uint64 {
		tt.emit(&inabav1.Event{Payload: &inabav1.Event_TextDelta{TextDelta: &inabav1.TextDelta{Kind: kind, Text: text}}})
		return tt.snapshot().LastSeq
	}
	const msg = inabav1.TextDelta_KIND_MESSAGE
	search := func(q string) []*inabav1.SearchHit {
		t.Helper()
		res, err := h.SearchThreads(ctx, &inabav1.SearchThreadsRequest{Query: q})
		if err != nil {
			t.Fatal(err)
		}
		return res.Hits
	}

	a := newThread()
	first := user(a, "오늘 날씨 어때?")
	answer := delta(a, msg, "오늘은 ")
	delta(a, msg, "맑")
	delta(a, msg, "습니다")
	delta(a, inabav1.TextDelta_KIND_THOUGHT, "thinking about 맑습니다")
	after := delta(a, msg, "Done.")

	if hits := search("날씨"); len(hits) != 1 || hits[0].Seq != first || hits[0].Kind != "user" {
		t.Fatalf("two characters: %v", hits)
	}
	// Split over three deltas, found as one message at its first; the thought is not indexed.
	if hits := search("맑습니다"); len(hits) != 1 || hits[0].Seq != answer || hits[0].Kind != "assistant" || hits[0].Snippet != "오늘은 맑습니다" {
		t.Fatalf("across deltas: %v", hits)
	}
	if hits := search("done"); len(hits) != 1 || hits[0].Seq != after {
		t.Fatalf("after a thought: %v", hits)
	}
	// More of the same answer, after a search indexed the start of it.
	delta(a, msg, " Really done")
	if hits := search("Done. Really"); len(hits) != 1 || hits[0].Seq != after {
		t.Fatalf("open tail: %v", hits)
	}
	if hits := search(""); len(hits) != 0 {
		t.Fatalf("empty query: %v", hits)
	}

	// Taken back by a rewind.
	second := user(a, "두번째 질문")
	delta(a, msg, "두번째 답")
	a.emit(&inabav1.Event{Payload: &inabav1.Event_Rewound{Rewound: &inabav1.Rewound{ToSeq: second}}})
	if hits := search("두번째"); len(hits) != 0 {
		t.Fatalf("rewound: %v", hits)
	}
	if hits := search("날씨"); len(hits) != 1 {
		t.Fatalf("kept before the rewind: %v", hits)
	}

	// Newest thread first, and a deleted one is gone.
	b := newThread()
	user(b, "오늘 날씨 좋다")
	if hits := search("오늘 날씨"); len(hits) != 2 || hits[0].ThreadId != b.meta.Id {
		t.Fatalf("order: %v", hits)
	}
	if _, err := h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: b.meta.Id}); err != nil {
		t.Fatal(err)
	}
	if hits := search("오늘 날씨"); len(hits) != 1 || hits[0].ThreadId != a.meta.Id {
		t.Fatalf("deleted: %v", hits)
	}
}

func TestSnippetCountsUTF16(t *testing.T) {
	body := "😀😀 한국어\n\n  text then the NEEDLE here"
	got, start, end := snippet(body, "needle")
	if got != "😀😀 한국어 text then the NEEDLE here" {
		t.Fatalf("snippet = %q", got)
	}
	u := utf16.Encode([]rune(got))
	if string(utf16.Decode(u[start:end])) != "NEEDLE" {
		t.Fatalf("match at %d-%d of %q", start, end, got)
	}
	long := "가나다라마바사아자차카타파하가나다라마바사아자차카타파하가나다라마바사아자차카타파하 needle"
	got, start, end = snippet(long, "needle")
	u = utf16.Encode([]rune(got))
	if got[:len("…")] != "…" || string(utf16.Decode(u[start:end])) != "needle" {
		t.Fatalf("cut: %q %d-%d", got, start, end)
	}
}
