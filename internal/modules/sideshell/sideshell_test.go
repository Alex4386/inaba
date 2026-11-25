package sideshell

import (
	"context"
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
	"github.com/Alex4386/inaba/pkg/provider"
)

type idleAgent struct{}

func (idleAgent) ID() string { return "idle" }
func (idleAgent) Open(context.Context, provider.Config, provider.Emit) (provider.Session, error) {
	return idleSession{}, nil
}

type idleSession struct{}

func (idleSession) NativeSessionID() string                                 { return "n" }
func (idleSession) Prompt(context.Context, provider.Prompt) (string, error) { return "end_turn", nil }
func (idleSession) Interrupt(context.Context) error                         { return nil }
func (idleSession) Respond(context.Context, string, string) error           { return nil }
func (idleSession) Close() error                                            { return nil }

func TestAShellBesideEachThread(t *testing.T) {
	// A plain shell, whatever the machine's login shell prints. PowerShell on Windows reads the
	// same lines below.
	t.Setenv("SHELL", "/bin/sh")
	ctx, cancel := context.WithTimeout(rpc.Trusted(context.Background()), time.Minute)
	t.Cleanup(cancel)
	h := host.New(idleAgent{})
	t.Cleanup(h.Close)
	s := newService(h, h)
	s.start()
	t.Cleanup(s.stop)
	if err := h.Contribute([]module.Contribution{{Services: func(server *connect.Server) { inabav1connect.RegisterShellServiceHandler(server, s) }}}); err != nil {
		t.Fatal(err)
	}
	handler, _ := h.Handler()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := inabav1connect.NewShellServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL)))

	// The shell ends with the test, but Windows may not have let go of its folder yet.
	dir := testdir.New(t)
	// Windows may name the temp folder the short way, and the shell the long way.
	if long, err := filepath.EvalSymlinks(dir); err == nil && runtime.GOOS == "windows" {
		dir = long
	}
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "idle", Cwd: dir})
	if _, err := client.WriteShell(ctx, &inabav1.WriteShellRequest{ThreadId: th.Id, Data: []byte("x")}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("writing before the shell started: %v", err)
	}
	out, err := client.AttachShell(ctx, &inabav1.AttachShellRequest{ThreadId: th.Id, Cols: 250, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteShell(ctx, &inabav1.WriteShellRequest{ThreadId: th.Id, Data: []byte("echo \"inaba-$((1+1)) $PWD\"\r")}); err != nil {
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
	// It runs in the thread's folder.
	if !strings.Contains(got, "inaba-2 "+dir) && !strings.Contains(got, "inaba-2 /private"+dir) {
		t.Fatalf("the shell is not in the thread's folder: %q", got)
	}

	// A second shell is a process of its own, and closing it leaves the first running.
	second, err := client.AttachShell(ctx, &inabav1.AttachShellRequest{ThreadId: th.Id, ShellId: "two", Cols: 132, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	size := "echo \"size-$(stty size)\"\r"
	if runtime.GOOS == "windows" {
		size = "echo \"size-$([Console]::WindowHeight) $([Console]::WindowWidth)\"\r"
	}
	if _, err := client.WriteShell(ctx, &inabav1.WriteShellRequest{ThreadId: th.Id, ShellId: "two", Data: []byte(size)}); err != nil {
		t.Fatal(err)
	}
	// It started at the viewer's size, not 80 by 24.
	for got = ""; !strings.Contains(got, "size-40 132"); {
		chunk, err := second.Receive()
		if err != nil {
			t.Fatalf("second shell so far %q: %v", got, err)
		}
		got += string(chunk.Data)
	}
	if _, err := client.CloseShell(ctx, &inabav1.CloseShellRequest{ThreadId: th.Id, ShellId: "two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteShell(ctx, &inabav1.WriteShellRequest{ThreadId: th.Id, ShellId: "two", Data: []byte("x")}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("writing to a closed shell: %v", err)
	}
	if _, err := client.WriteShell(ctx, &inabav1.WriteShellRequest{ThreadId: th.Id, Data: []byte("\n")}); err != nil {
		t.Fatalf("the first shell went with the second: %v", err)
	}
	if bad, err := client.AttachShell(ctx, &inabav1.AttachShellRequest{ThreadId: th.Id, ShellId: "../x"}); err == nil {
		if _, err := bad.Receive(); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("a shell id with a slash: %v", err)
		}
	}

	// Its thread deleted, the shell ends and its stream with it.
	if _, err := h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: th.Id}); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() {
		for {
			if _, err := out.Receive(); err != nil {
				ended <- err
				return
			}
		}
	}()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the shell outlived its thread")
	}
}
