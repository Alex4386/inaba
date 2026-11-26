package automation

import (
	"context"
	"strings"
	"testing"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/module"
	"github.com/Alex4386/inaba/pkg/provider"
)

// echoAgent answers each message with "echo <message>".
type echoAgent struct{}

func (echoAgent) ID() string { return "fake" }
func (echoAgent) Open(_ context.Context, _ provider.Config, emit provider.Emit) (provider.Session, error) {
	return echoSession{emit}, nil
}

type echoSession struct{ emit provider.Emit }

func (echoSession) NativeSessionID() string { return "n" }
func (s echoSession) Prompt(_ context.Context, p provider.Prompt) (string, error) {
	s.emit(&inabav1.Event{Payload: &inabav1.Event_TextDelta{TextDelta: &inabav1.TextDelta{Text: "echo " + p.Text}}})
	return "end_turn", nil
}
func (echoSession) Interrupt(context.Context) error               { return nil }
func (echoSession) Respond(context.Context, string, string) error { return nil }
func (echoSession) Close() error                                  { return nil }

// setup runs the module on a host of its own, saved at dbPath when it is not empty.
func setup(t *testing.T, dbPath string) (*host.Host, *service) {
	t.Helper()
	h := host.New(echoAgent{})
	if dbPath != "" {
		if err := h.OpenData(dbPath); err != nil {
			t.Fatal(err)
		}
	}
	s, err := newService(h, module.Store{DB: h.DB()})
	if err != nil {
		t.Fatal(err)
	}
	s.start()
	if err := h.Contribute([]module.Contribution{s.contribution()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.stop()
		h.Close()
	})
	return h, s
}

// userTexts is what the thread's user said, each as user:<text>.
func userTexts(t *testing.T, h *host.Host, id string) string {
	t.Helper()
	evs, err := h.ListEvents(rpc.Trusted(context.Background()), &inabav1.ListEventsRequest{ThreadId: id})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs.Events {
		if m := e.GetUserMessage(); m != nil {
			out = append(out, "user:"+m.Text)
		}
	}
	return strings.Join(out, " ")
}
