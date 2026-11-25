// Package mcpbridge serves an MCP server reached over Streamable HTTP as one on stdio,
// for an agent that can only start MCP servers as commands.
package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HeadersEnv carries the HTTP headers to send, as a JSON object, so tokens in them stay
// out of the command line where other users of the machine could read them.
const HeadersEnv = "INABA_MCP_HEADERS"

// Run relays the tools of the MCP server at url to the server transport, usually
// stdio, until either side closes.
// shortcut: tools only; relay resources and prompts too if an agent needs them over the bridge.
func Run(ctx context.Context, url string, headers map[string]string, server mcp.Transport) error {
	httpClient := &http.Client{Transport: withHeaders{headers, http.DefaultTransport}}
	upstream, err := mcp.NewClient(&mcp.Implementation{Name: "inaba-mcp-bridge", Version: "1"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: httpClient}, nil)
	if err != nil {
		return fmt.Errorf("could not reach the MCP server at %s: %w", url, err)
	}
	defer upstream.Close()
	tools, err := upstream.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("could not list the MCP server's tools: %w", err)
	}
	relay := mcp.NewServer(&mcp.Implementation{Name: "inaba-mcp-bridge", Version: "1"}, nil)
	for _, tool := range tools.Tools {
		relay.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args any
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					return nil, err
				}
			}
			return upstream.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: args})
		})
	}
	return relay.Run(ctx, server)
}

// HeadersFromEnv reads the headers Run is given from HeadersEnv.
func HeadersFromEnv() (map[string]string, error) {
	headers := map[string]string{}
	if raw := os.Getenv(HeadersEnv); raw != "" {
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			return nil, fmt.Errorf("%s is not a JSON object of strings: %w", HeadersEnv, err)
		}
	}
	return headers, nil
}

type withHeaders struct {
	headers map[string]string
	next    http.RoundTripper
}

func (w withHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range w.headers {
		r.Header.Set(k, v)
	}
	return w.next.RoundTrip(r)
}
