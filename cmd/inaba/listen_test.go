package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalOnly(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	for _, addr := range []string{":8081", "0.0.0.0:8081", "192.168.1.2:8081", "example.com:8081"} {
		if _, err := localOnly(addr, ok); err == nil {
			t.Fatalf("accepted %s", addr)
		}
	}
	h, err := localOnly("127.0.0.1:8081", ok)
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]int{"localhost:8081": 200, "127.0.0.1:8081": 200, "[::1]:8081": 200, "localhost": 200, "evil.example:8081": 403, "127.0.0.2:8081": 403} {
		r := httptest.NewRequest("POST", "/x", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("Host %s: %d", host, w.Code)
		}
	}
	// A browser request from another site is refused.
	r := httptest.NewRequest("POST", "/x", nil)
	r.Host = "localhost:8081"
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", w.Code)
	}
}
