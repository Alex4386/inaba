package acp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/coder/acp-go-sdk"
)

// fakeAgent is the agent the tests drive: the test binary, run with INABA_FAKE_ACP=1,
// speaking ACP through the SDK's agent side. What it does depends on the prompt.
type fakeAgentImpl struct {
	conn      *sdk.AgentSideConnection
	cancelled chan struct{}
	mu        sync.Mutex
	// The MCP servers the last session was given, as name=command or name=url.
	mcp []string
	// The instructions the client added to the session's, if any.
	system string
}

func fakeAgent() {
	a := &fakeAgentImpl{cancelled: make(chan struct{}, 1)}
	a.conn = sdk.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	<-a.conn.Done()
}

func (a *fakeAgentImpl) update(u sdk.SessionUpdate) {
	_ = a.conn.SessionUpdate(context.Background(), sdk.SessionNotification{SessionId: "s1", Update: u})
}

func fakeOptions(model string, fast bool) []sdk.SessionConfigOption {
	cat := sdk.SessionConfigOptionCategoryModel
	groups := sdk.SessionConfigSelectOptionsGrouped{
		{Group: "big", Name: "Big", Options: []sdk.SessionConfigSelectOption{{Value: "opus", Name: "Opus"}}},
		{Group: "small", Name: "Small", Options: []sdk.SessionConfigSelectOption{{Value: "haiku", Name: "Haiku"}}},
	}
	return []sdk.SessionConfigOption{
		{Select: &sdk.SessionConfigOptionSelect{Id: "model", Name: "Model", Category: &cat, Type: "select", CurrentValue: sdk.SessionConfigValueId(model), Options: sdk.SessionConfigSelectOptions{Grouped: &groups}}},
		{Boolean: &sdk.SessionConfigOptionBoolean{Id: "fast", Name: "Fast", Type: "boolean", CurrentValue: fast}},
	}
}

func (a *fakeAgentImpl) Initialize(context.Context, sdk.InitializeRequest) (sdk.InitializeResponse, error) {
	return sdk.InitializeResponse{
		ProtocolVersion:   sdk.ProtocolVersionNumber,
		AgentCapabilities: sdk.AgentCapabilities{LoadSession: true, PromptCapabilities: sdk.PromptCapabilities{Image: true}, McpCapabilities: sdk.McpCapabilities{Http: true}, SessionCapabilities: sdk.SessionCapabilities{SystemPrompt: &sdk.SessionSystemPromptCapabilities{}}},
		AuthMethods:       []sdk.AuthMethod{},
		AgentInfo:         &sdk.Implementation{Name: "fake-agent", Version: "1.2.3"},
	}, nil
}

func (a *fakeAgentImpl) NewSession(_ context.Context, req sdk.NewSessionRequest) (sdk.NewSessionResponse, error) {
	var got []string
	for _, m := range req.McpServers {
		switch {
		case m.Stdio != nil:
			entry := m.Stdio.Name + "=" + m.Stdio.Command + " " + strings.Join(m.Stdio.Args, " ")
			for _, e := range m.Stdio.Env {
				entry += " " + e.Name + "=" + e.Value
			}
			got = append(got, entry)
		case m.Http != nil:
			got = append(got, m.Http.Name+"="+m.Http.Url)
		}
	}
	a.mu.Lock()
	a.mcp = got
	a.system = ""
	if p := req.SystemPrompt; p != nil && p.Mode == sdk.SystemPromptModeAppend && len(p.Content) > 0 && p.Content[0].Text != nil {
		a.system = p.Content[0].Text.Text
	}
	a.mu.Unlock()
	// Commands come after the session exists, as a notification.
	go func() {
		time.Sleep(10 * time.Millisecond)
		a.update(sdk.SessionUpdate{AvailableCommandsUpdate: &sdk.SessionAvailableCommandsUpdate{SessionUpdate: "available_commands_update", AvailableCommands: []sdk.AvailableCommand{
			{Name: "review", Description: "Review the branch", Input: &sdk.AvailableCommandInput{Unstructured: &sdk.UnstructuredCommandInput{Hint: "what to look at"}}},
			{Name: "compact", Description: "Shorten the history"},
		}}})
	}()
	return sdk.NewSessionResponse{SessionId: "s1", ConfigOptions: fakeOptions("opus", false)}, nil
}

func (a *fakeAgentImpl) LoadSession(_ context.Context, req sdk.LoadSessionRequest) (sdk.LoadSessionResponse, error) {
	a.update(sdk.UpdateAgentMessageText("old history"))
	return sdk.LoadSessionResponse{}, nil
}

func (a *fakeAgentImpl) SetSessionConfigOption(_ context.Context, req sdk.SetSessionConfigOptionRequest) (sdk.SetSessionConfigOptionResponse, error) {
	if req.Boolean != nil {
		return sdk.SetSessionConfigOptionResponse{ConfigOptions: fakeOptions("opus", req.Boolean.Value)}, nil
	}
	return sdk.SetSessionConfigOptionResponse{ConfigOptions: fakeOptions(string(req.ValueId.Value), false)}, nil
}

func (a *fakeAgentImpl) Cancel(context.Context, sdk.CancelNotification) error {
	select {
	case a.cancelled <- struct{}{}:
	default:
	}
	return nil
}

func (a *fakeAgentImpl) HandleExtensionMethod(_ context.Context, method string, params json.RawMessage) (any, error) {
	if method == "_inaba/echo" {
		return params, nil
	}
	return nil, sdk.NewMethodNotFound(method)
}

