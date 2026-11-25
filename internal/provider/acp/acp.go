// Package acp drives any agent that speaks the Agent Client Protocol on stdio, through
// github.com/coder/acp-go-sdk.
package acp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	sdk "github.com/coder/acp-go-sdk"
	"google.golang.org/protobuf/types/known/structpb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/mcpbridge"
	"github.com/Alex4386/inaba/internal/proc"
	"github.com/Alex4386/inaba/pkg/provider"
)

type Provider struct {
	Name string
	// Command starts the agent in ACP mode.
	Command []string
	// Terminal starts the agent's interactive CLI; empty disables terminal mode.
	Terminal []string
	// TerminalResume continues a session interactively. "{session}" is replaced with
	// the native session id. Falls back to Terminal when empty.
	TerminalResume []string
	Env            []string
}

func (p *Provider) ID() string { return p.Name }

func (p *Provider) TerminalCommand(nativeSessionID string) []string {
	if nativeSessionID == "" || len(p.TerminalResume) == 0 {
		return p.Terminal
	}
	argv := make([]string, len(p.TerminalResume))
	for i, a := range p.TerminalResume {
		argv[i] = strings.ReplaceAll(a, "{session}", nativeSessionID)
	}
	return argv
}

type session struct {
	cmd   *exec.Cmd
	group *proc.Group
	stdin io.Closer
	conn  *sdk.ClientSideConnection
	emit  provider.Emit
	id    sdk.SessionId

	mu sync.Mutex
	// Loading a session replays its history as updates the event log already holds.
	replaying bool
	pending   map[string]chan string
	// modes is set for an agent that offers session modes but no config options.
	modes *sdk.SessionModeState
	// booleans are the config options that are on/off switches, by id.
	booleans map[string]bool
	// The agent takes images in prompts.
	images bool
	// What the last turn used, as its PromptResponse said.
	turnUsage *inabav1.TurnUsage
}

func (p *Provider) Open(ctx context.Context, cfg provider.Config, emit provider.Emit) (provider.Session, error) {
	s, err := p.spawn(cfg.Cwd, emit)
	if err != nil {
		return nil, err
	}
	if err := s.start(ctx, cfg); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Check starts the agent and introduces itself, without opening a session, to learn
// whether it runs and what it says it is.
func (p *Provider) Check(ctx context.Context) (provider.Check, error) {
	s, err := p.spawn(os.TempDir(), func(*inabav1.Event) {})
	if err != nil {
		return provider.Check{}, err
	}
	defer s.Close()
	init, err := s.initialize(ctx)
	if err != nil {
		return provider.Check{}, err
	}
	var c provider.Check
	if init.AgentInfo != nil {
		c.Name, c.Version = init.AgentInfo.Name, init.AgentInfo.Version
	}
	return c, nil
}

// spawn starts the agent's process in dir, connected but not yet introduced.
func (p *Provider) spawn(dir string, emit provider.Emit) (*session, error) {
	if len(p.Command) == 0 {
		return nil, errors.New("acp: no command configured")
	}
	cmd := exec.Command(p.Command[0], p.Command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), p.Env...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// A group of its own, so Close also reaches children a launcher such as npx spawns.
	group, err := proc.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("acp: start %s: %w", p.Command[0], err)
	}
	s := &session{cmd: cmd, group: group, stdin: stdin, emit: emit, pending: map[string]chan string{}, booleans: map[string]bool{}}
	s.conn = sdk.NewClientSideConnection(client{s}, stdin, stdout)
	return s, nil
}

func (s *session) initialize(ctx context.Context) (sdk.InitializeResponse, error) {
	init, err := s.conn.Initialize(ctx, sdk.InitializeRequest{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		// No client-side fs or terminals: the agent uses its own tools.
		ClientCapabilities: sdk.ClientCapabilities{
			// On/off session options are sent only to clients that say they show them.
			Session: &sdk.ClientSessionCapabilities{ConfigOptions: &sdk.SessionConfigOptionsCapabilities{Boolean: &sdk.BooleanConfigOptionCapabilities{}}},
		},
		ClientInfo: &sdk.Implementation{Name: "inaba", Version: "0"},
	})
	if err != nil {
		return init, fmt.Errorf("acp: initialize: %w", err)
	}
	return init, nil
}

