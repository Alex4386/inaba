package store

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Alex4386/inaba/internal/testdir"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(testdir.New(t), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Who hears of a host is who can see it: owner and members, everyone for an open shared host,
// members only for a restricted one.
func TestUsersOf(t *testing.T) {
	s := openTest(t)
	ann, _ := s.AddLocalUser("ann", "pw", false, nil)
	bob, _ := s.AddLocalUser("bob", "pw", false, nil)
	cat, _ := s.AddLocalUser("cat", "pw", false, nil)
	personal, _, _ := s.AddHost("laptop", ann.ID)
	_ = s.AddMember(personal.ID, bob.ID)
	shared, _, _ := s.AddHost("box", "")

	got := func(id string) []string {
		ids, err := s.UsersOf(id)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(ids)
		return ids
	}
	want := func(ids ...string) []string { slices.Sort(ids); return ids }
	if g := got(personal.ID); !slices.Equal(g, want(ann.ID, bob.ID)) {
		t.Fatalf("personal: %v", g)
	}
	if g := got(shared.ID); !slices.Equal(g, want(ann.ID, bob.ID, cat.ID)) {
		t.Fatalf("open shared: %v", g)
	}
	_ = s.UpdateHost(shared.ID, map[string]any{"restricted": true})
	_ = s.AddMember(shared.ID, cat.ID)
	if g := got(shared.ID); !slices.Equal(g, want(cat.ID)) {
		t.Fatalf("restricted shared: %v", g)
	}
}

// A browser that signs in to another account takes its subscription along, keeping its id.
func TestRegisterDeviceMovesOwner(t *testing.T) {
	s := openTest(t)
	d := &PushDevice{UserID: "ann", Platform: "web", Endpoint: "https://fcm.googleapis.com/x", P256dh: "k", Auth: "a", Label: "Chrome"}
	if err := s.RegisterDevice(d); err != nil {
		t.Fatal(err)
	}
	again := &PushDevice{UserID: "bob", Platform: "web", Endpoint: d.Endpoint, P256dh: "k2", Auth: "a2", Label: "Chrome"}
	if err := s.RegisterDevice(again); err != nil {
		t.Fatal(err)
	}
	if again.ID != d.ID {
		t.Fatalf("id changed: %s -> %s", d.ID, again.ID)
	}
	if ds, _ := s.Devices("ann"); len(ds) != 0 {
		t.Fatalf("ann still has it: %+v", ds)
	}
	if ds, _ := s.Devices("bob"); len(ds) != 1 || ds[0].P256dh != "k2" {
		t.Fatalf("bob: %+v", ds)
	}
	for i := range MaxDevices - 1 {
		if err := s.RegisterDevice(&PushDevice{UserID: "bob", Platform: "web", Endpoint: "https://fcm.googleapis.com/" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RegisterDevice(&PushDevice{UserID: "bob", Platform: "web", Endpoint: "https://fcm.googleapis.com/over"}); !errors.Is(err, ErrTooMany) {
		t.Fatalf("over the cap: %v", err)
	}
	// Updating one already there is not a new device.
	if err := s.RegisterDevice(&PushDevice{UserID: "bob", Platform: "web", Endpoint: d.Endpoint}); err != nil {
		t.Fatalf("re-register at the cap: %v", err)
	}
}

// A snooze that woke is not listed, and housekeeping drops it; removing a member or host drops theirs.
func TestSnoozeExpiryAndCascades(t *testing.T) {
	// Wake times come in as UTC from the API while queries use the local zone; SQLite compares
	// them as text, so a zone ahead of UTC shows whether SetSnooze converts them.
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.FixedZone("UTC+9", 9*3600)
	s := openTest(t)
	ann, _ := s.AddLocalUser("ann", "pw", false, nil)
	bob, _ := s.AddLocalUser("bob", "pw", false, nil)
	h, _, _ := s.AddHost("laptop", ann.ID)
	_ = s.AddMember(h.ID, bob.ID)
	past, future := time.Now().Add(-time.Minute).UTC(), time.Now().Add(time.Hour).UTC()
	_ = s.SetSnooze(&Snooze{UserID: ann.ID, HostID: h.ID, Kind: "thread", TargetID: "old", Until: &past})
	_ = s.SetSnooze(&Snooze{UserID: ann.ID, HostID: h.ID, Kind: "thread", TargetID: "new", Until: &future})
	_ = s.SetSnooze(&Snooze{UserID: ann.ID, HostID: h.ID, Kind: "project", TargetID: "p"})
	if sn, _ := s.Snoozes(ann.ID, h.ID); len(sn) != 2 {
		t.Fatalf("listed: %+v", sn)
	}
	if _, err := s.Upkeep(); err != nil {
		t.Fatal(err)
	}
	var n int64
	s.db.Model(&Snooze{}).Count(&n)
	if n != 2 {
		t.Fatalf("rows after upkeep: %d", n)
	}

	_ = s.SetSnooze(&Snooze{UserID: bob.ID, HostID: h.ID, Kind: "thread", TargetID: "t"})
	if err := s.RemoveMember(h.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	if sn, _ := s.Snoozes(bob.ID, h.ID); len(sn) != 0 {
		t.Fatalf("removed member's snoozes: %+v", sn)
	}
	_ = s.RegisterDevice(&PushDevice{UserID: ann.ID, Platform: "web", Endpoint: "https://fcm.googleapis.com/x"})
	if err := s.DeleteHost(h.ID); err != nil {
		t.Fatal(err)
	}
	if sn, _ := s.Snoozes(ann.ID, h.ID); len(sn) != 0 {
		t.Fatalf("deleted host's snoozes: %+v", sn)
	}
	_ = s.SetSnooze(&Snooze{UserID: ann.ID, HostID: "other", Kind: "thread", TargetID: "t"})
	if err := s.DeleteUser(ann.ID); err != nil {
		t.Fatal(err)
	}
	if ds, _ := s.Devices(ann.ID); len(ds) != 0 {
		t.Fatalf("deleted user's devices: %+v", ds)
	}
	if sn, _ := s.Snoozes(ann.ID, "other"); len(sn) != 0 {
		t.Fatalf("deleted user's snoozes: %+v", sn)
	}
}

func TestVAPIDKeysAreMadeOnce(t *testing.T) {
	s := openTest(t)
	pub, priv, err := s.VAPIDKeys()
	if err != nil || pub == "" || priv == "" {
		t.Fatal(pub, priv, err)
	}
	if pub2, _, _ := s.VAPIDKeys(); pub2 != pub {
		t.Fatal("keys changed")
	}
}
