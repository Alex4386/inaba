package browser

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
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
			s.relay(w, r, in, thread, rest)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

// relay carries the agent's DevTools connection to the browser a message at a time, so each
// command it sends takes the browser for it, or waits while a person has it. What the browser
// sends back goes straight through.
func (s *service) relay(w http.ResponseWriter, r *http.Request, in *instance, thread, path string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	up, _, err := websocket.Dial(ctx, "ws://"+in.debug+path, nil)
	if err != nil {
		http.Error(w, "the browser did not answer", http.StatusBadGateway)
		return
	}
	defer up.CloseNow()
	// The path's token is what keeps others out; the agent's Playwright sends no Origin to check.
	down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer down.CloseNow()
	// Screenshots and page contents come whole in one message.
	up.SetReadLimit(-1)
	down.SetReadLimit(-1)
	defer in.agentDone(thread)
	go func() {
		defer cancel()
		for {
			typ, data, err := up.Read(ctx)
			if err != nil || down.Write(ctx, typ, data) != nil {
				return
			}
		}
	}()
	for {
		typ, data, err := down.Read(ctx)
		if err != nil {
			return
		}
		// Only a command that does something to a page is the agent using the browser; Playwright's own
		// bookkeeping, as it attaches to a tab or frame someone opened, goes straight through, at any time.
		var msg struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(data, &msg)
		if !passive(msg.Method) && in.agentActs(ctx, thread) != nil {
			return
		}
		if up.Write(ctx, typ, data) != nil {
			return
		}
	}
}

// passive is whether a DevTools command only sets up or reads a session, rather than acting on a page:
// what Playwright sends by itself for every target that appears, which Chrome holds paused until it does.
func passive(method string) bool {
	domain, name, _ := strings.Cut(method, ".")
	switch {
	case name == "enable" || name == "disable":
		return true
	case domain == "Target":
		return name != "createTarget" && name != "closeTarget" && name != "activateTarget"
	case domain == "Network", domain == "Emulation", domain == "Fetch", domain == "Log", domain == "Browser" && name == "getVersion":
		return true
	}
	switch method {
	case "Runtime.runIfWaitingForDebugger", "Runtime.addBinding", "Page.getFrameTree", "Page.setLifecycleEventsEnabled",
		"Page.addScriptToEvaluateOnNewDocument", "Page.createIsolatedWorld", "Page.setInterceptFileChooserDialog", "":
		return true
	}
	return false
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
