package mcpbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type addIn struct {
	A int `json:"a"`
	B int `json:"b"`
}
type addOut struct {
	Sum int `json:"sum"`
}

func TestBridgeRelaysToolsWithHeaders(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "add", Description: "adds"}, func(_ context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
		return nil, addOut{Sum: in.A + in.B}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The header is how the upstream knows the caller; the bridge must carry it.
		if r.Header.Get("Authorization") != "Bearer t" {
			http.Error(w, "who are you", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	agentSide, bridgeSide := mcp.NewInMemoryTransports()
	go Run(ctx, upstream.URL, map[string]string{"Authorization": "Bearer t"}, bridgeSide)

	client, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, nil).Connect(ctx, agentSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "add" || tools.Tools[0].InputSchema == nil {
		t.Fatalf("tools through the bridge: %v %v", tools, err)
	}
	res, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "add", Arguments: map[string]any{"a": 2, "b": 3}})
	if err != nil || res.IsError {
		t.Fatalf("call through the bridge: %v %v", res, err)
	}
	if got := res.StructuredContent.(map[string]any)["sum"]; got != float64(5) {
		t.Fatalf("sum = %v", got)
	}

	// Without the header the upstream refuses, and the bridge says so instead of serving nothing.
	if err := Run(ctx, upstream.URL, nil, &mcp.InMemoryTransport{}); err == nil {
		t.Fatal("the bridge started without reaching its upstream")
	}
}
