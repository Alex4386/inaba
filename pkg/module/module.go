// Package module is the boundary between an inaba host and its optional parts.
//
// A host always runs sessions. Everything else that can be turned off, such as
// automations, is a Module: an fx.Option that the host assembles, with whatever
// else is turned on, into an fx app of its own. Modules reach the host only through
// the interfaces here, and give the host what it serves through Contributions.
// Turning a module on or off rebuilds that app while the host keeps running.
package module

import (
	"context"
	"net/http"

	"connectrpc.com/connect/v2"
	"go.uber.org/fx"
	"gorm.io/gorm"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/pkg/provider"
)

// Module is one optional part of a host.
type Module struct {
	// Name identifies it, in the API and in the host's settings. Lower case, no spaces.
	Name        string
	Title       string
	Description string
	// Whether it runs on a host that has never been told either way.
	Default bool
	// Option builds it. It may take any of the values below as dependencies, and
	// provides Contributions to the "contributions" group.
	Option fx.Option
}

// Contribution is what a module adds to the host's API.
type Contribution struct {
	// Services registers the module's Connect services. Their REST mappings come
	// from the services' own google.api.http options, as for the host's.
	Services func(*connect.Server)
	// Routes are plain HTTP handlers by ServeMux pattern, such as a webhook. They
	// are served outside the API, with no permission check of the host's.
	Routes map[string]http.Handler
	// MCPServers adds MCP servers to an agent session as it starts, for the thread.
	// It must be quick: the session waits for it.
	MCPServers func(thread *inabav1.Thread) []provider.MCPServer
}

// Provide puts a Contribution constructor into the group the host collects.
func Provide(constructor any) fx.Option {
	return fx.Provide(fx.Annotate(constructor, fx.ResultTags(`group:"contributions"`)))
}

// Threads is what a module may do with the host's sessions. Calls are checked like
// the API's: pass a context from Trusted to act on the host's own authority.
type Threads interface {
	ListProviders(context.Context, *inabav1.ListProvidersRequest) (*inabav1.ListProvidersResponse, error)
	CreateThread(context.Context, *inabav1.CreateThreadRequest) (*inabav1.Thread, error)
	GetThread(context.Context, *inabav1.GetThreadRequest) (*inabav1.Thread, error)
	StartTurn(context.Context, *inabav1.StartTurnRequest) (*inabav1.StartTurnResponse, error)
	ListThreads(context.Context, *inabav1.ListThreadsRequest) (*inabav1.ListThreadsResponse, error)
	ListEvents(context.Context, *inabav1.ListEventsRequest) (*inabav1.ListEventsResponse, error)
	Interrupt(context.Context, *inabav1.InterruptRequest) (*inabav1.InterruptResponse, error)
	// SetLabel sets one of the thread's labels; an empty value removes it.
	SetLabel(threadID, key, value string) error
}

// ThreadEvent is an event of a thread, with the thread as it was right after it.
type ThreadEvent struct {
	Thread *inabav1.Thread
	Event  *inabav1.Event
	// Copied from another thread, as when a thread is forked: history, not news.
	Copied bool
}

// Events lets a module follow what happens in sessions.
type Events interface {
	// Follow calls handle, on a goroutine of its own and in order, with each event
	// that want accepts. want runs while the thread is locked: keep it to a look at
	// the event. Events past a backlog the handler cannot keep up with are dropped
	// and logged. The returned func ends the subscription.
	Follow(want func(*inabav1.Event) bool, handle func(ThreadEvent)) (stop func())
	// OnDelete calls deleted, on a goroutine of its own, with the id of each thread
	// deleted from now on, so a module can let go of what it keeps for it.
	OnDelete(deleted func(threadID string)) (stop func())
}

// Store is the host's database, for a module's own tables. Prefix table names with
// the module's name.
type Store struct{ *gorm.DB }

// DataDir is the host's data folder, for a module's own files. Use a subfolder
// named after the module.
type DataDir string
