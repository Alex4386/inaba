package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestHandlerCachesAndCompresses(t *testing.T) {
	js := bytes.Repeat([]byte("console.log('inaba');\n"), 200)
	h := handler(fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><title>inaba</title>")},
		"assets/main-abc.js":    {Data: js},
		"assets/font-abc.woff2": {Data: []byte("not text")},
	})
	get := func(path, accept string) *http.Response {
		r := httptest.NewRequest("GET", path, nil)
		if accept != "" {
			r.Header.Set("Accept-Encoding", accept)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Result()
	}

	res := get("/assets/main-abc.js", "br, gzip")
	if res.Header.Get("Content-Encoding") != "gzip" || res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("hashed asset headers: %v", res.Header)
	}
	if got := res.Header.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("content type of gzipped js: %q", got)
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(zr); !bytes.Equal(body, js) {
		t.Fatal("gzipped body does not decode to the file")
	}

	// Asked again, the same bytes come back: the file was compressed once.
	first, _ := io.ReadAll(get("/assets/main-abc.js", "gzip").Body)
	second, _ := io.ReadAll(get("/assets/main-abc.js", "gzip").Body)
	if !bytes.Equal(first, second) || len(second) >= len(js) {
		t.Fatalf("second gzipped response: %d bytes of %d", len(second), len(js))
	}

	if res := get("/assets/main-abc.js", "gzip;q=0"); res.Header.Get("Content-Encoding") != "" {
		t.Fatal("gzip sent to a client that refused it")
	}
	if body, _ := io.ReadAll(get("/assets/main-abc.js", "").Body); !bytes.Equal(body, js) {
		t.Fatal("plain asset body")
	}
	if res := get("/assets/font-abc.woff2", "gzip"); res.Header.Get("Content-Encoding") != "" {
		t.Fatal("an already compressed font was gzipped")
	}
	// A client-side route gets index.html, never cached without asking.
	res = get("/threads/x", "gzip")
	if res.Header.Get("Cache-Control") != "no-cache" || res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("index headers: %v", res.Header)
	}
}
