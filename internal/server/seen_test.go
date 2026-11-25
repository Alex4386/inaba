package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/testdir"
)

func TestThreadSeen(t *testing.T) {
	s, err := New(context.Background(), Config{DataPath: filepath.Join(testdir.New(t), "s.db")})
	if err != nil {
		t.Fatal(err)
	}
	ann, _ := s.store.AddLocalUser("ann", "pw", false, nil)
	bob, _ := s.store.AddLocalUser("bob", "pw", false, nil)
	h, _, _ := s.store.AddHost("bobs", bob.ID)

	if m, err := s.MarkThreadSeen(as(bob), &inabav1.MarkThreadSeenRequest{HostId: h.ID, ThreadId: "t", Seq: 7}); err != nil || m.Seq != 7 {
		t.Fatalf("mark: %v %v", m, err)
	}
	if m, _ := s.MarkThreadSeen(as(bob), &inabav1.MarkThreadSeenRequest{HostId: h.ID, ThreadId: "t", Seq: 2}); m.Seq != 7 {
		t.Fatalf("a lower seq moved the mark: %v", m)
	}
	if l, err := s.ListThreadSeen(as(bob), &inabav1.ListThreadSeenRequest{HostId: h.ID}); err != nil || len(l.Threads) != 1 || l.Threads[0].ThreadId != "t" || l.Threads[0].Seq != 7 {
		t.Fatalf("list: %v %v", l, err)
	}
	// Bob's personal host is not Ann's: answered as missing, as snoozes are.
	if _, err := s.MarkThreadSeen(as(ann), &inabav1.MarkThreadSeenRequest{HostId: h.ID, ThreadId: "t", Seq: 1}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("mark on another's host: %v", err)
	}
	if _, err := s.ListThreadSeen(as(ann), &inabav1.ListThreadSeenRequest{HostId: h.ID}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("list on another's host: %v", err)
	}
	for _, id := range []string{"", strings.Repeat("t", 65)} {
		if _, err := s.MarkThreadSeen(as(bob), &inabav1.MarkThreadSeenRequest{HostId: h.ID, ThreadId: id}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("thread id %q: %v", id, err)
		}
	}
	if _, err := s.ListThreadSeen(context.Background(), &inabav1.ListThreadSeenRequest{HostId: h.ID}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("signed out: %v", err)
	}
}
