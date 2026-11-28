// Package browser is the browser module: a browser on the host for each thread, which
// the thread's agent drives through Playwright MCP and people watch and use from the web.
package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"go.uber.org/fx"
	"gorm.io/gorm"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/proc"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/module"
	"github.com/Alex4386/inaba/pkg/provider"
)

// Module is the browser module, for a host to run. It needs Chrome or Chromium on the
// host, and Node for the agent's Playwright MCP.
var Module = module.Module{
	Name:        "browser",
	Title:       "Browser",
	Description: "A browser for each thread, which its agent drives through Playwright MCP and you can watch and use. Needs Chrome or Chromium, and Node, on the host.",
	// On, as an agent that can see and use a page does more; a host without Chrome or Node
	// keeps it off by itself, saying which it lacks.
	Default: true,
	Option: fx.Options(
		fx.Provide(newService),
		module.Provide((*service).contribution),
		fx.Invoke(func(lc fx.Lifecycle, s *service) { lc.Append(fx.StartStopHook(s.start, s.stop)) }),
	),
}

const (
	// A browser no one has used for this long is stopped; its profile stays.
	idleTimeout = 30 * time.Minute
	// How often browsers are checked for being idle or their thread gone.
	reapEvery = time.Minute
	// The size of a new browser window, in CSS pixels.
	windowSize = "1280,800"
	// The Playwright MCP the agent drives the browser with.
	playwrightMCP = "@playwright/mcp@latest"
)

// instance is one thread's running browser.
type instance struct {
	launcher *launcher.Launcher
	browser  *rod.Browser
	// debug is Chrome's DevTools address, host:port.
	debug string

	mu       sync.Mutex
	lastUsed time.Time
	// users counts what has it open now: viewers and the agent's connections.
	users int
	// Who has it, the thread whose agent last used it, and how many people watch its pages.
	control     inabav1.BrowserControl
	agentThread string
	viewers     int
	// changed is closed, and replaced, whenever control or an agent's cursor changes.
	changed chan struct{}
	// Where the agent last put the mouse, by page (target) id.
	cursors map[string]*inabav1.AgentCursor
	// unwatched gives back a taken browser once no one has watched it for a while; restarted by each who leaves.
	unwatched *time.Timer
	// emulated undoes, by page and then by the agent's session, a viewport its Playwright set in place of the window's.
	emulated map[string]map[string]func()
}

func (in *instance) touch(delta int) {
	in.mu.Lock()
	in.users += delta
	in.lastUsed = time.Now()
	in.mu.Unlock()
}

func (in *instance) idle() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.users <= 0 && time.Since(in.lastUsed) > idleTimeout
}

func (in *instance) close() {
	_ = in.browser.Close()
	in.launcher.Kill()
}

type service struct {
	inabav1connect.UnimplementedBrowserServiceHandler
	threads module.Threads
	events  module.Events
	// stopFollow ends the module's hearing of deleted threads.
	stopFollow func()
	dir        string
	bin        string

	// The DevTools proxy the agents' Playwright MCP connects through, and the token in
	// its paths that keeps other local users out.
	listener net.Listener
	token    string

	db *gorm.DB

	mu sync.Mutex
	// browsers are the running ones, by key: a thread id, or a project's key when its threads share one.
	browsers map[string]*instance
	// keyOf is the browser each thread last used, by thread id.
	keyOf    map[string]string
	settings *inabav1.BrowserSettings
	// starting holds a thread's browser while it starts, so two callers start one.
	starting map[string]chan struct{}
	// stopped is set once the module stops; a late call must not start a browser no one will stop.
	stopped bool
	// casts are the pages being watched, by page id: one screencast each, however many watch.
	casts map[string]*cast

	done chan struct{}
}

