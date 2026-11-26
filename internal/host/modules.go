package host

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/module"
	"github.com/Alex4386/inaba/pkg/provider"
)

// The host is what modules see of it.
var (
	_ module.Threads = (*Host)(nil)
	_ module.Events  = (*Host)(nil)
)

// Events a subscriber may fall behind by before new ones are dropped for it.
const subscriberBacklog = 1024

type subscriber struct {
	want func(*inabav1.Event) bool
	ch   chan module.ThreadEvent
}

// bus hands thread events to the modules that subscribed to them.
type bus struct {
	mu      sync.Mutex
	subs    map[*subscriber]struct{}
	deleted map[*int]func(threadID string)
}

func (h *Host) Follow(want func(*inabav1.Event) bool, handle func(module.ThreadEvent)) func() {
	s := &subscriber{want: want, ch: make(chan module.ThreadEvent, subscriberBacklog)}
	h.bus.mu.Lock()
	h.bus.subs[s] = struct{}{}
	h.bus.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range s.ch {
			handle(e)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.bus.mu.Lock()
			delete(h.bus.subs, s)
			h.bus.mu.Unlock()
			close(s.ch)
			<-done
		})
	}
}

func (h *Host) OnDelete(deleted func(threadID string)) func() {
	key := new(int)
	h.bus.mu.Lock()
	h.bus.deleted[key] = deleted
	h.bus.mu.Unlock()
	return func() {
		h.bus.mu.Lock()
		delete(h.bus.deleted, key)
		h.bus.mu.Unlock()
	}
}

// gone tells the modules that follow deletions that the thread is gone.
func (b *bus) gone(threadID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, deleted := range b.deleted {
		go deleted(threadID)
	}
}

// publish passes an event to its subscribers. The caller holds the thread's lock, so
// meta is the thread as the event left it.
func (b *bus) publish(meta *inabav1.Thread, e *inabav1.Event, copied bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var thread *inabav1.Thread
	for s := range b.subs {
		if !s.want(e) {
			continue
		}
		if thread == nil {
			thread = proto.Clone(meta).(*inabav1.Thread)
		}
		select {
		case s.ch <- module.ThreadEvent{Thread: thread, Event: e, Copied: copied}:
		default:
			slog.Warn("a module fell behind on events; dropped one", "thread", meta.Id, "seq", e.Seq)
		}
	}
}

func (h *Host) SetLabel(threadID, key, value string) error {
	t, err := h.thread(threadID)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if value == "" {
		delete(t.meta.Labels, key)
	} else {
		if t.meta.Labels == nil {
			t.meta.Labels = map[string]string{}
		}
		t.meta.Labels[key] = value
	}
	t.save.meta(t.meta)
	return nil
}

// ModuleController turns the host's modules on and off. The host asks it for
// ModuleService; it gives the host what the running modules serve, by Contribute.
type ModuleController interface {
	List() []*inabav1.Module
	Set(name string, enabled bool) (*inabav1.Module, error)
}

func (h *Host) SetModuleController(c ModuleController) { h.moduleCtl.Store(&c) }

func (h *Host) ListModules(context.Context, *inabav1.ListModulesRequest) (*inabav1.ListModulesResponse, error) {
	c := h.moduleCtl.Load()
	if c == nil {
		return &inabav1.ListModulesResponse{}, nil
	}
	return &inabav1.ListModulesResponse{Modules: (*c).List()}, nil
}

func (h *Host) SetModule(ctx context.Context, req *inabav1.SetModuleRequest) (*inabav1.Module, error) {
	if err := may(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	c := h.moduleCtl.Load()
	if c == nil {
		return nil, connect.NewError(connect.CodeNotFound, "this host has no modules")
	}
	return (*c).Set(req.Name, req.Enabled)
}

// served is what the host answers with at the moment: its own API and what its
// running modules add. Contribute replaces it whole.
type served struct {
	api     atomic.Pointer[http.Handler]
	control atomic.Pointer[http.Handler]
	// peers answers direct connections; registered in every rebuild of the API.
	peers *peers
	// contributions are the running modules', kept for rebuilds.
	mu            sync.Mutex
	contributions []module.Contribution
	// services are the core's own that live outside this package, kept for rebuilds.
	services []func(*connect.Server)
}

// AddService serves a core service that lives outside this package, on the API and
// the control socket alike.
func (h *Host) AddService(register func(*connect.Server)) error {
	h.served.mu.Lock()
	defer h.served.mu.Unlock()
	h.served.services = append(h.served.services, register)
	return h.rebuild()
}

// Contribute makes the host serve what the modules give, in place of what earlier
// modules gave. Calls in flight finish on what they started with.
func (h *Host) Contribute(contributions []module.Contribution) error {
	h.served.mu.Lock()
	defer h.served.mu.Unlock()
	h.served.contributions = contributions
	return h.rebuild()
}

// rebuild makes the API and control handlers from the host's services and the
// modules'. The caller holds served.mu.
func (h *Host) rebuild() error {
	core := func(server *connect.Server) {
		inabav1connect.RegisterThreadServiceHandler(server, h)
		inabav1connect.RegisterProjectServiceHandler(server, h)
		inabav1connect.RegisterHousekeepingServiceHandler(server, h)
		inabav1connect.RegisterModuleServiceHandler(server, h)
		inabav1connect.RegisterSystemServiceHandler(server, h)
		inabav1connect.RegisterUsageServiceHandler(server, h)
		for _, register := range h.served.services {
			register(server)
		}
		for _, c := range h.served.contributions {
			if c.Services != nil {
				c.Services(server)
			}
		}
	}

	server := connect.NewServer()
	core(server)
	inabav1connect.RegisterTerminalServiceHandler(server, h)
	inabav1connect.RegisterGitServiceHandler(server, h)
	// For inaba-server only; its relay keeps browsers off it.
	inabav1connect.RegisterAlertServiceHandler(server, alerts{h: h})
	if h.served.peers != nil {
		inabav1connect.RegisterPeerServiceHandler(server, h.served.peers)
	}
	// The REST mapping claims "/" on its mux; modules' own routes sit in front of it.
	api := http.NewServeMux()
	if err := rpc.Mount(api, server, streams...); err != nil {
		return err
	}
	var top http.Handler = api
	if routes := h.routes(); len(routes) > 0 {
		front := http.NewServeMux()
		for pattern, handler := range routes {
			front.Handle(pattern, handler)
		}
		front.Handle("/", api)
		top = front
	}
	h.served.api.Store(&top)

	control := connect.NewServer(func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			return next(Trusted(ctx), spec, stream)
		}
	})
	core(control)
	controlMux := http.NewServeMux()
	connecthttp.Mount(controlMux, control)
	var controlHandler http.Handler = controlMux
	h.served.control.Store(&controlHandler)
	return nil
}

