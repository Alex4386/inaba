// Package control is the local socket a running inaba daemon listens on, so its own
// CLI can talk to it. Whoever can open the socket file is trusted as an administrator.
package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
)

// Listen creates the socket for a daemon, replacing one a dead daemon left behind.
func Listen(path string) (net.Listener, error) {
	if Dial(path) != nil {
		return nil, fmt.Errorf("another instance is already listening on %s", path)
	}
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control socket %s: %w", path, err)
	}
	// The socket has no sign-in of its own: only its owner may use it. On Windows the folder
	// it is in says who that is, by default one only the user may enter.
	if runtime.GOOS == "windows" {
		return l, nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Serve answers RPCs on the socket until it is closed.
func Serve(l net.Listener, server *connect.Server) error {
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	return http.Serve(l, mux)
}

// ServeHandler is Serve for a handler that already routes the RPCs, such as one
// whose services change while it runs.
func ServeHandler(l net.Listener, handler http.Handler) error {
	return http.Serve(l, handler)
}

// Dial returns a transport to the daemon listening on the socket, or nil when none is.
func Dial(path string) connect.Transport {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return nil
	}
	conn.Close()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
	// The host name is a placeholder; every connection goes to the socket.
	return connecthttp.NewTransport(client, "http://inaba")
}

// Plain turns an RPC error into the message its handler wrote, for printing to a person.
func Plain(err error) error {
	var rpcErr *connect.Error
	if errors.As(err, &rpcErr) && rpcErr.Message() != "" {
		return errors.New(rpcErr.Message())
	}
	return err
}
