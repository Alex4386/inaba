// Package hostconfig is the "providers" section of the host's config file: the agents it offers. The host
// settings in the web UI read and write it through ProviderConfigService, and a
// change applies to new threads without a restart.
package hostconfig

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"google.golang.org/protobuf/types/known/timestamppb"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/config"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/provider/acp"
	"github.com/Alex4386/inaba/internal/provider/shell"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/provider"
)

// Provider is one agent in the file.
type Provider struct {
	DisplayName string `json:"displayName,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	// Command starts the agent speaking ACP on stdio.
	Command        []string          `json:"command,omitempty"`
	Terminal       []string          `json:"terminal,omitempty"`
	TerminalResume []string          `json:"terminalResume,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	// HiddenModels are model choices left out of the pickers, by value.
	HiddenModels []string `json:"hiddenModels,omitempty"`
	// Icon is a picture chosen for it, as a data: URL; empty shows the registry's.
	Icon string `json:"icon,omitempty"`
	// Removed hides the provider of this id that a lower file defines. The host settings
	// page writes it on delete, since it never edits the lower file itself.
	Removed bool `json:"removed,omitempty"`
}

// File is the config file.
type File struct {
	// Providers maps an id to an agent. "shell", the plain shell inaba always has,
	// takes only a name and whether it is offered.
	Providers map[string]Provider `json:"providers"`
}

const shellID = "shell"

// Load reads the providers from files, lowest first, skipping missing ones. A provider
// in a later file replaces the one with its id in an earlier file, and a removed one
// takes it away.
func Load(files ...string) (File, error) {
	f := File{Providers: map[string]Provider{}}
	for _, path := range files {
		layer, err := readLayer(path)
		if err != nil {
			return f, err
		}
		maps.Copy(f.Providers, layer.Providers)
	}
	maps.DeleteFunc(f.Providers, func(_ string, p Provider) bool { return p.Removed })
	return f, nil
}

// readLayer reads one file's providers as written, removed entries included.
func readLayer(path string) (File, error) {
	layer := File{Providers: map[string]Provider{}}
	doc, err := readDoc(path)
	if err == nil {
		var b []byte
		if b, err = json.Marshal(doc); err == nil {
			err = json.Unmarshal(b, &layer)
		}
	}
	if err != nil {
		return layer, fmt.Errorf("settings file %s: %w", path, err)
	}
	if layer.Providers == nil {
		layer.Providers = map[string]Provider{}
	}
	return layer, nil
}

