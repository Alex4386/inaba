package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

type testSettings struct {
	Listen string   `koanf:"listen"`
	URL    string   `koanf:"public_url"`
	Data   string   `koanf:"data"`
	ICE    []string `koanf:"ice_urls"`
	OIDC   struct {
		Issuer  string            `koanf:"issuer"`
		Secret  string            `koanf:"client_secret"`
		Name    string            `koanf:"name"`
		Domains []string          `koanf:"allowed_domains"`
		Extra   map[string]string `koanf:"extra_params"`
		Strict  bool              `koanf:"email_verified_required"`
	} `koanf:"oidc"`
}

func write(t *testing.T, path, body string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadTest(t *testing.T, opts Options) (testSettings, error) {
	t.Helper()
	d := testSettings{Listen: ":8080"}
	d.OIDC.Name = "SSO"
	d.OIDC.Strict = true
	opts.Lists = map[string]bool{"ice_urls": true, "oidc.allowed_domains": true}
	opts.Sections = []string{"oidc"}
	opts.Secrets = []string{"oidc.client_secret"}
	opts.Credentials = map[string]string{"oidc_client_secret": "oidc.client_secret"}
	var out testSettings
	_, err := Load(d, opts, &out)
	return out, err
}

func TestPrecedence(t *testing.T) {
	file := write(t, filepath.Join(t.TempDir(), "server.json"), `{"listen":":1","public_url":"https://file","oidc":{"issuer":"https://file-issuer","client_secret":"from-file"}}`, 0o600)
	t.Setenv("CREDENTIALS_DIRECTORY", "")

	got, err := loadTest(t, Options{Files: []string{file}})
	if err != nil || got.Listen != ":1" || got.URL != "https://file" || got.OIDC.Name != "SSO" || got.OIDC.Secret != "from-file" {
		t.Fatalf("file over defaults: %+v %v", got, err)
	}

	creds := t.TempDir()
	write(t, filepath.Join(creds, "oidc_client_secret"), "from-cred\n", 0o600)
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	t.Setenv("INABA_LISTEN", ":2")
	t.Setenv("INABA_OIDC__ISSUER", "https://env")
	t.Setenv("INABA_ICE_URLS", "stun:a:1, stun:b:2,")
	t.Setenv("INABA_CONFIG", "/ignored")
	// A section takes the one-underscore shorthand; the "__" form wins when both are set.
	t.Setenv("INABA_OIDC_NAME", "Short")
	t.Setenv("INABA_OIDC_ISSUER", "https://loses")
	t.Setenv("INABA_OIDC_ALLOWED_DOMAINS", "a.example, b.example")
	t.Setenv("INABA_OIDC__EXTRA_PARAMS__HD", "a.example")
	t.Setenv("INABA_OIDC_EXTRA_PARAMS__PROMPT", "login")
	t.Setenv("INABA_OIDC__EMAIL_VERIFIED_REQUIRED", "false")
	got, err = loadTest(t, Options{Files: []string{file}})
	if err != nil || got.Listen != ":2" || got.OIDC.Issuer != "https://env" || got.OIDC.Name != "Short" || got.OIDC.Secret != "from-cred" || !slices.Equal(got.ICE, []string{"stun:a:1", "stun:b:2"}) {
		t.Fatalf("env and credential over file: %+v %v", got, err)
	}
	if !slices.Equal(got.OIDC.Domains, []string{"a.example", "b.example"}) || got.OIDC.Extra["hd"] != "a.example" || got.OIDC.Extra["prompt"] != "login" || got.OIDC.Strict {
		t.Fatalf("lists, maps and bools from env: %+v", got.OIDC)
	}

	t.Setenv("INABA_OIDC_CLIENT_SECRET", "from-short")
	got, _ = loadTest(t, Options{Files: []string{file}})
	if got.OIDC.Secret != "from-short" {
		t.Fatalf("shorthand env: %+v", got)
	}
	t.Setenv("INABA_OIDC__CLIENT_SECRET", "from-env")
	t.Setenv("INABA_OIDC_CLIENT_SECRET", "from-short")
	got, _ = loadTest(t, Options{Files: []string{file}})
	if got.OIDC.Secret != "from-env" {
		t.Fatalf("nested env: %+v", got)
	}

	fs := pflag.NewFlagSet("x", pflag.ContinueOnError)
	fs.String("listen", ":8080", "")
	fs.String("public-url", "", "")
	fs.StringSlice("ice-url", nil, "")
	fs.StringToString("extra", nil, "")
	fs.Bool("strict", true, "")
	fs.Parse([]string{"--listen", ":3", "--ice-url", "stun:c:3", "--extra", "hd=flag.example", "--strict"})
	got, _ = loadTest(t, Options{Files: []string{file}, Overrides: FlagOverrides(fs, map[string]string{"listen": "listen", "public-url": "public_url", "ice-url": "ice_urls", "extra": "oidc.extra_params", "strict": "oidc.email_verified_required"})})
	// An unset flag leaves the file's value alone.
	if got.Listen != ":3" || got.URL != "https://file" || !slices.Equal(got.ICE, []string{"stun:c:3"}) || got.OIDC.Extra["hd"] != "flag.example" || !got.OIDC.Strict {
		t.Fatalf("flags over env: %+v", got)
	}
}

// Secret variables leave the environment once read, so child processes never see them.
func TestUnsetsSecrets(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("INABA_OIDC__CLIENT_SECRET", "s")
	t.Setenv("INABA_TOKEN", "t")
	t.Setenv("INABA_LISTEN", ":9")
	got, err := loadTest(t, Options{})
	if err != nil || got.OIDC.Secret != "s" || got.Listen != ":9" {
		t.Fatalf("%+v %v", got, err)
	}
	// The shorthand name is a secret variable too.
	t.Setenv("INABA_OIDC_CLIENT_SECRET", "s")
	got, err = loadTest(t, Options{})
	if err != nil || got.OIDC.Secret != "s" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, name := range []string{"INABA_OIDC__CLIENT_SECRET", "INABA_OIDC_CLIENT_SECRET", "INABA_TOKEN"} {
		if _, ok := os.LookupEnv(name); ok {
			t.Fatalf("%s still set", name)
		}
	}
	if os.Getenv("INABA_LISTEN") != ":9" {
		t.Fatal("unset a variable that is not secret")
	}
}

func TestSecretFileMustBePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes say nothing there; see TestWindows")
	}
	dir := t.TempDir()
	if _, err := loadTest(t, Options{Files: []string{write(t, filepath.Join(dir, "a.json"), `{"oidc":{"client_secret":"s"}}`, 0o644)}}); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("readable secret file: %v", err)
	}
	if _, err := loadTest(t, Options{Files: []string{write(t, filepath.Join(dir, "b.yaml"), "listen: ':1'\n", 0o644)}}); err != nil {
		t.Fatalf("readable file without secrets: %v", err)
	}
	if _, err := loadTest(t, Options{Files: []string{filepath.Join(dir, "missing.json")}}); err != nil {
		t.Fatalf("missing file: %v", err)
	}
}

