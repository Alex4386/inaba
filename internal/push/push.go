// Package push sends notifications to people's devices: browsers through Web Push for
// now, native apps later through their own senders.
package push

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/SherClockHolmes/webpush-go"

	"github.com/Alex4386/inaba/internal/store"
)

// Message is one notification. Tag collapses repeats, such as several about one thread.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
	// High asks the push service to wake the device, for something waiting on its person,
	// and the device to keep it on screen until it is dealt with.
	High bool `json:"high,omitempty"`
}

// Sender delivers a message to one device of its platform.
type Sender interface {
	Send(ctx context.Context, d store.PushDevice, m Message) error
}

// ErrGone is a device its push service no longer knows; the caller deletes it.
var ErrGone = errors.New("push: device is gone")

// Limits on what is sent, well under the 4 KiB a push payload may be once encrypted.
const (
	maxTitle = 120
	maxBody  = 300
	ttl      = 3600
)

type web struct {
	public, private, subscriber string
	client                      webpush.HTTPClient
}

// NewWeb sends through Web Push, signed with the server's VAPID keys. subscriber is how a
// push service can reach whoever runs the server: an https URL or an e-mail address.
func NewWeb(public, private, subscriber string) Sender {
	return &web{public: public, private: private, subscriber: subscriber, client: noRedirects}
}

// noRedirects keeps a push to the endpoint that passed the allowlist: following a redirect
// from a push service would let it send the server anywhere, internal addresses included.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (w *web) Send(ctx context.Context, d store.PushDevice, m Message) error {
	m.Title, m.Body = cut(m.Title, maxTitle), cut(m.Body, maxBody)
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	urgency := webpush.UrgencyNormal
	if m.High {
		urgency = webpush.UrgencyHigh
	}
	res, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: d.Endpoint, Keys: webpush.Keys{P256dh: d.P256dh, Auth: d.Auth}}, &webpush.Options{
		HTTPClient: w.client, Subscriber: w.subscriber, VAPIDPublicKey: w.public, VAPIDPrivateKey: w.private,
		TTL: ttl, Urgency: urgency, Topic: topic(m.Tag),
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return ErrGone
	case res.StatusCode < 200 || res.StatusCode > 299:
		return fmt.Errorf("push: the push service answered %d", res.StatusCode)
	}
	return nil
}

// topic is a tag as a Topic header allows it: at most 32 URL-safe characters.
func topic(tag string) string {
	if tag == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tag))
	return base64.RawURLEncoding.EncodeToString(sum[:24])
}

// cut shortens s to at most n bytes without splitting a character.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n-len("…")]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}
