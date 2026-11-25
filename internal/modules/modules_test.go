package modules

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"go.uber.org/fx"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/modules/automation"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/testdir"
	"github.com/Alex4386/inaba/pkg/module"
)

// broken cannot start, to show it does not take the other modules down.
var broken = module.Module{Name: "broken", Title: "Broken", Default: true, Option: fx.Invoke(func() error { return errors.New("no luck") })}

func TestModulesTurnOnAndOffWhileTheHostRuns(t *testing.T) {
	dbPath := filepath.Join(testdir.New(t), "inaba.db")
	open := func() (*host.Host, *Manager, inabav1connect.AutomationServiceClient, inabav1connect.ModuleServiceClient, *httptest.Server) {
		t.Helper()
		h := host.New()
		if err := h.OpenData(dbPath); err != nil {
			t.Fatal(err)
		}
		handler, err := h.Handler()
		if err != nil {
			t.Fatal(err)
		}
		m, err := Start(h, []module.Module{automation.Module, broken})
		if err != nil {
			t.Fatal(err)
		}
		// Everyone may manage everything here; permissions are the host's own tests' business.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set(rpc.PermissionsHeader, rpc.PermProjects+","+rpc.PermThreadsAnywhere+","+rpc.PermAutomations+","+rpc.PermHostSettings+","+rpc.PermHousekeeping)
			handler.ServeHTTP(w, r)
		}))
		t.Cleanup(func() {
			srv.Close()
			m.Close()
			h.Close()
		})
		client := connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
		return h, m, inabav1connect.NewAutomationServiceClient(client), inabav1connect.NewModuleServiceClient(client), srv
	}
	ctx := context.Background()
	_, m, autos, mods, srv := open()

	list, err := mods.ListModules(ctx, &inabav1.ListModulesRequest{})
	if err != nil || len(list.Modules) != 2 || !list.Modules[0].Enabled || list.Modules[0].Error != "" || !strings.Contains(list.Modules[1].Error, "no luck") {
		t.Fatalf("modules: %v %v", list, err)
	}
	made, err := autos.CreateAutomation(ctx, &inabav1.Automation{Name: "kept", Prompt: "p", Webhook: true, NewThread: &inabav1.NewThread{Provider: "none", Cwd: "/"}})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a module's API through the host: %v %v", made, err)
	}

	// Turned off, its API and its webhook route are gone; the host is still up.
	if _, err := mods.SetModule(ctx, &inabav1.SetModuleRequest{Name: "automations", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := autos.ListAutomations(ctx, &inabav1.ListAutomationsRequest{}); connect.CodeOf(err) != connect.CodeUnimplemented && connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("a turned-off module's API: %v", err)
	}
	if res, _ := http.Post(srv.URL+"/hooks/x", "application/json", nil); res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("a turned-off module's route answered %d", res.StatusCode)
	}
	if _, err := mods.SetModule(ctx, &inabav1.SetModuleRequest{Name: "nope", Enabled: true}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("an unknown module: %v", err)
	}
	m.Close()

	// The setting outlives the process.
	_, _, autos, mods, _ = open()
	list, _ = mods.ListModules(ctx, &inabav1.ListModulesRequest{})
	if list.Modules[0].Enabled {
		t.Fatal("automations came back on after a restart")
	}
	if _, err := mods.SetModule(ctx, &inabav1.SetModuleRequest{Name: "automations", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := autos.ListAutomations(ctx, &inabav1.ListAutomationsRequest{}); err != nil {
		t.Fatalf("turned back on: %v", err)
	}
}
