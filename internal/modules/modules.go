// Package modules runs a host's modules: the ones built into this binary, each in an
// fx app of its own, turned on and off while the host keeps running.
package modules

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"go.uber.org/fx"
	"gorm.io/gorm"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/modules/agenttools"
	"github.com/Alex4386/inaba/internal/modules/automation"
	"github.com/Alex4386/inaba/internal/modules/browser"
	"github.com/Alex4386/inaba/internal/modules/files"
	"github.com/Alex4386/inaba/internal/modules/mcp"
	"github.com/Alex4386/inaba/internal/modules/sideshell"
	"github.com/Alex4386/inaba/pkg/module"
)

// Builtin is every module this binary carries.
var Builtin = []module.Module{files.Module, sideshell.Module, automation.Module, mcp.Module, browser.Module, agenttools.Module}

// How long a module gets to start or stop.
const lifecycleTimeout = 30 * time.Second

type setting struct {
	Name    string `gorm:"primaryKey"`
	Enabled bool
}

func (setting) TableName() string { return "module_settings" }

// running is a module's app while it runs, and what it gives the host.
type running struct {
	app           *fx.App
	contributions []module.Contribution
}

// Manager starts and stops a host's modules. It is the host's ModuleController.
type Manager struct {
	h       *host.Host
	catalog []module.Module
	db      *gorm.DB

	mu      sync.Mutex
	enabled map[string]bool
	running map[string]*running
	// Why a module that should run does not.
	failed map[string]string
}

// Start runs the host's enabled modules and lets the host turn them on and off.
func Start(h *host.Host, catalog []module.Module) (*Manager, error) {
	m := &Manager{h: h, catalog: catalog, db: h.DB(), enabled: map[string]bool{}, running: map[string]*running{}, failed: map[string]string{}}
	for _, mod := range catalog {
		m.enabled[mod.Name] = mod.Default
	}
	if m.db != nil {
		if err := m.db.AutoMigrate(&setting{}); err != nil {
			return nil, err
		}
		var saved []setting
		if err := m.db.Find(&saved).Error; err != nil {
			return nil, err
		}
		for _, s := range saved {
			if _, known := m.enabled[s.Name]; known {
				m.enabled[s.Name] = s.Enabled
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mod := range catalog {
		if m.enabled[mod.Name] {
			m.start(mod)
		}
	}
	h.SetModuleController(m)
	return m, m.contribute()
}

// Close stops every module, after taking their API away so no call reaches one mid-stop.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	stopping := m.running
	m.running = map[string]*running{}
	_ = m.contribute()
	for name, r := range stopping {
		stopApp(name, r)
	}
}

func (m *Manager) List() []*inabav1.Module {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*inabav1.Module
	for _, mod := range m.catalog {
		out = append(out, m.state(mod))
	}
	return out
}

func (m *Manager) Set(name string, enabled bool) (*inabav1.Module, error) {
	mod, ok := m.find(name)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, "no module "+name+" on this host")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db != nil {
		if err := m.db.Save(&setting{Name: name, Enabled: enabled}).Error; err != nil {
			return nil, connect.NewError(connect.CodeInternal, "could not save the setting").WithCause(err)
		}
	}
	m.enabled[name] = enabled
	_, isRunning := m.running[name]
	switch {
	case enabled && !isRunning:
		m.start(mod)
	case !enabled && isRunning:
		// Its API goes first, so no call reaches it while it stops.
		r := m.running[name]
		delete(m.running, name)
		if err := m.contribute(); err != nil {
			return nil, connect.NewError(connect.CodeInternal, "could not serve the modules' API").WithCause(err)
		}
		stopApp(name, r)
		return m.state(mod), nil
	}
	if err := m.contribute(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, "could not serve the modules' API").WithCause(err)
	}
	return m.state(mod), nil
}

func (m *Manager) find(name string) (module.Module, bool) {
	for _, mod := range m.catalog {
		if mod.Name == name {
			return mod, true
		}
	}
	return module.Module{}, false
}

// state is how a module stands; the caller holds m.mu.
func (m *Manager) state(mod module.Module) *inabav1.Module {
	return &inabav1.Module{Name: mod.Name, Title: mod.Title, Description: mod.Description, Enabled: m.enabled[mod.Name], Error: m.failed[mod.Name]}
}

// start runs one module in an app of its own, so one that fails leaves the others
// alone; the caller holds m.mu.
func (m *Manager) start(mod module.Module) {
	delete(m.failed, mod.Name)
	r := &running{}
	app := fx.New(
		fx.NopLogger,
		fx.Supply(
			fx.Annotate(m.h, fx.As(new(module.Threads)), fx.As(new(module.Events))),
			module.Store{DB: m.db},
			module.DataDir(m.h.DataDir()),
		),
		fx.Module(mod.Name, mod.Option),
		fx.Invoke(fx.Annotate(func(cs []module.Contribution) { r.contributions = cs }, fx.ParamTags(`group:"contributions"`))),
	)
	if err := app.Err(); err != nil {
		m.fail(mod.Name, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleTimeout)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		m.fail(mod.Name, err)
		return
	}
	r.app = app
	m.running[mod.Name] = r
	slog.Info("module started", "module", mod.Name)
}

func (m *Manager) fail(name string, err error) {
	m.failed[name] = err.Error()
	slog.Error("a module could not start", "module", name, "error", err.Error())
}

// stopApp ends one module's app.
func stopApp(name string, r *running) {
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleTimeout)
	defer cancel()
	if err := r.app.Stop(ctx); err != nil {
		slog.Warn("a module did not stop cleanly", "module", name, "error", err.Error())
	}
	slog.Info("module stopped", "module", name)
}

// contribute gives the host what the running modules serve; the caller holds m.mu.
func (m *Manager) contribute() error {
	var all []module.Contribution
	for _, mod := range m.catalog {
		if r := m.running[mod.Name]; r != nil {
			all = append(all, r.contributions...)
		}
	}
	return m.h.Contribute(all)
}