// clean empties every variable the paths depend on, points home, /etc/inaba and the
// working directory at temp dirs, and returns the fake /etc/inaba.
func clean(t *testing.T, os_ string, root bool) string {
	t.Helper()
	for _, v := range []string{"INABA_CONFIG", "CONFIGURATION_DIRECTORY", "STATE_DIRECTORY", "RUNTIME_DIRECTORY", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "CREDENTIALS_DIRECTORY"} {
		t.Setenv(v, "")
	}
	t.Setenv("HOME", t.TempDir())
	etc := t.TempDir()
	oldOS, oldRoot, oldEtc := goos, isRoot, etcDir
	goos, isRoot, etcDir = os_, func() bool { return root }, etc
	t.Cleanup(func() { goos, isRoot, etcDir = oldOS, oldRoot, oldEtc })
	t.Chdir(t.TempDir())
	return etc
}

// Every settings file found is merged: /etc < systemd < user < explicit. The working
// directory is never read.
func TestFilesMerge(t *testing.T) {
	etc := clean(t, "linux", false)
	user := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", user)
	sysd := t.TempDir()
	t.Setenv("CONFIGURATION_DIRECTORY", sysd)
	cwd, _ := os.Getwd()

	write(t, filepath.Join(etc, "server.json"), `{"listen":":1","public_url":"https://etc","data":"/etc.db","oidc":{"issuer":"https://etc"}}`, 0o644)
	write(t, filepath.Join(sysd, "server.yml"), "listen: ':2'\npublic_url: https://systemd\n", 0o644)
	write(t, filepath.Join(user, "inaba", "server.yaml"), "listen: ':3'\noidc:\n  name: User\n", 0o644)
	for _, name := range []string{"inaba-server.json", "server.json"} {
		write(t, filepath.Join(cwd, name), `{"listen":":4"}`, 0o644)
	}
	explicit := write(t, filepath.Join(t.TempDir(), "x.yaml"), "listen: ':5'\n", 0o644)

	files, err := Server.Files(explicit)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadTest(t, Options{Files: files})
	if err != nil || got.Listen != ":5" || got.URL != "https://systemd" || got.Data != "/etc.db" || got.OIDC.Issuer != "https://etc" || got.OIDC.Name != "User" {
		t.Fatalf("%v: %+v %v", files, got, err)
	}

	// Without the explicit file the user file wins, never the working directory's.
	files, _ = Server.Files("")
	if got, _ = loadTest(t, Options{Files: files}); got.Listen != ":3" || len(files) != 3 {
		t.Fatalf("%v: %+v", files, got)
	}
	t.Setenv("INABA_CONFIG", explicit)
	if files, _ = Server.Files(""); files[len(files)-1] != explicit {
		t.Fatalf("INABA_CONFIG: %v", files)
	}
}

