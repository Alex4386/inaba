package host

import (
	"context"
	"testing"
	"time"

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