func (s *session) start(ctx context.Context, cfg provider.Config) error {
	init, err := s.initialize(ctx)
	if err != nil {
		return err
	}
	s.images = init.AgentCapabilities.PromptCapabilities.Image
	servers := mcpServers(cfg.MCPServers, init.AgentCapabilities.McpCapabilities)

	if cfg.NativeSessionID != "" && init.AgentCapabilities.LoadSession {
		s.setReplaying(true)
		loaded, err := s.conn.LoadSession(ctx, sdk.LoadSessionRequest{SessionId: sdk.SessionId(cfg.NativeSessionID), Cwd: cfg.Cwd, McpServers: servers})
		s.setReplaying(false)
		if err == nil {
			s.id = sdk.SessionId(cfg.NativeSessionID)
			s.setup(loaded.ConfigOptions, loaded.Modes)
			return nil
		}
		var rpcErr *sdk.RequestError
		if !errors.As(err, &rpcErr) {
			return fmt.Errorf("acp: session/load: %w", err)
		}
		// The agent no longer has the session; continue in a new one.
	}
	created, err := s.conn.NewSession(ctx, sdk.NewSessionRequest{Cwd: cfg.Cwd, McpServers: servers})
	if err != nil {
		return fmt.Errorf("acp: session/new: %w", err)
	}
	s.id = created.SessionId
	s.setup(created.ConfigOptions, created.Modes)
	return nil
}

// MCPBridge is the command that serves an HTTP MCP server on stdio, given its address
// as the last argument and its headers in mcpbridge.HeadersEnv: for agents that only
// start MCP servers as commands. Without it, such agents do without HTTP servers.
var MCPBridge []string

// mcpServers says the MCP servers in ACP's terms. One reached over HTTP goes through
// MCPBridge to an agent that cannot reach one that way.
func mcpServers(servers []provider.MCPServer, can sdk.McpCapabilities) []sdk.McpServer {
	out := []sdk.McpServer{}
	for _, m := range servers {
		if m.URL != "" && !can.Http {
			if len(MCPBridge) == 0 {
				slog.Warn("the agent cannot use MCP servers over HTTP; leaving one out", "server", m.Name)
				continue
			}
			headers, _ := json.Marshal(m.Headers)
			bridged := provider.MCPServer{Name: m.Name, Command: MCPBridge[0], Args: append(append([]string{}, MCPBridge[1:]...), m.URL)}
			if len(m.Headers) > 0 {
				bridged.Env = map[string]string{mcpbridge.HeadersEnv: string(headers)}
			}
			m = bridged
		}
		if m.URL != "" {
			headers := []sdk.HttpHeader{}
			for _, k := range sortedKeys(m.Headers) {
				headers = append(headers, sdk.HttpHeader{Name: k, Value: m.Headers[k]})
			}
			out = append(out, sdk.McpServer{Http: &sdk.McpServerHttpInline{Type: "http", Name: m.Name, Url: m.URL, Headers: headers}})
			continue
		}
		env := []sdk.EnvVariable{}
		for _, k := range sortedKeys(m.Env) {
			env = append(env, sdk.EnvVariable{Name: k, Value: m.Env[k]})
		}
		out = append(out, sdk.McpServer{Stdio: &sdk.McpServerStdio{Name: m.Name, Command: m.Command, Args: append([]string{}, m.Args...), Env: env}})
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s *session) setReplaying(on bool) {
	s.mu.Lock()
	s.replaying = on
	s.mu.Unlock()
}

func (s *session) NativeSessionID() string { return string(s.id) }

func (s *session) Prompt(ctx context.Context, p provider.Prompt) (string, error) {
	if len(p.Images) > 0 && !s.images {
		return "", &provider.TurnError{Err: errors.New("this agent does not take images")}
	}
	blocks := []sdk.ContentBlock{sdk.TextBlock(p.Text)}
	for _, img := range p.Images {
		blocks = append(blocks, sdk.ImageBlock(base64.StdEncoding.EncodeToString(img.Data), img.MimeType))
	}
	// Every agent must take links to files; it reads them with its own tools.
	for _, f := range p.Files {
		blocks = append(blocks, sdk.ResourceLinkBlock(filepath.Base(f), fileURI(f)))
	}
	res, err := s.conn.Prompt(ctx, sdk.PromptRequest{SessionId: s.id, Prompt: blocks})
	s.cancelPending()
	s.mu.Lock()
	s.turnUsage = turnUsage(res.Usage)
	s.mu.Unlock()
	var rpcErr *sdk.RequestError
	if errors.As(err, &rpcErr) {
		return "", &provider.TurnError{Err: err}
	}
	return string(res.StopReason), err
}

func (s *session) LastTurnUsage() *inabav1.TurnUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnUsage
}