func TestFilesErrors(t *testing.T) {
	etc := clean(t, "linux", false)
	if _, err := Host.Files(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("accepted a missing explicit file")
	}
	if _, err := Host.Files(write(t, filepath.Join(t.TempDir(), "c.toml"), "", 0o600)); err == nil {
		t.Fatal("accepted an unknown format")
	}
	write(t, filepath.Join(etc, "config.json"), "{}", 0o644)
	write(t, filepath.Join(etc, "config.yaml"), "{}", 0o644)
	if _, err := Host.Files(""); err == nil || !strings.Contains(err.Error(), "one format") {
		t.Fatalf("two formats side by side: %v", err)
	}
}

func TestPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("these are unix paths")
	}
	clean(t, "linux", false)
	home, _ := os.UserHomeDir()
	if got, _ := Host.UserFile(); got != filepath.Join(home, ".config/inaba/config.json") {
		t.Fatal(got)
	}
	if got := Host.DataPath(); got != filepath.Join(home, ".local/share/inaba/inaba.db") {
		t.Fatal(got)
	}
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	if got := Server.DataPath(); got != "/xdg/data/inaba/inaba-server.db" {
		t.Fatal(got)
	}
	isRoot = func() bool { return true }
	t.Setenv("STATE_DIRECTORY", "/var/lib/x")
	if got := Server.DataPath(); got != "/var/lib/inaba/inaba-server.db" {
		t.Fatal(got)
	}
}

// The socket follows the database alone, so commands run from cron, sudo or another
// shell find the one a running server or host listens on.
func TestSocketDeterministic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("these are unix paths")
	}
	want := SocketPath("/srv/a.db")
	if want != "/srv/a.db.sock" {
		t.Fatal(want)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1")
	t.Setenv("RUNTIME_DIRECTORY", "/run/x")
	if got := SocketPath("/srv/a.db"); got != want {
		t.Fatal(got)
	}
}

func TestLongSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("these are unix paths")
	}
	// A short private temp folder, so the test never touches a real host's socket folder.
	tmp, err := os.MkdirTemp("/tmp", "it")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	t.Setenv("TMPDIR", tmp)

	long := "/" + strings.Repeat("d", 120) + "/inaba.db.sock"
	got := FitSocket(long)
	if len(got) > maxSocket || got != FitSocket(long) || !strings.HasPrefix(got, tmp+"/inaba-") {
		t.Fatal(got)
	}
	if FitSocket("/short.sock") != "/short.sock" {
		t.Fatal("short path changed")
	}
	if err := PrepareSocket(got); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Dir(got), 0o755)
	if err := PrepareSocket(got); err == nil {
		t.Fatal("accepted a shared socket folder")
	}
}

// On Windows /etc names a folder anyone may make, and modes say nothing about who may read a file.
func TestWindows(t *testing.T) {
	etc := clean(t, "windows", false)
	write(t, filepath.Join(etc, "server.json"), `{"listen":":1"}`, 0o644)
	files, err := Server.Files("")
	if err != nil || slices.Contains(files, filepath.Join(etc, "server.json")) {
		t.Fatalf("files %v, %v", files, err)
	}
	secret := write(t, filepath.Join(t.TempDir(), "a.json"), `{"oidc":{"client_secret":"s"}}`, 0o644)
	if _, err := loadTest(t, Options{Files: []string{secret}}); err != nil {
		t.Fatal(err)
	}
	if cache, _ := os.UserCacheDir(); Host.DataPath() != filepath.Join(cache, "inaba", "inaba.db") {
		t.Fatal(Host.DataPath())
	}
}
