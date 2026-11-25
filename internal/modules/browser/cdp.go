package browser

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// hostOf is the host:port of a DevTools address such as ws://127.0.0.1:9222/devtools/browser/x.
func hostOf(controlURL string) string {
	u, err := url.Parse(controlURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// proxy serves each thread's browser to the agent's Playwright, at
// /{token}/{thread}/: DevTools' discovery document, which it starts the browser for,
// and the DevTools WebSocket behind it.
func (s *service) proxy() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/{token}/{thread}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.PathValue("token")), []byte(s.token)) != 1 {
			http.NotFound(w, r)
			return
		}
		thread, rest := r.PathValue("thread"), "/"+r.PathValue("rest")
		in, err := s.open(r.Context(), thread)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		switch {
		case strings.HasPrefix(rest, "/json/version"):
			s.version(w, r, in, thread)
		case strings.HasPrefix(rest, "/devtools/"):
			// The connection counts as use for as long as it is open.
			in.touch(1)
			defer in.touch(-1)
			target := &url.URL{Scheme: "http", Host: in.debug}
			(&httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.URL.Path, pr.Out.URL.RawPath = rest, ""
				pr.Out.Host = in.debug
			}}).ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

// version answers DevTools' discovery request with the browser's own answer, its
// WebSocket address changed to come through this proxy.
func (s *service) version(w http.ResponseWriter, r *http.Request, in *instance, thread string) {
	res, err := http.Get("http://" + in.debug + "/json/version")
	if err != nil {
		http.Error(w, "the browser did not answer", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	var doc map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc); err != nil {
		http.Error(w, "the browser answered oddly", http.StatusBadGateway)
		return
	}
	if ws, ok := doc["webSocketDebuggerUrl"].(string); ok {
		if u, err := url.Parse(ws); err == nil {
			doc["webSocketDebuggerUrl"] = "ws://" + r.Host + "/" + s.token + "/" + thread + u.Path
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}
