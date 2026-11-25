package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
)

// recordSystem asks a host that just connected what it runs on, and keeps the answer for while
// it is away: its name, system, processor, memory and disk.
func (s *Server) recordSystem(hostID string, l *link) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return l.session.Open() }}
	defer transport.CloseIdleConnections()
	client := inabav1connect.NewSystemServiceClient(connect.NewClient(connecthttp.NewTransport(&http.Client{Transport: transport}, "http://host")))
	var system []byte
	if info, err := client.GetSystemInfo(ctx, &inabav1.GetSystemInfoRequest{}); err == nil {
		system, _ = proto.Marshal(info)
	} else {
		// An older host, or one without the system service: when it was here is still worth keeping.
		slog.Info("could not ask the host what it runs on", "host", hostID, "error", err)
	}
	if err := s.store.SawHost(hostID, system); err != nil {
		slog.Warn("could not record the host's system", "host", hostID, "error", err)
	}
}