func newService(threads module.Threads, events module.Events, dataDir module.DataDir, store module.Store) (*service, error) {
	settings, err := loadSettings(store.DB)
	if err != nil {
		return nil, err
	}
	bin := os.Getenv("INABA_BROWSER")
	if bin == "" {
		found, ok := launcher.LookPath()
		if !ok {
			return nil, errors.New("no Chrome or Chromium found on this host; install one, or set INABA_BROWSER to its path")
		}
		bin = found
	}
	// The agent's Playwright MCP runs with npx: without it every agent would be given a tool that never starts.
	if _, err := exec.LookPath("npx"); err != nil {
		return nil, errors.New("no Node found on this host, which the agent's Playwright MCP runs with; install Node, so npx is on the PATH")
	}
	dir := filepath.Join(string(dataDir), "browser")
	if dataDir == "" {
		var err error
		if dir, err = os.MkdirTemp("", "inaba-browser-"); err != nil {
			return nil, err
		}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &service{threads: threads, events: events, db: store.DB, settings: settings, keyOf: map[string]string{}, dir: dir, bin: bin, token: hex.EncodeToString(b), browsers: map[string]*instance{}, starting: map[string]chan struct{}{}, casts: map[string]*cast{}, done: make(chan struct{})}, nil
}

func (s *service) start() error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("could not open the DevTools proxy: %w", err)
	}
	s.listener = l
	go func() { _ = http.Serve(l, s.proxy()) }()
	go s.reap()
	stopDeletes := s.events.OnDelete(s.forget)
	// An agent has the browser until its turn ends.
	stopTurns := s.events.Follow(func(e *inabav1.Event) bool { return e.GetTurnEnded() != nil }, func(e module.ThreadEvent) {
		if !e.Copied {
			s.turnEnded(e.Thread.GetId())
		}
	})
	s.stopFollow = func() { stopDeletes(); stopTurns() }
	return nil
}

// forget stops a deleted thread's own browser and removes its profile; a browser its project's
// other threads share stays.
func (s *service) forget(threadID string) {
	s.mu.Lock()
	in := s.browsers[threadID]
	delete(s.browsers, threadID)
	delete(s.keyOf, threadID)
	s.mu.Unlock()
	if in != nil {
		in.close()
	}
	_ = os.RemoveAll(filepath.Join(s.dir, threadID))
	_ = os.RemoveAll(s.output(threadID))
}

func (s *service) stop() {
	if s.stopFollow != nil {
		s.stopFollow()
	}
	close(s.done)
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.mu.Lock()
	s.stopped = true
	browsers := s.browsers
	s.browsers = map[string]*instance{}
	s.mu.Unlock()
	for _, in := range browsers {
		in.close()
	}
}

func (s *service) contribution() module.Contribution {
	return module.Contribution{
		Services:   func(server *connect.Server) { inabav1connect.RegisterBrowserServiceHandler(server, s) },
		MCPServers: s.mcpFor,
	}
}

// mcpFor gives the thread's agent Playwright MCP, on the thread's browser.
func (s *service) mcpFor(thread *inabav1.Thread) []provider.MCPServer {
	return []provider.MCPServer{{
		Name:    "browser",
		Command: "npx",
		// Its snapshots and logs go to the module's folder, not the thread's: they are not the thread's work.
		Args: []string{"-y", playwrightMCP, "--cdp-endpoint", s.endpoint(thread.Id), "--output-dir", s.output(thread.Id)},
	}}
}

// output is where the thread's Playwright MCP writes the files it names itself.
func (s *service) output(threadID string) string { return filepath.Join(s.dir, threadID+"-output") }

// endpoint is where Playwright finds the thread's browser.
func (s *service) endpoint(threadID string) string {
	return "http://" + s.listener.Addr().String() + "/" + s.token + "/" + threadID
}

// open returns the thread's browser, starting it if it is not running. Which browser that is,
// and how it starts, follows the host's browser settings and the thread's project's.
func (s *service) open(ctx context.Context, threadID string) (*instance, error) {
	thread, err := s.threads.GetThread(rpc.Trusted(ctx), &inabav1.GetThreadRequest{ThreadId: threadID})
	if err != nil {
		return nil, err
	}
	var p plan
	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return nil, connect.NewError(connect.CodeUnavailable, "the browser module is stopping")
		}
		p = s.planFor(thread)
		s.keyOf[threadID] = p.key
		if in := s.browsers[p.key]; in != nil {
			s.mu.Unlock()
			in.touch(0)
			return in, nil
		}
		wait, busy := s.starting[p.key]
		if !busy {
			wait = make(chan struct{})
			s.starting[p.key] = wait
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	in, err := s.launch(p)
	s.mu.Lock()
	if err == nil && s.stopped {
		// Stopped while this one started: it would outlive the module.
		in.close()
		in, err = nil, connect.NewError(connect.CodeUnavailable, "the browser module is stopping")
	}
	if err == nil {
		s.browsers[p.key] = in
	}
	close(s.starting[p.key])
	delete(s.starting, p.key)
	s.mu.Unlock()
	return in, err
}

