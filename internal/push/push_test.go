package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SherClockHolmes/webpush-go"

	"github.com/Alex4386/inaba/internal/store"
)

// A push service that takes the message is success; one that says the subscription is gone is ErrGone.
func TestWebSend(t *testing.T) {
	status := http.StatusCreated
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.WriteHeader(status)
	}))
	defer srv.Close()

	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdh.P256().GenerateKey(rand.Reader)
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	d := store.PushDevice{Endpoint: srv.URL + "/sub", P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(secret)}
	s := NewWeb(pub, priv, "admin@localhost")

	if err := s.Send(context.Background(), d, Message{Title: "t", Body: strings.Repeat("x", 5000), Tag: "thread-1", High: true}); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("Urgency") != "high" || len(got.Header.Get("Topic")) != 32 || !strings.HasPrefix(got.Header.Get("Authorization"), "vapid ") {
		t.Fatalf("headers: %v", got.Header)
	}
	status = http.StatusGone
	if err := s.Send(context.Background(), d, Message{Title: "t"}); !errors.Is(err, ErrGone) {
		t.Fatalf("gone: %v", err)
	}
	status = http.StatusInternalServerError
	if err := s.Send(context.Background(), d, Message{Title: "t"}); err == nil || errors.Is(err, ErrGone) {
		t.Fatalf("server error: %v", err)
	}
	// A redirect is not followed: the endpoint passed the allowlist, where it points does not.
	followed := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer elsewhere.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	d.Endpoint = redirect.URL
	if err := s.Send(context.Background(), d, Message{Title: "t"}); err == nil || followed {
		t.Fatalf("redirect: err %v, followed %v", err, followed)
	}
}
