package config

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/v2"
)

// App names one program's files.
type App struct {
	// Name is the settings file's name in /etc/inaba, $CONFIGURATION_DIRECTORY and the
	// user config folder.
	Name string
	DB   string
	// System lets a root process use /etc/inaba and /var/lib/inaba's database.
	System bool
}

var (
	Server = App{Name: "server", DB: "inaba-server.db", System: true}
	Host   = App{Name: "config", DB: "inaba.db"}
)

// Seams for tests.
var (
	goos   = runtime.GOOS
	isRoot = func() bool { return os.Geteuid() == 0 }
	etcDir = "/etc/inaba"
)

// extensions are the settings formats, each read by the parser its extension names.
var extensions = []string{".json", ".yaml", ".yml"}

// Parser picks the koanf parser for a settings file by its extension.
func Parser(path string) (koanf.Parser, error) {
	switch filepath.Ext(path) {
	case ".json":
		return json.Parser(), nil
	case ".yaml", ".yml":
		return yaml.Parser(), nil
	}
	return nil, fmt.Errorf("settings file %s: the name must end in .json, .yaml or .yml", path)
}

func home() string { h, _ := os.UserHomeDir(); return h }

func (a App) system() bool { return a.System && isRoot() }

// find is dir/base with whichever settings extension exists, or "" when none does.
// Two formats side by side are refused, since neither could be said to win.
func find(dir, base string) (string, error) {
	var found []string
	for _, ext := range extensions {
		p := filepath.Join(dir, base+ext)
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	if len(found) > 1 {
		return "", fmt.Errorf("settings files %s: keep only one format", strings.Join(found, " and "))
	}
	if len(found) == 0 {
		return "", nil
	}
	return found[0], nil
}

// UserFile is the settings file in this user's config folder, the only one the host
// settings page writes; it names config.json when none exists yet.
func (a App) UserFile() (string, error) {
	dir := filepath.Join(configDir(), "inaba")
	p, err := find(dir, a.Name)
	if p == "" && err == nil {
		p = filepath.Join(dir, a.Name+".json")
	}
	return p, err
}

// Files lists the settings files to merge, lowest first: /etc/inaba, then
// $CONFIGURATION_DIRECTORY (systemd), the user config folder, and explicit (--config or
// $INABA_CONFIG), which must exist. The working directory is never searched: a host
// started inside a repository must not let that repository's file pick the commands it
// runs. The user file is listed even before it exists, so a host reloading its providers
// sees the one it just created.
func (a App) Files(explicit string) ([]string, error) {
	var files []string
	for _, dir := range []string{etcDir, os.Getenv("CONFIGURATION_DIRECTORY")} {
		// On Windows /etc is a folder any user may make, on whatever drive is current.
		if dir == "" || dir == etcDir && goos == "windows" {
			continue
		}
		p, err := find(dir, a.Name)
		if err != nil {
			return nil, err
		}
		if p != "" && !slices.Contains(files, p) {
			files = append(files, p)
		}
	}
	user, err := a.UserFile()
	if err != nil {
		return nil, err
	}
	if !slices.Contains(files, user) {
		files = append(files, user)
	}
	if explicit = cmp.Or(explicit, os.Getenv("INABA_CONFIG")); explicit != "" {
		if _, err := Parser(explicit); err != nil {
			return nil, err
		}
		if _, err := os.Stat(explicit); err != nil {
			return nil, fmt.Errorf("settings file: %w", err)
		}
		files = append(files, explicit)
	}
	return files, nil
}

// DataPath is where the database is when nothing sets it; its folder also holds
// the host's worktrees and sign-in. It reads no service manager variables, so a root
// admin command and the service find the same file.
func (a App) DataPath() string {
	if a.system() {
		return filepath.Join("/var/lib/inaba", a.DB)
	}
	return filepath.Join(dataDir(), "inaba", a.DB)
}

// SocketPath is the control socket beside the database. It depends on nothing but the
// database path, so every command that finds the database also finds a running server or host.
func SocketPath(data string) string { return FitSocket(data + ".sock") }

// maxSocket is the longest unix socket path that works everywhere: macOS allows 103 bytes.
const maxSocket = 103

// FitSocket returns p, or when it is too long for a unix socket, a short name
// derived from it in a folder of this user's, so every command finds the same one.
func FitSocket(p string) string {
	if abs, err := filepath.Abs(p); err == nil && len(abs) <= maxSocket || len(p) <= maxSocket {
		return p
	}
	sum := sha256.Sum256([]byte(p))
	name := hex.EncodeToString(sum[:8]) + ".sock"
	temps := []string{os.TempDir()}
	if goos != "windows" {
		temps = append(temps, "/tmp")
	}
	for _, tmp := range temps {
		if s := filepath.Join(tmp, fmt.Sprintf("inaba-%d", os.Getuid()), name); len(s) <= maxSocket {
			return s
		}
	}
	return p
}

// PrepareSocket makes the socket's folder. A folder in a shared temp directory must be
// this user's alone, or another user could stand in for the server on it.
// shortcut: checks the folder's mode, not its owner; add an owner check if the temp fallback sees shared use.
func PrepareSocket(p string) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// On Windows the temp folder is the user's own, and modes say nothing.
	if goos == "windows" || !strings.HasPrefix(filepath.Base(dir), "inaba-") {
		return nil
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return errors.New("socket folder " + dir + " is not private to this user")
	}
	return nil
}

func configDir() string {
	if goos == "linux" {
		if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
			return d
		}
		return filepath.Join(home(), ".config")
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(home(), ".config")
	}
	return d
}

func dataDir() string {
	if goos == "linux" {
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return d
		}
		return filepath.Join(home(), ".local", "share")
	}
	// Worktrees and databases stay on the machine rather than roam with the user.
	if d, err := os.UserCacheDir(); goos == "windows" && err == nil {
		return d
	}
	return configDir()
}
