package hostconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/rpc"
)

func offered(t *testing.T, h *host.Host) map[string]string {
	t.Helper()
	res, err := h.ListProviders(context.Background(), &inabav1.ListProvidersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, p := range res.Providers {
		names[p.Id] = p.DisplayName
	}
	return names
}

// A change made over the API lands in the file and in what the host offers.
func TestSaveAppliesAndPersists(t *testing.T) {
	ctx := rpc.Trusted(context.Background())
	path := filepath.Join(t.TempDir(), "config.json")
	h := host.New()
	s := &Service{Path: path, Host: h}
	if err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := offered(t, h); len(got) != 1 || got["shell"] != "shell" {
		t.Fatalf("without a file: %v", got)
	}

	if _, err := s.SaveProviderConfig(ctx, &inabav1.SaveProviderConfigRequest{Config: &inabav1.ProviderConfig{Id: "claude", DisplayName: "Claude", Enabled: true, Command: []string{"claude-acp"}, Env: map[string]string{"KEY": "v"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProviderConfig(ctx, &inabav1.SaveProviderConfigRequest{Config: &inabav1.ProviderConfig{Id: "shell", DisplayName: "Shell", Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if got := offered(t, h); len(got) != 1 || got["claude"] != "Claude" {
		t.Fatalf("after saving: %v", got)
	}
	if fi, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
	// A host started on the file offers the same.
	again := host.New()
	if err := (&Service{Path: path, Host: again}).Apply(); err != nil {
		t.Fatal(err)
	}
	if got := offered(t, again); len(got) != 1 || got["claude"] != "Claude" {
		t.Fatalf("reloaded: %v", got)
	}

	if _, err := s.SaveProviderConfig(ctx, &inabav1.SaveProviderConfigRequest{Config: &inabav1.ProviderConfig{Id: "pic", Enabled: true, Command: []string{"x"}, Icon: "data:image/png;base64,iVBORw0KGgo="}}); err != nil {
		t.Fatalf("a provider with a picture: %v", err)
	}
	if got, _ := h.ListProviders(ctx, &inabav1.ListProvidersRequest{}); !slices.ContainsFunc(got.Providers, func(p *inabav1.Provider) bool { return p.Id == "pic" && p.Icon != "" }) {
		t.Fatalf("the picture is not offered: %v", got)
	}
	s.DeleteProviderConfig(ctx, &inabav1.DeleteProviderConfigRequest{Id: "pic"})
	for _, bad := range []*inabav1.ProviderConfig{{Id: "Bad Id", Command: []string{"x"}}, {Id: "nocmd"}, {Id: "env", Command: []string{"x"}, Env: map[string]string{"A=B": "c"}}, {Id: "html", Command: []string{"x"}, Icon: "data:text/html;base64,PHNjcmlwdD4="}} {
		if _, err := s.SaveProviderConfig(ctx, &inabav1.SaveProviderConfigRequest{Config: bad}); err == nil {
			t.Fatalf("saved %v", bad)
		}
	}
	if _, err := s.DeleteProviderConfig(ctx, &inabav1.DeleteProviderConfigRequest{Id: "shell"}); err == nil {
		t.Fatal("removed the shell")
	}
	if _, err := s.DeleteProviderConfig(ctx, &inabav1.DeleteProviderConfigRequest{Id: "claude"}); err != nil {
		t.Fatal(err)
	}
	if got := offered(t, h); len(got) != 0 {
		t.Fatalf("after removing: %v", got)
	}
	// Without the hosts permission nothing is read, since the environment may hold keys.
	if _, err := s.ListProviderConfigs(context.Background(), &inabav1.ListProviderConfigsRequest{}); err == nil {
		t.Fatal("listed without permission")
	}
}

// Registry entries become commands the host runs; a download-only agent gets none.
func TestRegistryAgents(t *testing.T) {
	agents, err := registryAgents([]byte(`{"agents":[
		{"id":"codex-acp","name":"Codex","distribution":{"npx":{"package":"@agentclientprotocol/codex-acp@2.12.0"}}},
		{"id":"fast","name":"fast","distribution":{"uvx":{"package":"fast-agent-acp==0.1","args":["-x"],"env":{"M":"m"}}}},
		{"id":"amp","name":"Amp","distribution":{"binary":{"darwin-aarch64":{"cmd":"./amp"}}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range agents {
		got[a.Id] = strings.Join(a.Command, " ")
	}
	want := map[string]string{"codex-acp": "npx -y @agentclientprotocol/codex-acp@2.12.0", "fast": "uvx fast-agent-acp==0.1 -x", "amp": ""}
	if fmt.Sprint(got) != fmt.Sprint(want) || agents[1].Env["M"] != "m" {
		t.Fatalf("got %v", got)
	}
}

// Saving providers keeps the host's own settings written by hand in the same file.
func TestSaveKeepsOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"server":"https://inaba.example.com","data":"/srv/inaba.db","providers":{"old":{"command":["x"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, File{Providers: map[string]Provider{"claude": {Command: []string{"claude-acp"}}}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	providers, _ := doc["providers"].(map[string]any)
	if doc["server"] != "https://inaba.example.com" || doc["data"] != "/srv/inaba.db" || providers["claude"] == nil || providers["old"] != nil {
		t.Fatalf("got %s", b)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

// Providers merge across files, and the UI writes only the entries it edits, only to
// the user file, in its format; deleting a lower file's provider hides it there.
func TestLayersWriteUserFileOnly(t *testing.T) {
	ctx := rpc.Trusted(context.Background())
	dir := t.TempDir()
	etc := filepath.Join(dir, "etc.json")
	os.WriteFile(etc, []byte(`{"providers":{"a":{"command":["etc-a"]},"b":{"command":["etc-b"]}}}`), 0o644)
	user := filepath.Join(dir, "user", "config.yaml")
	h := host.New()
	s := &Service{Files: []string{etc, user}, Path: user, Host: h}
	save := func(id, cmd string) {
		t.Helper()
		if _, err := s.SaveProviderConfig(ctx, &inabav1.SaveProviderConfigRequest{Config: &inabav1.ProviderConfig{Id: id, Enabled: true, Command: []string{cmd}}}); err != nil {
			t.Fatal(err)
		}
	}
	userDoc := func() map[string]any {
		t.Helper()
		doc, err := readDoc(user)
		if err != nil {
			t.Fatal(err)
		}
		providers, _ := doc["providers"].(map[string]any)
		return providers
	}
	save("b", "user-b")
	save("c", "user-c")
	f, err := Load(etc, user)
	if err != nil || f.Providers["a"].Command[0] != "etc-a" || f.Providers["b"].Command[0] != "user-b" {
		t.Fatalf("%+v %v", f, err)
	}
	if b, _ := os.ReadFile(etc); !strings.Contains(string(b), "etc-b") {
		t.Fatal("rewrote the lower file")
	}
	if b, _ := os.ReadFile(user); !strings.Contains(string(b), "user-b") || strings.HasPrefix(string(b), "{") {
		t.Fatalf("user file not YAML: %s", b)
	}
	// /etc's own provider stays owned by /etc: a later change there still shows.
	if got := userDoc(); got["a"] != nil || len(got) != 2 {
		t.Fatalf("copied a lower provider into the user file: %v", got)
	}
	if fi, _ := os.Stat(user); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// Deleting /etc's provider writes a removed entry; deleting the user's own drops it.
	for _, id := range []string{"a", "c"} {
		if _, err := s.DeleteProviderConfig(ctx, &inabav1.DeleteProviderConfigRequest{Id: id}); err != nil {
			t.Fatal(err)
		}
	}
	if got := userDoc(); fmt.Sprint(got["a"]) != "map[removed:true]" || got["c"] != nil {
		t.Fatalf("user file after deleting: %v", got)
	}
	if got := offered(t, h); got["a"] != "" || got["c"] != "" || got["b"] == "" {
		t.Fatalf("offered after deleting: %v", got)
	}
	if _, err := s.DeleteProviderConfig(ctx, &inabav1.DeleteProviderConfigRequest{Id: "a"}); err == nil {
		t.Fatal("deleted a hidden provider again")
	}
	// Saving the id again replaces the removed entry.
	save("a", "user-a")
	if f, _ := Load(etc, user); f.Providers["a"].Command[0] != "user-a" {
		t.Fatalf("%+v", f)
	}
}
