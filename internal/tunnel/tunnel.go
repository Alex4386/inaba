// Package tunnel carries HTTP from inaba-server to a host over a connection the host
// opened, so hosts need no reachable address. It is yamux streams over one WebSocket:
// the server opens a stream per HTTP connection and the host serves HTTP on them.
package tunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

const Path = "/tunnel"

// PlatformHeader tells the server what the host runs on, as GOOS/GOARCH, so the web
// UI can tell machines apart.
const PlatformHeader = "X-Inaba-Platform"

// Dial connects a host to the server. Serve HTTP on the returned listener.
func Dial(ctx context.Context, serverURL, token string) (net.Listener, error) {
	url := "ws" + strings.TrimPrefix(strings.TrimSuffix(serverURL, "/"), "http") + Path
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}, PlatformHeader: {runtime.GOOS + "/" + runtime.GOARCH}},
	})
	if err != nil {
		return nil, err
	}
	return yamux.Server(netConn(c), config())
}

// Accept upgrades a host's request. Open streams on the returned session to reach it.
// The caller's handler must not return while the session is in use.
func Accept(w http.ResponseWriter, r *http.Request) (*yamux.Session, error) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil, err
	}
	return yamux.Client(netConn(c), config())
}

func config() *yamux.Config {
	c := yamux.DefaultConfig()
	// Both binaries log connects and disconnects themselves.
	c.LogOutput = io.Discard
	return c
}

func netConn(c *websocket.Conn) net.Conn {
	// yamux frames its own messages; the default 32 KiB cap would cut large responses.
	c.SetReadLimit(-1)
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary)
}
