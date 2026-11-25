package store

import "testing"

// A read mark only moves forward, is per user, and goes with the member, host or user.
func TestThreadSeen(t *testing.T) {
	s := openTest(t)
	ann, _ := s.AddLocalUser("ann", "pw", false, nil)
	bob, _ := s.AddLocalUser("bob", "pw", false, nil)
	h, _, _ := s.AddHost("laptop", ann.ID)
	_ = s.AddMember(h.ID, bob.ID)

	if seq, err := s.MarkSeen(ann.ID, h.ID, "t1", 5); err != nil || seq != 5 {
		t.Fatal(seq, err)
	}
	if seq, _ := s.MarkSeen(ann.ID, h.ID, "t1", 3); seq != 5 {
		t.Fatalf("a lower seq moved the mark back to %d", seq)
	}
	if seq, _ := s.MarkSeen(ann.ID, h.ID, "t1", 9); seq != 9 {
		t.Fatalf("mark = %d", seq)
	}
	_, _ = s.MarkSeen(bob.ID, h.ID, "t1", 1)
	if seen, _ := s.Seen(ann.ID, h.ID); len(seen) != 1 || seen[0].ThreadID != "t1" || seen[0].Seq != 9 {
		t.Fatalf("ann's marks: %+v", seen)
	}

	if err := s.RemoveMember(h.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	if seen, _ := s.Seen(bob.ID, h.ID); len(seen) != 0 {
		t.Fatalf("removed member's marks: %+v", seen)
	}
	if err := s.DeleteHost(h.ID); err != nil {
		t.Fatal(err)
	}
	if seen, _ := s.Seen(ann.ID, h.ID); len(seen) != 0 {
		t.Fatalf("deleted host's marks: %+v", seen)
	}
	_, _ = s.MarkSeen(ann.ID, "other", "t", 1)
	if err := s.DeleteUser(ann.ID); err != nil {
		t.Fatal(err)
	}
	if seen, _ := s.Seen(ann.ID, "other"); len(seen) != 0 {
		t.Fatalf("deleted user's marks: %+v", seen)
	}
}
