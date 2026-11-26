package push

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/store"
)

// The server must not post into its own network on a user's behalf, whatever the address looks like.
func TestChannelRefusesPrivate(t *testing.T) {
	cs := NewChannels(false)
	for _, to := range []string{"http://127.0.0.1:9/x", "http://10.1.2.3/x", "http://[::1]:9/x", "http://localhost:9/x", "http://169.254.169.254/latest", "http://100.64.0.1/x", "http://0.0.0.0:9/x", "http://0.1.2.3:9/x", "http://198.18.0.1/x", "http://[::ffff:127.0.0.1]:9/x"} {
		_, err := cs.Send(&store.Channel{Kind: "webhook", URL: to}, Event{Alert: &inabav1.Alert{Event: "test"}})
		if !errors.Is(err, ErrPrivate) {
			t.Fatalf("%s: %v", to, err)
		}
	}
}

func TestChannelChecks(t *testing.T) {
	for _, bad := range []store.Channel{
		{Kind: "webhook", URL: "file:///etc/passwd"},
		{Kind: "webhook", URL: "gopher://x"},
		{Kind: "ntfy", URL: "https://ntfy.sh", Topic: "no spaces"},
		{Kind: "ntfy", URL: "https://ntfy.sh/my-topic", Topic: "my-topic"},
		{Kind: "discord", URL: "https://evil.example/api/webhooks/1/x"},
		{Kind: "discord", URL: "https://discord.com.evil.example/api/webhooks/1/x"},
		{Kind: "discord", URL: "http://discord.com/api/webhooks/1/x"},
		{Kind: "slack", URL: "https://hooks.slack.com:8443/services/x"},
		{Kind: "telegram", Secret: "nope", ChatID: "1"},
	} {
		if Kinds[bad.Kind].Check(&bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	for _, good := range []store.Channel{
		{Kind: "webhook", URL: "https://example.com/hook"},
		{Kind: "ntfy", URL: "https://ntfy.sh", Topic: "my-topic_1"},
		{Kind: "discord", URL: "https://discord.com/api/webhooks/1/abc"},
		{Kind: "slack", URL: "https://hooks.slack.com/services/T/B/x"},
		{Kind: "telegram", Secret: "123456:ABCdefGHIjklMNOpqrSTUvwx", ChatID: "-100123"},
	} {
		if err := Kinds[good.Kind].Check(&good); err != nil {
			t.Fatalf("refused %+v: %v", good, err)
		}
	}
}

// A webhook delivery is signed with the channel's secret, and a server error is tried again.
func TestWebhookSignature(t *testing.T) {
	tries := 0
	var body []byte
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tries++
		body, _ = io.ReadAll(r.Body)
		got = r.Header
		if tries == 1 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	cs := NewChannels(true)
	cs.retryDelays = []time.Duration{0, 0, 0}
	c := &store.Channel{Kind: "webhook", URL: srv.URL, Secret: "s3cret"}
	status, err := cs.Send(c, Event{HostID: "h", Alert: &inabav1.Alert{Event: "turn.failed", ThreadId: "t1", Error: "boom", Automation: true}, Delivery: "d1"})
	if err != nil || status != 200 || tries != 2 {
		t.Fatalf("status %d, tries %d: %v", status, tries, err)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	if got.Get("X-Inaba-Signature-256") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || got.Get("X-Inaba-Event") != "turn.failed" || got.Get("X-Inaba-Delivery") != "d1" {
		t.Fatalf("headers: %v", got)
	}
	var payload struct {
		Event, Error string
		Thread       struct {
			ID         string `json:"id"`
			Automation bool
		}
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Event != "turn.failed" || payload.Error != "boom" || payload.Thread.ID != "t1" || !payload.Thread.Automation {
		t.Fatalf("payload %s: %v", body, err)
	}
}
