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
	if named, err := client.RenameHostTerminal(ctx, &inabav1.RenameHostTerminalRequest{Id: term.Id, Name: "  build  "}); err != nil || named.Name != "build" {
		t.Fatalf("renamed = %v, %v", named, err)
	}
	if _, err := client.RenameHostTerminal(ctx, &inabav1.RenameHostTerminalRequest{Id: term.Id, Name: strings.Repeat("가", maxName+1)}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a name too long: %v", err)
	}
	if _, err := client.RenameHostTerminal(ctx, &inabav1.RenameHostTerminalRequest{Id: "nope", Name: "x"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("renaming no terminal: %v", err)
	}
	if list, err := client.ListHostTerminals(ctx, &inabav1.ListHostTerminalsRequest{}); err != nil || list.Terminals[0].Name != "build" {
		t.Fatalf("the name is not kept: %v, %v", list, err)
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

// A terminal is its opener's: another account neither sees it nor reaches it by id, and a
// trusted call sees and manages every one.
func TestATerminalIsItsOpeners(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	s := newService()
	t.Cleanup(s.stop)
	h := host.New()
	t.Cleanup(h.Close)
	if err := h.Contribute([]module.Contribution{{Services: func(server *connect.Server) { inabav1connect.RegisterHostTerminalServiceHandler(server, s) }}}); err != nil {
		t.Fatal(err)
	}
	handler, _ := h.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(rpc.PermissionsHeader, rpc.PermThreadsAnywhere)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	client := inabav1connect.NewHostTerminalServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL)))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	as := func(user string) context.Context {
		ctx, info := connect.NewClientContext(ctx)
		info.RequestHeader().Set(rpc.UserHeader, user)
		return ctx
	}
	dir := testdir.New(t)
	open := func(user string) *inabav1.HostTerminal {
		t.Helper()
		term, err := client.OpenHostTerminal(as(user), &inabav1.OpenHostTerminalRequest{Cwd: dir})
		if err != nil {
			t.Fatal(err)
		}
		return term
	}
	alices, bobs, nobodys := open("alice"), open("bob"), open("")
	for user, want := range map[string]string{"alice": alices.Id, "bob": bobs.Id, "": nobodys.Id} {
		if list, err := client.ListHostTerminals(as(user), &inabav1.ListHostTerminalsRequest{}); err != nil || len(list.Terminals) != 1 || list.Terminals[0].Id != want {
			t.Errorf("%q sees %v, %v", user, list, err)
		}
	}
	notFound := func(what string, err error) {
		t.Helper()
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("bob, %s alice's terminal: %v", what, err)
		}
	}
	bob, id := as("bob"), alices.Id
	if out, err := client.AttachHostTerminal(bob, &inabav1.AttachHostTerminalRequest{Id: id}); err == nil {
		_, err = out.Receive()
		notFound("attaching to", err)
	} else {
		notFound("attaching to", err)
	}
	_, err := client.WriteHostTerminal(bob, &inabav1.WriteHostTerminalRequest{Id: id, Data: []byte("x")})
	notFound("writing to", err)
	_, err = client.ResizeHostTerminal(bob, &inabav1.ResizeHostTerminalRequest{Id: id, Cols: 80, Rows: 24})
	notFound("resizing", err)
	_, err = client.RenameHostTerminal(bob, &inabav1.RenameHostTerminalRequest{Id: id, Name: "mine"})
	notFound("renaming", err)
	// Closing quietly succeeds, as for any id that is gone, but leaves alice's be.
	if _, err := client.CloseHostTerminal(bob, &inabav1.CloseHostTerminalRequest{Id: id}); err != nil {
		t.Errorf("bob, closing alice's terminal: %v", err)
	}
	if list, err := client.ListHostTerminals(as("alice"), &inabav1.ListHostTerminalsRequest{}); err != nil || len(list.Terminals) != 1 {
		t.Errorf("closed by bob, alice sees %v, %v", list, err)
	}
	if _, err := client.WriteHostTerminal(as("alice"), &inabav1.WriteHostTerminalRequest{Id: id, Data: []byte("x")}); err != nil {
		t.Errorf("alice, writing to her own: %v", err)
	}

	trusted := rpc.Trusted(context.Background())
	if list, err := s.ListHostTerminals(trusted, &inabav1.ListHostTerminalsRequest{}); err != nil || len(list.Terminals) != 3 {
		t.Fatalf("a trusted call sees %v, %v", list, err)
	}
	if _, err := s.RenameHostTerminal(trusted, &inabav1.RenameHostTerminalRequest{Id: id, Name: "kept"}); err != nil {
		t.Errorf("a trusted call renaming: %v", err)
	}
	if _, err := s.CloseHostTerminal(trusted, &inabav1.CloseHostTerminalRequest{Id: id}); err != nil {
		t.Errorf("a trusted call closing: %v", err)
	}
	if list, _ := client.ListHostTerminals(as("alice"), &inabav1.ListHostTerminalsRequest{}); len(list.GetTerminals()) != 0 {
		t.Errorf("closed by a trusted call, still listed: %v", list)
	}
}
