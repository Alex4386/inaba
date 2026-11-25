package store

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Alex4386/inaba/internal/testdir"
)

// Two handles on one file stand in for the server and a CLI command run beside it.
func TestChangesFromAnotherProcessAreSeen(t *testing.T) {
	path := filepath.Join(testdir.New(t), "s.db")
	server, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	u, err := cli.AddLocalUser("ann", "pw1", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.AddLocalUser("ann", "x", false, nil); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate name: %v", err)
	}
	if got, err := server.LocalUser("ann"); err != nil || got.ID != u.ID {
		t.Fatalf("server does not see the CLI's user: %v %v", got, err)
	}

	_, session, _ := server.NewToken(KindSession, u.ID, "", time.Hour)
	_, api, _ := server.NewToken(KindAPI, u.ID, "ci", 0)
	_, expired, _ := server.NewToken(KindSession, u.ID, "", time.Nanosecond)
	time.Sleep(time.Millisecond)
	if _, err := server.LookupToken(expired); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token: %v", err)
	}
	if _, err := server.LookupToken("inaba_api_wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}

	// A password change signs the account out but keeps its API tokens.
	if err := cli.SetPassword("ann", "pw2"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.LookupToken(session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session after password change: %v", err)
	}
	if _, err := server.LookupToken(api); err != nil {
		t.Fatalf("API token after password change: %v", err)
	}

	h, hostSecret, err := cli.AddHost("laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	// One account must not revoke another's token, and nobody revokes a host secret this way.
	hostToken, _ := server.LookupToken(hostSecret)
	apiToken, _ := server.LookupToken(api)
	if err := server.DeleteToken(apiToken.ID, "someone-else"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking another account's token: %v", err)
	}
	if err := server.DeleteToken(hostToken.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking a host secret as a token: %v", err)
	}

	if err := cli.DeleteHost(h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.LookupToken(hostSecret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("host secret after the host is removed: %v", err)
	}
	if err := cli.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.LookupToken(api); !errors.Is(err, ErrNotFound) {
		t.Fatalf("API token after its account is removed: %v", err)
	}
}

// A host registered before hosts had owners is shared.
func TestHostFromBeforeOwnersIsShared(t *testing.T) {
	s, err := Open(filepath.Join(testdir.New(t), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ := s.AddHost("old", "")
	if err := s.db.Exec("UPDATE hosts SET owner = NULL WHERE id = ?", h.ID).Error; err != nil {
		t.Fatal(err)
	}
	if hosts, _ := s.HostsFor("someone"); len(hosts) != 1 || hosts[0].Owner != "" {
		t.Fatalf("hosts: %+v", hosts)
	}
}

// A host from before hosts could be restricted has the column NULL: it is open to everyone, as it was.
func TestHostFromBeforeRestrictionIsOpen(t *testing.T) {
	s, err := Open(filepath.Join(testdir.New(t), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ := s.AddHost("old", "")
	if err := s.db.Exec("UPDATE hosts SET restricted = NULL WHERE id = ?", h.ID).Error; err != nil {
		t.Fatal(err)
	}
	if hosts, _ := s.HostsFor("someone"); len(hosts) != 1 {
		t.Fatalf("hosts: %+v", hosts)
	}
	// Restricted, it is its members' alone.
	if err := s.UpdateHost(h.ID, map[string]any{"restricted": true}); err != nil {
		t.Fatal(err)
	}
	if hosts, _ := s.HostsFor("someone"); len(hosts) != 0 {
		t.Fatalf("a restricted host for someone not in it: %+v", hosts)
	}
	_ = s.AddMember(h.ID, "someone")
	if hosts, _ := s.HostsFor("someone"); len(hosts) != 1 {
		t.Fatalf("a restricted host for its member: %+v", hosts)
	}
}

// An account saved before the permissions were split keeps all it could do; one saved
// after gets exactly what it was given, even when that looks like an old list.
func TestPermissionsFromBeforeTheSplitExpand(t *testing.T) {
	old := &User{Permissions: "hosts,projects"}
	if got := old.Granted(); !slices.Equal(got, []string{PermSharedHosts, PermHostSettings, PermHousekeeping, PermProjects, PermThreadsAnywhere, PermAutomations}) {
		t.Fatalf("old: %v", got)
	}
	s, err := Open(filepath.Join(testdir.New(t), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.AddLocalUser("ann", "pw", false, []string{PermProjects})
	if err != nil {
		t.Fatal(err)
	}
	if u, _ = s.User(u.ID); !slices.Equal(u.Granted(), []string{PermProjects}) {
		t.Fatalf("new: %v", u.Granted())
	}
	if u, _ = s.SetAccess(u.ID, false, nil); len(u.Granted()) != 0 || u.Can(PermThreadsAnywhere) {
		t.Fatalf("cleared: %v", u.Granted())
	}
}