// turnUsage converts the token counts a PromptResponse may carry. The field is unstable
// in ACP; its doc says it is this turn's usage, which is how inaba sums it.
func turnUsage(u *sdk.Usage) *inabav1.TurnUsage {
	if u == nil {
		return nil
	}
	n := func(v int64) uint64 { return uint64(max(0, v)) }
	p := func(v *int64) uint64 {
		if v == nil {
			return 0
		}
		return n(*v)
	}
	return &inabav1.TurnUsage{InputTokens: n(u.InputTokens), OutputTokens: n(u.OutputTokens), TotalTokens: n(u.TotalTokens),
		CachedReadTokens: p(u.CachedReadTokens), CachedWriteTokens: p(u.CachedWriteTokens), ThoughtTokens: p(u.ThoughtTokens)}
}

func (s *session) Interrupt(ctx context.Context) error {
	// ACP requires pending permission requests to be answered as cancelled.
	s.cancelPending()
	return s.conn.Cancel(ctx, sdk.CancelNotification{SessionId: s.id})
}

func (s *session) cancelPending() {
	s.mu.Lock()
	pending := s.pending
	s.pending = map[string]chan string{}
	s.mu.Unlock()
	for _, ch := range pending {
		ch <- ""
	}
}

func (s *session) Respond(_ context.Context, requestID, optionID string) error {
	s.mu.Lock()
	ch := s.pending[requestID]
	delete(s.pending, requestID)
	s.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("acp: no pending request %q", requestID)
	}
	ch <- optionID
	return nil
}

// Raw sends one of the agent's extension methods, whose names start with "_". The
// protocol's own methods are what inaba's calls already send.
func (s *session) Raw(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if !strings.HasPrefix(method, "_") {
		return nil, fmt.Errorf("acp: only extension methods, named with a leading _, pass through; %s is ACP's own", method)
	}
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	return s.conn.CallExtension(ctx, method, params)
}

func (s *session) Close() error {
	s.group.Ask(s.stdin)
	defer time.AfterFunc(3*time.Second, s.group.Kill).Stop()
	_ = s.cmd.Wait()
	s.group.Release()
	return nil
}