func (s *service) launch(p plan) (*instance, error) {
	profile := filepath.Join(s.dir, p.key)
	clearStaleLock(profile)
	// leakless watches the host: if it dies without stopping its browsers, they go too.
	l := withFlags(launcher.New().Bin(s.bin).Headless(true).
		UserDataDir(profile).
		Set("window-size", windowSize), p.flags)
	u, err := l.Launch()
	if err != nil {
		l.Kill()
		return nil, connect.NewError(connect.CodeUnavailable, "could not start the browser: "+err.Error())
	}
	// Viewport and device are the page's own business, and Playwright's: no emulation of rod's.
	b := rod.New().ControlURL(u).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, connect.NewError(connect.CodeUnavailable, "could not reach the browser: "+err.Error())
	}
	in := &instance{launcher: l, browser: b, debug: hostOf(u), lastUsed: time.Now()}
	if pages, err := b.Pages(); err == nil && len(pages) == 0 {
		_, _ = b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	}
	slog.Info("browser started", "browser", p.key, "flags", len(p.flags))
	return in, nil
}

// reap stops browsers no one uses, and removes those of threads that are gone.
func (s *service) reap() {
	t := time.NewTicker(reapEvery)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		s.reapOnce()
	}
}

// reapOnce looks at each browser once.
func (s *service) reapOnce() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.browsers))
	for id := range s.browsers {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		// A project's shared browser outlives any one thread; only being idle stops it.
		// shortcut: a deleted project's shared profile stays on disk; remove it with the project when that matters.
		gone := false
		if !strings.HasPrefix(id, projectKey) {
			_, err := s.threads.GetThread(rpc.Trusted(context.Background()), &inabav1.GetThreadRequest{ThreadId: id})
			gone = connect.CodeOf(err) == connect.CodeNotFound
		}
		s.mu.Lock()
		in := s.browsers[id]
		if in != nil && (gone || in.idle()) {
			delete(s.browsers, id)
		} else {
			in = nil
		}
		s.mu.Unlock()
		if in == nil {
			continue
		}
		in.close()
		if gone {
			_ = os.RemoveAll(filepath.Join(s.dir, id))
			_ = os.RemoveAll(s.output(id))
		}
		slog.Info("browser stopped", "thread", id, "thread_gone", gone)
	}
}

func (s *service) OpenBrowser(ctx context.Context, req *inabav1.OpenBrowserRequest) (*inabav1.BrowserState, error) {
	in, err := s.open(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	return state(in)
}

func (s *service) CloseBrowser(_ context.Context, req *inabav1.CloseBrowserRequest) (*inabav1.CloseBrowserResponse, error) {
	s.mu.Lock()
	key := req.ThreadId
	if k, ok := s.keyOf[req.ThreadId]; ok {
		key = k
	}
	in := s.browsers[key]
	delete(s.browsers, key)
	s.mu.Unlock()
	if in != nil {
		in.close()
	}
	return &inabav1.CloseBrowserResponse{}, nil
}

// state lists the browser's pages, oldest first.
func state(in *instance) (*inabav1.BrowserState, error) {
	pages, err := in.browser.Pages()
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the browser did not answer: "+err.Error())
	}
	res := &inabav1.BrowserState{Control: in.controlNow()}
	// Chrome lists the newest first.
	for i := len(pages) - 1; i >= 0; i-- {
		info, err := pages[i].Info()
		if err != nil {
			continue
		}
		res.Pages = append(res.Pages, &inabav1.BrowserPage{Id: string(info.TargetID), Url: info.URL, Title: info.Title})
	}
	return res, nil
}

// clearStaleLock removes a profile's lock left by a browser that is gone, as after a
// crash: Chrome refuses a profile whose lock exists. A lock of a browser still running
// is left alone; that browser keeps the profile.
func clearStaleLock(profile string) {
	target, err := os.Readlink(filepath.Join(profile, "SingletonLock"))
	if err != nil {
		return
	}
	// The lock names its holder as host-pid.
	i := strings.LastIndex(target, "-")
	pid, err := strconv.Atoi(target[i+1:])
	if err == nil && pid > 0 && proc.Alive(pid) {
		return
	}
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		_ = os.Remove(filepath.Join(profile, name))
	}
}