func (a *fakeAgentImpl) Prompt(ctx context.Context, req sdk.PromptRequest) (sdk.PromptResponse, error) {
	first := ""
	if len(req.Prompt) > 0 && req.Prompt[0].Text != nil {
		first = req.Prompt[0].Text.Text
	}
	done := sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}
	switch first {
	case "edit":
		// Stands in for an agent changing files; it runs in the session's folder.
		_ = os.WriteFile("written-by-agent.txt", []byte("the agent was here\n"), 0o644)
		a.update(sdk.UpdateAgentMessageText("Wrote a file."))
		return done, nil
	case "hang":
		<-a.cancelled
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
	case "system":
		a.mu.Lock()
		a.update(sdk.UpdateAgentMessageText("system: " + a.system))
		a.mu.Unlock()
		return done, nil
	case "mcp":
		a.mu.Lock()
		a.update(sdk.UpdateAgentMessageText("mcp: " + strings.Join(a.mcp, "; ")))
		a.mu.Unlock()
		return done, nil
	case "plan":
		step := func(first, second sdk.PlanEntryStatus) {
			a.update(sdk.UpdatePlan(
				sdk.PlanEntry{Content: "Read the code", Priority: sdk.PlanEntryPriorityHigh, Status: first},
				sdk.PlanEntry{Content: "Write the fix", Priority: sdk.PlanEntryPriorityMedium, Status: second},
			))
		}
		step(sdk.PlanEntryStatusInProgress, sdk.PlanEntryStatusPending)
		time.Sleep(300 * time.Millisecond)
		step(sdk.PlanEntryStatusCompleted, sdk.PlanEntryStatusInProgress)
		time.Sleep(300 * time.Millisecond)
		step(sdk.PlanEntryStatusCompleted, sdk.PlanEntryStatusCompleted)
		a.update(sdk.UpdateAgentMessageText("Planned and done."))
		cached := int64(7)
		done.Usage = &sdk.Usage{InputTokens: 120, OutputTokens: 30, TotalTokens: 157, CachedReadTokens: &cached}
		return done, nil
	case "look":
		// Says what it got besides the text, and how full its context is.
		var got []string
		for _, b := range req.Prompt[1:] {
			switch {
			case b.Image != nil:
				got = append(got, "image:"+b.Image.MimeType+b.Image.Data)
			case b.ResourceLink != nil:
				got = append(got, "resource_link:"+b.ResourceLink.Uri)
			}
		}
		a.update(sdk.SessionUpdate{UsageUpdate: &sdk.SessionUsageUpdate{SessionUpdate: "usage_update", Used: 1200, Size: 200000, Cost: &sdk.Cost{Amount: 0.25, Currency: "USD"}}})
		a.update(sdk.UpdateAgentMessageText(strings.Join(got, " ")))
		return done, nil
	}
	a.update(sdk.UpdateAgentMessageText("hello"))
	a.update(sdk.StartToolCall("t1", "Run ls", sdk.WithStartKind(sdk.ToolKindExecute), sdk.WithStartStatus(sdk.ToolCallStatusPending)))
	perm, err := a.conn.RequestPermission(ctx, sdk.RequestPermissionRequest{
		SessionId: "s1",
		ToolCall:  sdk.ToolCallUpdate{ToolCallId: "t1", Title: sdk.PatchSet("Run ls")},
		Options:   []sdk.PermissionOption{{OptionId: "yes", Name: "Allow", Kind: sdk.PermissionOptionKindAllowOnce}},
	})
	if err != nil {
		return sdk.PromptResponse{}, err
	}
	answer := "cancelled:"
	if perm.Outcome.Selected != nil {
		answer = "selected:" + string(perm.Outcome.Selected.OptionId)
	}
	a.update(sdk.UpdateAgentMessageText(answer))
	// An update inaba has no event of its own for.
	a.update(sdk.UpdateUserMessageText("echoed back"))
	return done, nil
}

func (a *fakeAgentImpl) Authenticate(context.Context, sdk.AuthenticateRequest) (sdk.AuthenticateResponse, error) {
	return sdk.AuthenticateResponse{}, nil
}
func (a *fakeAgentImpl) Logout(context.Context, sdk.LogoutRequest) (sdk.LogoutResponse, error) {
	return sdk.LogoutResponse{}, nil
}
func (a *fakeAgentImpl) CloseSession(context.Context, sdk.CloseSessionRequest) (sdk.CloseSessionResponse, error) {
	return sdk.CloseSessionResponse{}, nil
}
func (a *fakeAgentImpl) DeleteSession(context.Context, sdk.DeleteSessionRequest) (sdk.DeleteSessionResponse, error) {
	return sdk.DeleteSessionResponse{}, nil
}
func (a *fakeAgentImpl) ListSessions(context.Context, sdk.ListSessionsRequest) (sdk.ListSessionsResponse, error) {
	return sdk.ListSessionsResponse{}, nil
}
func (a *fakeAgentImpl) ResumeSession(context.Context, sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	return sdk.ResumeSessionResponse{}, sdk.NewMethodNotFound("session/resume")
}
func (a *fakeAgentImpl) SetSessionMode(context.Context, sdk.SetSessionModeRequest) (sdk.SetSessionModeResponse, error) {
	return sdk.SetSessionModeResponse{}, sdk.NewMethodNotFound("session/set_mode")
}
