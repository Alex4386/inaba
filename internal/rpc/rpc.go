// Package rpc mounts a connect server the way both inaba binaries serve it:
// Connect, gRPC and gRPC-Web by procedure name, plus the REST paths from the protos.
package rpc

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/vanguard"
)

// Mount serves the server's procedures by name and at their REST paths from the protos,
// and the server streams given at their own GET paths, which the protos cannot hold.
func Mount(mux *http.ServeMux, server *connect.Server, streams ...Stream) error {
	connecthttp.Mount(mux, server)
	for _, s := range streams {
		mux.Handle(s.Pattern, serveStream(server, s))
	}
	return vanguard.Mount(mux, server)
}

// PermissionsHeader is how inaba-server tells a host what the caller may do: a
// comma-separated list of permission names. The server sets it on every relayed
// request, replacing whatever the caller sent; the host enforces it.
const PermissionsHeader = "X-Inaba-Permissions"

// UserHeader is how inaba-server tells a host which account is calling: its id, or empty
// for a call with no account behind it, such as a webhook. Like PermissionsHeader, the
// server sets it on every relayed request, replacing whatever the caller sent.
const UserHeader = "X-Inaba-User"

// Permissions a host enforces, by the names inaba-server sends in PermissionsHeader.
const (
	// PermProjects lets its holder add, change and remove a host's projects.
	PermProjects = "projects"
	// PermThreadsAnywhere lets its holder start threads in any folder, outside the projects.
	PermThreadsAnywhere = "threads-anywhere"
	// PermAutomations lets its holder set up automations and their notifications.
	PermAutomations = "automations"
	// PermHostSettings lets its holder change a host's settings: its providers, modules,
	// MCP servers, browser and archive policy.
	PermHostSettings = "host-settings"
	// PermHousekeeping lets its holder clear what inaba leaves on a host's disk.
	PermHousekeeping = "housekeeping"
	// PermRestart lets its holder restart a host. The server sends it to the host's
	// owner and administrators, and on a shared host to those who may add shared
	// hosts; it is not one an administrator grants.
	PermRestart = "restart"
	// PermHosts is the old name for host settings and housekeeping together, sent too
	// so hosts that predate the split keep working.
	PermHosts = "hosts"
)

type trustedKey struct{}

// Trusted marks a context as a call from the machine's own administrator: a local
// control socket, or code in the same process. Such calls skip permission checks.
func Trusted(ctx context.Context) context.Context {
	return context.WithValue(ctx, trustedKey{}, true)
}

// May fails unless the call is trusted or inaba-server said its caller holds the
// permission, in PermissionsHeader.
func May(ctx context.Context, permission string) error {
	if ctx.Value(trustedKey{}) != nil {
		return nil
	}
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		if slices.Contains(strings.Split(info.RequestHeader().Get(PermissionsHeader), ","), permission) {
			return nil
		}
	}
	return connect.NewError(connect.CodePermissionDenied, "an administrator has to allow your account to manage "+permission)
}

// Caller returns the calling account's id, as inaba-server said in UserHeader, and whether
// the call is trusted, in which case it acts for every account.
func Caller(ctx context.Context) (user string, trusted bool) {
	if ctx.Value(trustedKey{}) != nil {
		return "", true
	}
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		return info.RequestHeader().Get(UserHeader), false
	}
	return "", false
}
