package terminal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/testdir"
	"github.com/Alex4386/inaba/pkg/module"
)

func TestATerminalApartFromThreads(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	s := newService()
	t.Cleanup(s.stop)
	h := host.New()
	t.Cleanup(h.Close)
	if err := h.Contribute([]module.Contribution{{Services: func(server *connect.Server) { inabav1connect.RegisterHostTerminalServiceHandler(server, s) }}}); err != nil {
		t.Fatal(err)
	}
	handler, _ := h.Handler()
	// What inaba-server says the caller may do, as it does in front of every call.
	granted := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(rpc.PermissionsHeader, granted)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	client := inabav1connect.NewHostTerminalServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL)))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	if _, err := client.OpenHostTerminal(ctx, &inabav1.OpenHostTerminalRequest{}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("opening without threads-anywhere: %v", err)
	}
	granted = rpc.PermThreadsAnywhere
	dir := testdir.New(t)
	if long, err := filepath.EvalSymlinks(dir); err == nil && runtime.GOOS == "windows" {
		dir = long
	}
	if _, err := client.OpenHostTerminal(ctx, &inabav1.OpenHostTerminalRequest{Cwd: "relative"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a relative folder: %v", err)
	}
	term, err := client.OpenHostTerminal(ctx, &inabav1.OpenHostTerminalRequest{Cwd: dir, Cols: 120, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.AttachHostTerminal(ctx, &inabav1.AttachHostTerminalRequest{Id: term.Id, Cols: 120, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteHostTerminal(ctx, &inabav1.WriteHostTerminalRequest{Id: term.Id, Data: []byte("echo \"inaba-$((1+1)) $PWD\"\r")}); err != nil {
		t.Fatal(err)
	}
	var got string
	for !strings.Contains(got, "inaba-2 ") {
		chunk, err := out.Receive()
		if err != nil {
			t.Fatalf("output so far %q: %v", got, err)
		}
		got += string(chunk.Data)
	}
	if !strings.Contains(got, "inaba-2 "+dir) && !strings.Contains(got, "inaba-2 /private"+dir) {
		t.Fatalf("the terminal is not in its folder: %q", got)
	}
	list, err := client.ListHostTerminals(ctx, &inabav1.ListHostTerminalsRequest{})
	if err != nil || len(list.Terminals) != 1 || list.Terminals[0].Cwd != dir || list.Terminals[0].Exited {
		t.Fatalf("list = %v, %v", list, err)
	}

	// Closed, it is gone from the list and its stream ends.
	if _, err := client.CloseHostTerminal(ctx, &inabav1.CloseHostTerminalRequest{Id: term.Id}); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	go func() {
		for {
			if _, err := out.Receive(); err != nil {
				close(ended)
				return
			}
		}
	}()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived its terminal")
	}
	if list, _ := client.ListHostTerminals(ctx, &inabav1.ListHostTerminalsRequest{}); len(list.GetTerminals()) != 0 {
		t.Fatalf("a closed terminal is still listed: %v", list)
	}
	if _, err := client.WriteHostTerminal(ctx, &inabav1.WriteHostTerminalRequest{Id: term.Id, Data: []byte("x")}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("writing to a closed terminal: %v", err)
	}
}
