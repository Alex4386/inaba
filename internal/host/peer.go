package host

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v4"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/rpc"
)

// Browsers cap a data channel message well below what pion accepts; stay under it.
const maxChannelMessage = 16 << 10

// How much one channel may have queued and unsent. Every channel shares one SCTP
// queue, so a flood on one (a terminal, a screencast) must not get far ahead of the rest.
const maxChannelBuffered = 256 << 10

// peers answers WebRTC offers. A browser opens one data channel per API call and
// speaks plain HTTP on it, so a direct connection serves the very same handler the
// relay does.
type peers struct {
	inabav1connect.UnimplementedPeerServiceHandler
	handler http.Handler
	api     *webrtc.API
}

func newPeers(handler http.Handler) *peers {
	var se webrtc.SettingEngine
	se.DetachDataChannels()
	return &peers{handler: handler, api: webrtc.NewAPI(webrtc.WithSettingEngine(se))}
}

func (p *peers) Connect(ctx context.Context, req *inabav1.ConnectRequest) (*inabav1.ConnectResponse, error) {
	var cfg webrtc.Configuration
	for _, u := range req.IceUrls {
		// STUN only: a TURN relay would carry traffic the server relay already can.
		if !strings.HasPrefix(u, "stun:") && !strings.HasPrefix(u, "stuns:") {
			return nil, connect.NewError(connect.CodeInvalidArgument, "only stun: URLs are accepted")
		}
		cfg.ICEServers = append(cfg.ICEServers, webrtc.ICEServer{URLs: []string{u}})
	}
	pc, err := p.api.NewPeerConnection(cfg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "could not create the connection").WithCause(err)
	}
	// The offer came through the server, which vouched for the caller and their permissions.
	// Requests on this connection skip the server, so they are held to the same ones.
	// shortcut: a permission change reaches a direct connection only when it reconnects.
	var permissions, user string
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		permissions = info.RequestHeader().Get(rpc.PermissionsHeader)
		user = info.RequestHeader().Get(rpc.UserHeader)
	}
	conns := make(chanListener, 16)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The alert stream is inaba-server's own line to the host, refused to browsers here as on the relay.
		if strings.Contains(r.URL.Path, inabav1connect.AlertServiceName) {
			http.NotFound(w, r)
			return
		}
		r.Header.Set(rpc.PermissionsHeader, permissions)
		r.Header.Set(rpc.UserHeader, user)
		p.handler.ServeHTTP(w, r)
	})}
	go server.Serve(conns)
	closeAll := func() {
		_ = pc.Close()
		_ = server.Close()
	}
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			closeAll()
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() {
			raw, err := dc.DetachWithDeadline()
			if err != nil {
				return
			}
			select {
			case conns <- newChannelConn(raw):
			default:
				raw.Close()
			}
		})
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: req.OfferSdp}); err != nil {
		closeAll()
		return nil, connect.NewError(connect.CodeInvalidArgument, "the offer is not valid SDP")
	}
	answer, err := pc.CreateAnswer(nil)
	if err == nil {
		gathered := webrtc.GatheringCompletePromise(pc)
		if err = pc.SetLocalDescription(answer); err == nil {
			select {
			case <-gathered:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
	}
	if err != nil {
		closeAll()
		return nil, connect.NewError(connect.CodeInternal, "could not answer the offer").WithCause(err)
	}
	// An offer nobody follows up on must not hold a connection open forever.
	time.AfterFunc(30*time.Second, func() {
		if pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
			closeAll()
		}
	})
	return &inabav1.ConnectResponse{AnswerSdp: pc.LocalDescription().SDP}, nil
}

// chanListener hands data channels to an http.Server as connections.
type chanListener chan net.Conn

func (l chanListener) Accept() (net.Conn, error) {
	c, ok := <-l
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l chanListener) Close() error   { return nil }
func (l chanListener) Addr() net.Addr { return channelAddr{} }

type channelAddr struct{}

func (channelAddr) Network() string { return "webrtc" }
func (channelAddr) String() string  { return "datachannel" }

// channelConn makes a message-based data channel look like a byte stream.
type channelConn struct {
	rwc  datachannel.ReadWriteCloserDeadliner
	rest []byte
	// Set when rwc can say how much it has queued; Write then waits for the reader.
	dc        *datachannel.DataChannel
	low       chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newChannelConn(raw datachannel.ReadWriteCloserDeadliner) *channelConn {
	c := &channelConn{rwc: raw, low: make(chan struct{}, 1), closed: make(chan struct{})}
	if dc, ok := raw.(*datachannel.DataChannel); ok {
		c.dc = dc
		dc.SetBufferedAmountLowThreshold(maxChannelBuffered)
		dc.OnBufferedAmountLow(func() {
			select {
			case c.low <- struct{}{}:
			default:
			}
		})
	}
	return c
}

func (c *channelConn) Read(p []byte) (int, error) {
	if len(c.rest) == 0 {
		// A read must take a whole message, whatever size the caller asked for.
		buf := make([]byte, 64<<10)
		n, err := c.rwc.Read(buf)
		if err != nil {
			// The browser closed the channel: nothing will drain what a waiting Write queued.
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				c.gone()
			}
			return 0, err
		}
		c.rest = buf[:n]
	}
	n := copy(p, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

func (c *channelConn) Write(p []byte) (int, error) {
	for off := 0; off < len(p); off += maxChannelMessage {
		for c.dc != nil && c.dc.BufferedAmount() > maxChannelBuffered {
			select {
			case <-c.low:
			case <-c.closed:
				return off, net.ErrClosed
			}
		}
		if _, err := c.rwc.Write(p[off:min(off+maxChannelMessage, len(p))]); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

func (c *channelConn) Close() error {
	c.gone()
	return c.rwc.Close()
}

// gone wakes a Write waiting for the reader: the channel will not drain any more.
func (c *channelConn) gone() { c.closeOnce.Do(func() { close(c.closed) }) }

func (c *channelConn) LocalAddr() net.Addr  { return channelAddr{} }
func (c *channelConn) RemoteAddr() net.Addr { return channelAddr{} }

// net/http wakes its own pending reads by setting a past deadline, so these must work.
func (c *channelConn) SetReadDeadline(t time.Time) error  { return c.rwc.SetReadDeadline(t) }
func (c *channelConn) SetWriteDeadline(t time.Time) error { return c.rwc.SetWriteDeadline(t) }
func (c *channelConn) SetDeadline(t time.Time) error {
	_ = c.rwc.SetWriteDeadline(t)
	return c.rwc.SetReadDeadline(t)
}
