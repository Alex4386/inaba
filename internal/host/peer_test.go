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
		conn := &channelConn{rwc: raw}
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
}
