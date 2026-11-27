package host

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/pion/webrtc/v4"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/rpc"
)

// Does what the browser does: offer, then one data channel per HTTP/1.0 request.
func TestAPIOverDataChannel(t *testing.T) {
	h := New(fakeProvider{})
	t.Cleanup(h.Close)
	handler, err := h.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(asServer(handler))
	t.Cleanup(srv.Close)
	threads := inabav1connect.NewThreadServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL)))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	th, err := threads.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	var se webrtc.SettingEngine
	se.DetachDataChannels()
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	request := func(req string) string {
		t.Helper()
		dc, err := pc.CreateDataChannel("rpc", nil)
		if err != nil {
			t.Fatal(err)
		}
		opened := make(chan struct{})
		dc.OnOpen(func() { close(opened) })
		select {
		case <-opened:
		case <-ctx.Done():
			t.Fatal("data channel did not open")
		}
		raw, err := dc.DetachWithDeadline()
		if err != nil {
			t.Fatal(err)
		}
		conn := newChannelConn(raw)
		if _, err := conn.Write([]byte(req)); err != nil {
			t.Fatal(err)
		}
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		return fmt.Sprintf("%d %s", res.StatusCode, body)
	}

	// The first channel exists before the offer, so the offer carries a data section.
	first, _ := pc.CreateDataChannel("init", nil)
	_ = first
	offer, _ := pc.CreateOffer(nil)
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	// The offer itself travels over the relay path; here that is the plain test server.
	peer := newPeers(handler)
	ans, err := peer.Connect(ctx, &inabav1.ConnectRequest{OfferSdp: pc.LocalDescription().SDP})
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans.AnswerSdp}); err != nil {
		t.Fatal(err)
	}

	// This offer arrived with no permissions, so the direct connection has none either,
	// whatever a request on it claims.
	denied := request("POST /v1/projects HTTP/1.0\r\nContent-Type: application/json\r\n" + rpc.PermissionsHeader + ": projects\r\nContent-Length: 12\r\n\r\n" + `{"cwd":"/x"}`)
	if !strings.HasPrefix(denied, "403 ") {
		t.Fatalf("project over a direct connection without the permission: %s", denied)
	}
	body := `{"text":"` + strings.Repeat("x", 40_000) + `"}`
	got := request(fmt.Sprintf("POST /v1/threads/%s/turns HTTP/1.0\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", th.Id, len(body), body))
	if got != `200 {"queuedId":""}` {
		t.Fatalf("start turn over a data channel: %s", got)
	}
	waitIdle(t, threads, th.Id)
	// A response larger than one channel message arrives whole.
	got = request(fmt.Sprintf("GET /v1/threads/%s/events HTTP/1.0\r\n\r\n", th.Id))
	if !strings.HasPrefix(got, "200 ") || !strings.Contains(got, strings.Repeat("x", 40_000)) || !strings.Contains(got, `"turnEnded"`) {
		t.Fatalf("events over a data channel: %.200s (%d bytes)", got, len(got))
	}
	// The web UI asks for protobuf and gzip, as fetch would over the relay; nothing on
	// the way strips the header, so the answer comes compressed.
	got = request("POST " + inabav1connect.ThreadServiceListThreadsProcedure + " HTTP/1.0\r\nContent-Type: application/proto\r\nAccept-Encoding: gzip\r\nContent-Length: 0\r\n\r\n")
	if !strings.HasPrefix(got, "200 \x1f\x8b") {
		t.Fatalf("ListThreads as gzipped protobuf over a data channel: %q", got)
	}
}

// A reader that falls behind slows the writer down instead of letting the queue grow,
// and a writer left waiting is let go when the channel closes.
func TestChannelConnBackpressure(t *testing.T) {
	var se webrtc.SettingEngine
	se.DetachDataChannels()
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	a, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	accepted := make(chan *channelConn, 2)
	b.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "rpc" {
			return
		}
		dc.OnOpen(func() {
			raw, err := dc.DetachWithDeadline()
			if err == nil {
				accepted <- newChannelConn(raw)
			}
		})
	})
	open := func() (*channelConn, *webrtc.DataChannel) {
		t.Helper()
		dc, err := a.CreateDataChannel("rpc", nil)
		if err != nil {
			t.Fatal(err)
		}
		opened := make(chan struct{})
		dc.OnOpen(func() { close(opened) })
		select {
		case <-opened:
		case <-ctx.Done():
			t.Fatal("data channel did not open")
		}
		select {
		case c := <-accepted:
			return c, dc
		case <-ctx.Done():
			t.Fatal("data channel was not accepted")
		}
		return nil, nil
	}
	_, _ = a.CreateDataChannel("init", nil)
	offer, _ := a.CreateOffer(nil)
	gathered := webrtc.GatheringCompletePromise(a)
	if err := a.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err := b.SetRemoteDescription(*a.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, _ := b.CreateAnswer(nil)
	gathered = webrtc.GatheringCompletePromise(b)
	if err := b.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err := a.SetRemoteDescription(*b.LocalDescription()); err != nil {
		t.Fatal(err)
	}

	// Over loopback the reader keeps up, but writes still outrun the wire: without
	// a limit, most of the write would sit queued at once.
	const size = 8 << 20
	writer, reading := open()
	raw, err := reading.DetachWithDeadline()
	if err != nil {
		t.Fatal(err)
	}
	wrote := make(chan error, 1)
	go func() {
		_, err := writer.Write(make([]byte, size))
		wrote <- err
	}()
	var most uint64
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for len(wrote) == 0 {
			most = max(most, writer.dc.BufferedAmount())
			time.Sleep(time.Millisecond)
		}
	}()
	if n, err := io.CopyN(io.Discard, newChannelConn(raw), size); err != nil || n != size {
		t.Fatalf("read %d of %d: %v", n, size, err)
	}
	<-sampled
	if err := <-wrote; err != nil {
		t.Fatalf("write: %v", err)
	}
	if most > maxChannelBuffered+maxChannelMessage {
		t.Fatalf("queued up to %d bytes, more than the limit", most)
	}

	// Nobody reads, then the reader goes away: the waiting write must return.
	writer, reading = open()
	go func() {
		_, err := writer.Write(make([]byte, size))
		wrote <- err
	}()
	readEnded := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, writer); readEnded <- err }()
	time.Sleep(300 * time.Millisecond)
	if len(wrote) != 0 {
		t.Fatal("a write with nobody reading finished")
	}
	_ = reading.Close()
	for _, done := range []chan error{readEnded, wrote} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a closed channel left a read or a waiting write behind")
		}
	}
}
