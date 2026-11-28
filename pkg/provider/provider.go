// Package provider is the boundary between inaba and a coding agent.
//
// A Provider speaks one agent protocol. Everything agent-specific stays behind it;
// the host only sees Sessions and the events they emit. Abilities not every agent has
// are separate interfaces (RawCaller, TerminalResumer) found by type assertion.
package provider

import (
	"context"
	"encoding/json"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

type Config struct {
	Cwd string
	// Resume this native session instead of starting a new one, where the agent can.
	NativeSessionID string
	// MCP servers to give the agent for this session, on top of its own.
	MCPServers []MCPServer
	// The language the agent talks to people in, as a BCP 47 tag; empty leaves it to the agent.
	Language string
}

// MCPServer is an MCP server an agent can use: a command it starts, or an address
// it reaches over HTTP.
type MCPServer struct {
	Name string
	// For a server the agent starts: the command, its arguments and environment.
	Command string
	Args    []string
	Env     map[string]string
	// For a server reached over Streamable HTTP; Command is then empty.
	URL     string
	Headers map[string]string
}

// Emit publishes an event. Only the payload needs to be set.
// It must be safe to call from any goroutine.
type Emit func(*inabav1.Event)

type Provider interface {
	ID() string
	// Open starts the agent process and a session in it. ctx bounds startup only;
	// the session lives until Close.
	Open(ctx context.Context, cfg Config, emit Emit) (Session, error)
}

// Checker is a provider that can tell, without a session, whether its agent runs.
type Checker interface {
	Check(ctx context.Context) (Check, error)
}

// Check is what an agent says of itself when it starts.
type Check struct {
	Name    string
	Version string
}

// Prompt is one message from the user.
type Prompt struct {
	Text   string
	Images []Image
	// Files in the session's folder the message points the agent at, as absolute paths.
	Files []string
}

type Image struct {
	MimeType string
	Data     []byte
}

type Session interface {
	NativeSessionID() string
	// Prompt runs one turn and returns when it ends, with the agent's stop reason.
	// A returned error that is not a *TurnError means the session is no longer usable.
	Prompt(ctx context.Context, p Prompt) (stopReason string, err error)
	// Interrupt asks the running turn to stop; Prompt then returns.
	Interrupt(ctx context.Context) error
	// Respond answers a PermissionRequest. An empty optionID cancels it.
	Respond(ctx context.Context, requestID, optionID string) error
	Close() error
}

// TurnError is a turn the agent rejected or failed while the session stays usable.
type TurnError struct{ Err error }

func (e *TurnError) Error() string { return e.Err.Error() }
func (e *TurnError) Unwrap() error { return e.Err }

// TurnUsageReporter is a Session that can say what its last turn used. Implemented by
// Sessions whose agent reports it.
type TurnUsageReporter interface {
	// LastTurnUsage is the usage of the turn the last Prompt call ran, read after it
	// returns; nil when the agent did not say.
	LastTurnUsage() *inabav1.TurnUsage
}

// RawCaller sends a request of the agent's own protocol, for features inaba has no
// method for. Implemented by Sessions.
type RawCaller interface {
	Raw(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error)
}

// Configurable is a Session whose agent offers options of its own, such as the model
// or a permission mode. The session reports them, and every later change to them,
// as OptionsChanged events.
type Configurable interface {
	// SetOption changes one option to a value the agent offered for it.
	SetOption(ctx context.Context, id, value string) error
}

// TerminalResumer names the agent's own CLI, for terminal mode. Implemented by Providers.
type TerminalResumer interface {
	// TerminalCommand returns the argv that continues nativeSessionID interactively,
	// or starts a fresh CLI when it is empty.
	TerminalCommand(nativeSessionID string) []string
}

// TerminalOnly marks a Provider whose sessions are just a process in a terminal, such
// as a shell for long-running commands. Its threads never leave terminal mode and
// Open is never called.
type TerminalOnly interface {
	TerminalResumer
	TerminalOnly()
}

// JSONValue converts raw JSON into the Value that events carry; invalid JSON becomes null.
func JSONValue(raw json.RawMessage) *structpb.Value {
	v := &structpb.Value{}
	if len(raw) == 0 || protojson.Unmarshal(raw, v) != nil {
		return structpb.NewNullValue()
	}
	return v
}