// readDoc parses a JSON or YAML settings file; a missing one is empty.
func readDoc(path string) (map[string]any, error) {
	parser, err := config.Parser(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	doc, err := parser.Unmarshal(b)
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, err
}

// Save writes the providers into path in one step, in the format its extension names,
// readable only by its owner: the environment may hold keys. Other keys in the file,
// such as the host's own settings written by hand, are kept.
func Save(path string, f File) error {
	doc, err := readDoc(path)
	if err != nil {
		return err
	}
	var providers any
	b, err := json.Marshal(f.Providers)
	if err == nil {
		err = json.Unmarshal(b, &providers)
	}
	if err != nil {
		return err
	}
	doc["providers"] = providers
	if filepath.Ext(path) == ".json" {
		b, err = json.MarshalIndent(doc, "", "  ")
		b = append(b, '\n')
	} else {
		parser, _ := config.Parser(path)
		b, err = parser.Marshal(doc)
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	// A leftover temp file would keep its old, possibly wider, mode through WriteFile.
	os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Providers makes the providers the file describes, the plain shell always among them.
func (f File) Build() ([]provider.Provider, map[string]host.ProviderInfo) {
	return f.build(nil)
}

// build is Build with icons from the registry's agents, when they are known.
func (f File) build(registry []*inabav1.AcpAgent) ([]provider.Provider, map[string]host.ProviderInfo) {
	providers := []provider.Provider{shell.Provider{}}
	info := map[string]host.ProviderInfo{}
	for id, p := range f.Providers {
		info[id] = host.ProviderInfo{DisplayName: p.DisplayName, Disabled: p.Disabled, HiddenModels: p.HiddenModels, Icon: cmp.Or(p.Icon, iconFor(id, p.Command, registry))}
		if id == shellID {
			continue
		}
		a := &acp.Provider{Name: id, Command: p.Command, Terminal: p.Terminal, TerminalResume: p.TerminalResume}
		for _, k := range slices.Sorted(maps.Keys(p.Env)) {
			a.Env = append(a.Env, k+"="+p.Env[k])
		}
		providers = append(providers, a)
	}
	return providers, info
}

// Service serves the providers as ProviderConfigService and applies each change to the host.
type Service struct {
	inabav1connect.UnimplementedProviderConfigServiceHandler
	// Files are the settings files providers are read from, lowest first; empty means Path alone.
	Files []string
	// Path is the user-level settings file, the only one changes are written to, and
	// only with the entries edited there: the system file in /etc is the administrator's
	// and a --config file belongs to whoever placed it, so their providers stay theirs.
	// Deleting a provider a lower file defines writes a removed entry to hide it. A file
	// above Path still wins for a provider it names.
	Path string
	Host *host.Host
	mu   sync.Mutex
	// checks are the last CheckProviders result for each provider.
	checks map[string]*inabav1.ProviderStatus
	// registry is the ACP registry as last fetched, at registryAt.
	registry   []*inabav1.AcpAgent
	registryAt time.Time
}

// Apply loads the file into the host, then fetches the registry in the background so
// the providers get their icons without the host waiting on the network to start.
func (s *Service) Apply() error {
	f, err := s.load()
	if err != nil {
		return err
	}
	s.Host.SetProviders(f.Build())
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.ListAcpAgents(rpc.Trusted(ctx), &inabav1.ListAcpAgentsRequest{}); err != nil {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if f, err := s.load(); err == nil {
			s.Host.SetProviders(f.build(s.registry))
		}
	}()
	return nil
}

// iconFor finds the registry agent a provider's command runs, or failing that the one
// its id names, and returns its icon.
func iconFor(id string, command []string, registry []*inabav1.AcpAgent) string {
	for _, a := range registry {
		if pkg := packageName(a.Command); pkg != "" && slices.ContainsFunc(command, func(w string) bool { return packageName([]string{"npx", "-y", w}) == pkg }) {
			return a.Icon
		}
	}
	for _, a := range registry {
		if a.Id == id || a.Id == id+"-acp" {
			return a.Icon
		}
	}
	return ""
}

// packageName is the package an npx or uvx command runs, without its version.
func packageName(command []string) string {
	var pkg string
	switch {
	case len(command) > 2 && command[0] == "npx":
		pkg = command[2]
	case len(command) > 1 && command[0] == "uvx":
		pkg = command[1]
	default:
		return ""
	}
	if i := strings.LastIndex(pkg, "@"); i > 0 {
		pkg = pkg[:i]
	}
	if i := strings.Index(pkg, "=="); i > 0 {
		pkg = pkg[:i]
	}
	return pkg
}

func message(id string, p Provider) *inabav1.ProviderConfig {
	return &inabav1.ProviderConfig{
		Id: id, DisplayName: p.DisplayName, Enabled: !p.Disabled, Builtin: id == shellID,
		Command: p.Command, Terminal: p.Terminal, TerminalResume: p.TerminalResume, Env: p.Env, HiddenModels: p.HiddenModels, Icon: p.Icon,
	}
}

// describe adds what the host knows of a provider beyond its config: its last check
// and the models its agent offered. The caller holds mu.
func (s *Service) describe(c *inabav1.ProviderConfig) *inabav1.ProviderConfig {
	c.Status = s.checks[c.Id]
	for _, o := range s.Host.Offered(c.Id) {
		if o.Category == "model" {
			c.Models = o.Choices
		}
	}
	return c
}

func (s *Service) ListProviderConfigs(ctx context.Context, _ *inabav1.ListProviderConfigsRequest) (*inabav1.ListProviderConfigsResponse, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return s.list(f), nil
}

// list answers ListProviderConfigs. The caller holds mu.
func (s *Service) list(f File) *inabav1.ListProviderConfigsResponse {
	res := &inabav1.ListProviderConfigsResponse{Path: s.Path, Configs: []*inabav1.ProviderConfig{message(shellID, f.Providers[shellID])}}
	for _, id := range slices.Sorted(maps.Keys(f.Providers)) {
		if id != shellID {
			res.Configs = append(res.Configs, s.describe(message(id, f.Providers[id])))
		}
	}
	return res
}

// checkTimeout bounds one agent's check; npx may first download the agent.
const checkTimeout = 2 * time.Minute

func (s *Service) CheckProviders(ctx context.Context, _ *inabav1.CheckProvidersRequest) (*inabav1.ListProviderConfigsResponse, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	s.mu.Lock()
	f, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	providers, info := f.Build()
	results := make(map[string]*inabav1.ProviderStatus)
	var wg sync.WaitGroup
	var rmu sync.Mutex
	for _, p := range providers {
		c, ok := p.(provider.Checker)
		if !ok || info[p.ID()].Disabled {
			continue
		}
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()
			got, err := c.Check(ctx)
			st := &inabav1.ProviderStatus{Ok: err == nil, Name: got.Name, Version: got.Version, CheckedAt: timestamppb.Now()}
			if err != nil {
				st.Error = err.Error()
			}
			rmu.Lock()
			results[p.ID()] = st
			rmu.Unlock()
		})
	}
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checks == nil {
		s.checks = map[string]*inabav1.ProviderStatus{}
	}
	maps.Copy(s.checks, results)
	return s.list(f), nil
}

var (
	validID   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	validIcon = regexp.MustCompile(`^data:image/(png|jpeg|webp|svg\+xml);base64,[A-Za-z0-9+/=]+$`)
)

const maxIcon = 128 << 10 * 4 / 3

func (s *Service) SaveProviderConfig(ctx context.Context, req *inabav1.SaveProviderConfigRequest) (*inabav1.ProviderConfig, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	c := req.Config
	if c == nil || !validID.MatchString(c.Id) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "an id is lowercase letters, digits, - and _, starting with a letter or digit")
	}
	// A picture only, small enough to travel with every list of providers.
	if c.Icon != "" && (len(c.Icon) > maxIcon || !validIcon.MatchString(c.Icon)) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "an icon is a PNG, JPEG, WebP or SVG image of at most 128 KiB, as a data: URL")
	}
	p := Provider{DisplayName: c.DisplayName, Disabled: !c.Enabled, Icon: c.Icon}
	if c.Id != shellID {
		if len(c.Command) == 0 || c.Command[0] == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a provider needs the command that starts its agent")
		}
		p.Command, p.Terminal, p.TerminalResume, p.HiddenModels = c.Command, c.Terminal, c.TerminalResume, c.HiddenModels
		if len(c.Env) > 0 {
			p.Env = c.Env
		}
		for k := range p.Env {
			if k == "" || strings.Contains(k, "=") {
				return nil, connect.NewError(connect.CodeInvalidArgument, "an environment variable needs a name without =")
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, err := readLayer(s.Path)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	user.Providers[c.Id] = p
	if err := s.write(user); err != nil {
		return nil, err
	}
	return s.describe(message(c.Id, p)), nil
}

func (s *Service) DeleteProviderConfig(ctx context.Context, req *inabav1.DeleteProviderConfigRequest) (*inabav1.DeleteProviderConfigResponse, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	if req.Id == shellID {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the shell is built in; turn it off instead")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	if _, ok := f.Providers[req.Id]; !ok {
		return nil, connect.NewError(connect.CodeNotFound, "no provider "+req.Id)
	}
	user, err := readLayer(s.Path)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	below, err := Load(s.lower()...)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	if _, ok := below.Providers[req.Id]; ok {
		user.Providers[req.Id] = Provider{Removed: true}
	} else {
		delete(user.Providers, req.Id)
	}
	if err := s.write(user); err != nil {
		return nil, err
	}
	return &inabav1.DeleteProviderConfigResponse{}, nil
}

func (s *Service) load() (File, error) {
	if len(s.Files) == 0 {
		return Load(s.Path)
	}
	return Load(s.Files...)
}

// lower are the settings files below Path.
func (s *Service) lower() []string {
	if i := slices.Index(s.Files, s.Path); i >= 0 {
		return s.Files[:i]
	}
	return nil
}

// write saves the user file's providers and applies every layer. The caller holds mu.
func (s *Service) write(user File) error {
	if err := Save(s.Path, user); err != nil {
		return connect.NewError(connect.CodeInternal, err.Error())
	}
	f, err := s.load()
	if err != nil {
		return connect.NewError(connect.CodeInternal, err.Error())
	}
	s.Host.SetProviders(f.build(s.registry))
	return nil
}

// registryURL is the public ACP registry.
const registryURL = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"

type registryAgent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Description  string `json:"description"`
	Website      string `json:"website"`
	Icon         string `json:"icon"`
	Distribution struct {
		Npx *registryPackage `json:"npx"`
		Uvx *registryPackage `json:"uvx"`
	} `json:"distribution"`
}

