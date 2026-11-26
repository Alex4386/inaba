package host

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// A restart is asked for once and comes a moment after the answer; only a caller the
// server allowed, or the machine's own administrator, may ask.
func TestRestartHost(t *testing.T) {
	h := New()
	if _, err := h.RestartHost(context.Background(), &inabav1.RestartHostRequest{}); err == nil {
		t.Fatal("restarted for a caller nobody allowed")
	}
	select {
	case <-h.Restarts():
		t.Fatal("a refused restart went ahead")
	case <-time.After(2 * restartDelay):
	}
	for range 2 {
		if _, err := h.RestartHost(Trusted(context.Background()), &inabav1.RestartHostRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-h.Restarts():
	case <-time.After(5 * time.Second):
		t.Fatal("no restart after it was asked for")
	}
}

// An update is the restart permission's too, names a channel there is, and fetches a
// nightly with the host's gh sign-in only when told it may.
func TestUpgradeHostRefuses(t *testing.T) {
	h := New()
	for name, c := range map[string]struct {
		ctx  context.Context
		req  *inabav1.UpgradeHostRequest
		code connect.Code
	}{
		"not allowed":        {context.Background(), &inabav1.UpgradeHostRequest{Channel: "release"}, connect.CodePermissionDenied},
		"no such channel":    {Trusted(context.Background()), &inabav1.UpgradeHostRequest{Channel: "beta"}, connect.CodeInvalidArgument},
		"nightly without gh": {Trusted(context.Background()), &inabav1.UpgradeHostRequest{Channel: "nightly"}, connect.CodeFailedPrecondition},
	} {
		if _, err := h.UpgradeHost(c.ctx, c.req); connect.CodeOf(err) != c.code {
			t.Errorf("%s: %v, want %v", name, err, c.code)
		}
	}
	select {
	case <-h.Restarts():
		t.Fatal("a refused update restarted the host")
	case <-time.After(2 * restartDelay):
	}
}