// sessionMCP is the MCP servers the running modules give an agent session of the thread.
func (h *Host) sessionMCP(thread *inabav1.Thread) []provider.MCPServer {
	h.served.mu.Lock()
	contributions := h.served.contributions
	h.served.mu.Unlock()
	var out []provider.MCPServer
	for _, c := range contributions {
		if c.MCPServers != nil {
			out = append(out, c.MCPServers(thread)...)
		}
	}
	return out
}

func (h *Host) routes() map[string]http.Handler {
	routes := map[string]http.Handler{}
	for _, c := range h.served.contributions {
		for pattern, handler := range c.Routes {
			routes[pattern] = handler
		}
	}
	return routes
}

// ControlHandler is what the host's local control socket serves to its own CLI:
// the host's API and its modules', with every call trusted.
func (h *Host) ControlHandler() http.Handler {
	h.ensureBuilt()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*h.served.control.Load()).ServeHTTP(w, r)
	})
}

// ControlServer serves the host's own API, trusted, without any module's: for the
// CLI to use in-process when no host is running.
func (h *Host) ControlServer() *connect.Server {
	server := connect.NewServer(func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			return next(Trusted(ctx), spec, stream)
		}
	})
	inabav1connect.RegisterThreadServiceHandler(server, h)
	inabav1connect.RegisterProjectServiceHandler(server, h)
	inabav1connect.RegisterHousekeepingServiceHandler(server, h)
	return server
}

// Handler serves the host API, its modules' included. It does no authentication:
// only inaba-server, which does, should be able to reach it.
func (h *Host) Handler() (http.Handler, error) {
	outer := http.NewServeMux()
	h.served.mu.Lock()
	h.served.peers = newPeers(outer)
	err := h.rebuild()
	h.served.mu.Unlock()
	if err != nil {
		return nil, err
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*h.served.api.Load()).ServeHTTP(w, r)
	})
	outer.HandleFunc("/v1/threads/{thread_id}/git/{rest...}", h.threadGit(api))
	outer.Handle("/", api)
	return outer, nil
}

func (h *Host) ensureBuilt() {
	h.served.mu.Lock()
	defer h.served.mu.Unlock()
	if h.served.control.Load() == nil {
		if err := h.rebuild(); err != nil {
			slog.Error("could not build the host's API", "error", err.Error())
		}
	}
}

// DB is the host's database, for modules' own tables; nil for a host without one.
func (h *Host) DB() *gorm.DB {
	if h.save == nil {
		return nil
	}
	return h.save.db
}

// DataDir is the host's data folder; empty for a host without a database.
func (h *Host) DataDir() string { return h.dataDir }

// streams are the host's server streams at GET paths, as JSON lines or server-sent events.
var streams = []rpc.Stream{
	{Pattern: "GET /v1/threads/{thread_id}/events/watch", Procedure: inabav1connect.ThreadServiceSubscribeProcedure, Request: func() proto.Message { return &inabav1.SubscribeRequest{} }},
	{Pattern: "GET /v1/threads/{thread_id}/tools/{tool_call_id}/agents/{agent_id}/watch", Procedure: inabav1connect.ThreadServiceWatchWorkflowAgentProcedure, Request: func() proto.Message { return &inabav1.GetWorkflowAgentRequest{} }},
	{Pattern: "GET /v1/threads/{thread_id}/terminal/output", Procedure: inabav1connect.TerminalServiceAttachProcedure, Request: func() proto.Message { return &inabav1.AttachRequest{} }},
	{Pattern: "GET /v1/threads/{thread_id}/shells/{shell_id}/output", Procedure: inabav1connect.ShellServiceAttachShellProcedure, Request: func() proto.Message { return &inabav1.AttachShellRequest{} }},
	{Pattern: "GET /v1/threads/{thread_id}/browser/frames", Procedure: inabav1connect.BrowserServiceWatchPageProcedure, Request: func() proto.Message { return &inabav1.WatchPageRequest{} }},
}