type registryPackage struct {
	Package string            `json:"package"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// registryAgents turns the registry's entries into what the host would run.
func registryAgents(body []byte) ([]*inabav1.AcpAgent, error) {
	var reg struct {
		Agents []registryAgent `json:"agents"`
	}
	if err := json.Unmarshal(body, &reg); err != nil {
		return nil, err
	}
	var out []*inabav1.AcpAgent
	for _, a := range reg.Agents {
		m := &inabav1.AcpAgent{Id: a.ID, Name: a.Name, Description: a.Description, Version: a.Version, Icon: a.Icon, Website: a.Website}
		switch d := a.Distribution; {
		case d.Npx != nil:
			m.Command, m.Env = append([]string{"npx", "-y", d.Npx.Package}, d.Npx.Args...), d.Npx.Env
		case d.Uvx != nil:
			m.Command, m.Env = append([]string{"uvx", d.Uvx.Package}, d.Uvx.Args...), d.Uvx.Env
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Service) ListAcpAgents(ctx context.Context, _ *inabav1.ListAcpAgentsRequest) (*inabav1.ListAcpAgentsResponse, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The registry changes rarely; an hour's copy spares asking it on every look.
	if s.registry == nil || time.Since(s.registryAt) > time.Hour {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryURL, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, "could not reach the ACP registry: "+err.Error())
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		if err == nil && res.StatusCode != http.StatusOK {
			err = fmt.Errorf("it answered %s", res.Status)
		}
		var agents []*inabav1.AcpAgent
		if err == nil {
			agents, err = registryAgents(body)
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, "could not read the ACP registry: "+err.Error())
		}
		s.registry, s.registryAt = agents, time.Now()
	}
	return &inabav1.ListAcpAgentsResponse{Agents: s.registry}, nil
}