// fileURI names a file on this machine, C:\x as file:///C:/x.
func fileURI(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// describesSession tells updates about the session from ones that replay its history.
func describesSession(u sdk.SessionUpdate) bool {
	return u.ConfigOptionUpdate != nil || u.CurrentModeUpdate != nil || u.AvailableCommandsUpdate != nil || u.UsageUpdate != nil
}

func (s *session) onUpdate(u sdk.SessionUpdate) {
	s.mu.Lock()
	replaying := s.replaying
	s.mu.Unlock()
	// A replay repeats the conversation the event log already holds; what the session
	// offers and how full it is are news all the same.
	if replaying && !describesSession(u) {
		return
	}
	text := func(kind inabav1.TextDelta_Kind, c sdk.ContentBlock) {
		if c.Text != nil {
			s.emit(&inabav1.Event{Payload: &inabav1.Event_TextDelta{TextDelta: &inabav1.TextDelta{Kind: kind, Text: c.Text.Text}}})
		}
	}
	switch {
	case u.AgentMessageChunk != nil:
		text(inabav1.TextDelta_KIND_MESSAGE, u.AgentMessageChunk.Content)
	case u.AgentThoughtChunk != nil:
		text(inabav1.TextDelta_KIND_THOUGHT, u.AgentThoughtChunk.Content)
	case u.ToolCall != nil:
		t := u.ToolCall
		s.emit(&inabav1.Event{Payload: &inabav1.Event_ToolCall{ToolCall: &inabav1.ToolCall{
			Id: string(t.ToolCallId), Title: t.Title, Kind: string(t.Kind), Status: string(t.Status), Raw: rawValue(u),
			Input: toolInput(t.RawInput), Output: toolOutput(t.Content, t.RawOutput),
		}}})
	case u.ToolCallUpdate != nil:
		t := u.ToolCallUpdate
		title, _ := t.Title.Value()
		kind, _ := t.Kind.Value()
		status, _ := t.Status.Value()
		input, _ := t.RawInput.Value()
		content, _ := t.Content.Value()
		output, _ := t.RawOutput.Value()
		s.emit(&inabav1.Event{Payload: &inabav1.Event_ToolCall{ToolCall: &inabav1.ToolCall{
			Id: string(t.ToolCallId), Title: title, Kind: string(kind), Status: string(status), Raw: rawValue(u),
			Input: toolInput(input), Output: toolOutput(content, output),
		}}})
	case u.Plan != nil:
		plan := &inabav1.Plan{}
		for _, e := range u.Plan.Entries {
			plan.Entries = append(plan.Entries, &inabav1.Plan_Entry{Content: e.Content, Priority: string(e.Priority), Status: string(e.Status)})
		}
		s.emit(&inabav1.Event{Payload: &inabav1.Event_Plan{Plan: plan}})
	case u.AvailableCommandsUpdate != nil:
		ev := &inabav1.CommandsChanged{Commands: []*inabav1.Command{}}
		for _, c := range u.AvailableCommandsUpdate.AvailableCommands {
			cmd := &inabav1.Command{Name: c.Name, Description: c.Description}
			if c.Input != nil && c.Input.Unstructured != nil {
				cmd.Hint = c.Input.Unstructured.Hint
			}
			ev.Commands = append(ev.Commands, cmd)
		}
		s.emit(&inabav1.Event{Payload: &inabav1.Event_CommandsChanged{CommandsChanged: ev}})
	case u.UsageUpdate != nil:
		usage := &inabav1.Usage{Used: uint64(max(0, u.UsageUpdate.Used)), Size: uint64(max(0, u.UsageUpdate.Size))}
		if c := u.UsageUpdate.Cost; c != nil {
			usage.Cost, usage.Currency = c.Amount, c.Currency
		}
		s.emit(&inabav1.Event{Payload: &inabav1.Event_UsageChanged{UsageChanged: &inabav1.UsageChanged{Usage: usage}}})
	case u.ConfigOptionUpdate != nil:
		s.reportConfig(u.ConfigOptionUpdate.ConfigOptions)
	case u.CurrentModeUpdate != nil:
		s.reportMode(string(u.CurrentModeUpdate.CurrentModeId))
	case u.SessionInfoUpdate != nil:
		// The agent's name for the session; what else it says of itself is not news here.
		if title, ok := u.SessionInfoUpdate.Title.Value(); ok && strings.TrimSpace(title) != "" {
			s.emit(&inabav1.Event{Payload: &inabav1.Event_ThreadTitle{ThreadTitle: &inabav1.ThreadTitle{Title: title}}})
		}
	default:
		// Anything inaba has no event for still reaches clients.
		s.emit(&inabav1.Event{Payload: &inabav1.Event_Raw{Raw: &inabav1.RawEvent{Method: "session/update", Params: rawValue(map[string]any{"sessionId": s.id, "update": u})}}})
	}
}

// rawValue is a value of the agent's as the JSON events carry.
func rawValue(v any) *structpb.Value {
	data, err := json.Marshal(v)
	if err != nil {
		return structpb.NewNullValue()
	}
	return provider.JSONValue(data)
}

func (s *session) onPermission(ctx context.Context, req sdk.RequestPermissionRequest) sdk.RequestPermissionResponse {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	ch := make(chan string, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()

	title, _ := req.ToolCall.Title.Value()
	ev := &inabav1.PermissionRequest{RequestId: id, Title: title, Raw: rawValue(req)}
	for _, o := range req.Options {
		ev.Options = append(ev.Options, &inabav1.PermissionRequest_Option{Id: string(o.OptionId), Name: o.Name, Kind: string(o.Kind)})
	}
	s.emit(&inabav1.Event{Payload: &inabav1.Event_PermissionRequest{PermissionRequest: ev}})

	var option string
	select {
	case option = <-ch:
	case <-ctx.Done():
	}
	s.emit(&inabav1.Event{Payload: &inabav1.Event_PermissionResolved{PermissionResolved: &inabav1.PermissionResolved{RequestId: id, OptionId: option}}})
	if option == "" || !slices.ContainsFunc(req.Options, func(o sdk.PermissionOption) bool { return string(o.OptionId) == option }) {
		return sdk.RequestPermissionResponse{Outcome: sdk.NewRequestPermissionOutcomeCancelled()}
	}
	return sdk.RequestPermissionResponse{Outcome: sdk.NewRequestPermissionOutcomeSelected(sdk.PermissionOptionId(option))}
}

// client is what the agent may call on inaba: session updates and permission requests.
// inaba offers no file system or terminals of its own; the agent uses its own tools.
type client struct{ s *session }

func (c client) SessionUpdate(_ context.Context, n sdk.SessionNotification) error {
	c.s.onUpdate(n.Update)
	return nil
}

func (c client) RequestPermission(ctx context.Context, req sdk.RequestPermissionRequest) (sdk.RequestPermissionResponse, error) {
	return c.s.onPermission(ctx, req), nil
}

func (client) CompleteElicitation(context.Context, sdk.CompleteElicitationNotification) error {
	return nil
}
func (client) CreateElicitation(context.Context, sdk.CreateElicitationRequest) (sdk.CreateElicitationResponse, error) {
	return sdk.CreateElicitationResponse{}, sdk.NewMethodNotFound("elicitation/create")
}
func (client) ReadTextFile(context.Context, sdk.ReadTextFileRequest) (sdk.ReadTextFileResponse, error) {
	return sdk.ReadTextFileResponse{}, sdk.NewMethodNotFound("fs/read_text_file")
}
func (client) WriteTextFile(context.Context, sdk.WriteTextFileRequest) (sdk.WriteTextFileResponse, error) {
	return sdk.WriteTextFileResponse{}, sdk.NewMethodNotFound("fs/write_text_file")
}
func (client) CreateTerminal(context.Context, sdk.CreateTerminalRequest) (sdk.CreateTerminalResponse, error) {
	return sdk.CreateTerminalResponse{}, sdk.NewMethodNotFound("terminal/create")
}
func (client) KillTerminal(context.Context, sdk.KillTerminalRequest) (sdk.KillTerminalResponse, error) {
	return sdk.KillTerminalResponse{}, sdk.NewMethodNotFound("terminal/kill")
}
func (client) TerminalOutput(context.Context, sdk.TerminalOutputRequest) (sdk.TerminalOutputResponse, error) {
	return sdk.TerminalOutputResponse{}, sdk.NewMethodNotFound("terminal/output")
}
func (client) ReleaseTerminal(context.Context, sdk.ReleaseTerminalRequest) (sdk.ReleaseTerminalResponse, error) {
	return sdk.ReleaseTerminalResponse{}, sdk.NewMethodNotFound("terminal/release")
}
func (client) WaitForTerminalExit(context.Context, sdk.WaitForTerminalExitRequest) (sdk.WaitForTerminalExitResponse, error) {
	return sdk.WaitForTerminalExitResponse{}, sdk.NewMethodNotFound("terminal/wait_for_exit")
}

// maxToolOutput bounds what one tool call keeps in the event log.
const maxToolOutput = 64 << 10

// toolInput is a tool's input for a person: a command's command line, or the JSON.
func toolInput(raw json.RawMessage) string {
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" || s == "{}" {
		return ""
	}
	var cmd struct {
		Command any `json:"command"`
	}
	if json.Unmarshal(raw, &cmd) == nil {
		switch c := cmd.Command.(type) {
		case string:
			return c
		case []any:
			parts := make([]string, len(c))
			for i, p := range c {
				parts[i] = fmt.Sprint(p)
			}
			return strings.Join(parts, " ")
		}
	}
	return clip(compactJSON(raw))
}

// toolOutput is the text a tool gave back: its text content, or failing that its raw
// output, a string as it is and anything else as JSON.
func toolOutput(content []sdk.ToolCallContent, raw json.RawMessage) string {
	var b strings.Builder
	for _, c := range content {
		if c.Content != nil && c.Content.Content.Text != nil {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(c.Content.Content.Text.Text)
		}
	}
	if b.Len() == 0 && len(raw) > 0 && string(raw) != "null" {
		var str string
		if json.Unmarshal(raw, &str) == nil {
			b.WriteString(str)
		} else {
			b.WriteString(compactJSON(raw))
		}
	}
	return clip(unfence(b.String()))
}

// unfence takes a whole-text Markdown code block, as agents send a command's output,
// down to its contents.
func unfence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") || !strings.HasSuffix(t, "```") || len(t) < 6 {
		return s
	}
	first := strings.IndexByte(t, '\n')
	if first < 0 {
		return s
	}
	return strings.TrimSuffix(strings.TrimSuffix(t[first+1:], "```"), "\n")
}

func compactJSON(raw json.RawMessage) string {
	var out bytes.Buffer
	if json.Indent(&out, raw, "", "  ") != nil {
		return string(raw)
	}
	return out.String()
}

// clip cuts text at maxToolOutput without splitting a character.
func clip(s string) string {
	if len(s) <= maxToolOutput {
		return s
	}
	cut := maxToolOutput
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…"
}
