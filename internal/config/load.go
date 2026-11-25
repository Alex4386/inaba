// Package config loads inaba's settings and works out where its files live.
// Settings come from, lowest first: built-in defaults, every settings file found (see
// App.Files), systemd credentials, INABA_ environment variables, then the command-line
// flags that were set.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

// EnvPrefix namespaces inaba's environment variables. A double underscore nests,
// so single underscores stay usable inside names: INABA_OIDC__CLIENT_SECRET is
// oidc.client_secret. Options.Sections also accept one underscore after a section's
// name, INABA_OIDC_CLIENT_SECRET, which loses to the "__" form when both are set.
const EnvPrefix = "INABA_"

const delim = "."

// Options says where Load reads from besides the defaults and the environment.
type Options struct {
	// Files are JSON or YAML files merged lowest first; missing ones are skipped.
	Files []string
	// Secrets are keys that must not come from a file others can read.
	Secrets []string
	// Lists are keys that take a comma-separated list from the environment.
	Lists map[string]bool
	// Sections are the top-level keys whose children the environment may also name with
	// one underscore, as INABA_OIDC_ISSUER for oidc.issuer.
	Sections []string
	// Credentials maps a systemd credential name in $CREDENTIALS_DIRECTORY to a key.
	Credentials map[string]string
	// Overrides apply last; the command line passes only the flags that were set.
	Overrides map[string]any
}

// Load fills out from defaults, then opts' sources in rising precedence, and returns
// the settings files it read. It then removes secret INABA_ variables from the
// environment, so agents and terminals the process starts never inherit them.
func Load(defaults any, opts Options, out any) ([]string, error) {
	read, err := load(defaults, opts, out)
	unsetSecrets()
	return read, err
}

func load(defaults any, opts Options, out any) ([]string, error) {
	k := koanf.New(delim)
	if err := k.Load(structs.Provider(defaults, "koanf"), nil); err != nil {
		return nil, fmt.Errorf("defaults: %w", err)
	}

	var read []string
	for _, path := range opts.Files {
		parser, err := Parser(path)
		if err != nil {
			return nil, err
		}
		fk := koanf.New(delim)
		err = fk.Load(file.Provider(path), parser)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			err = checkSecretFile(path, fk, opts.Secrets)
		}
		if err == nil {
			err = k.Merge(fk)
		}
		if err != nil {
			return nil, fmt.Errorf("settings file %s: %w", path, err)
		}
		read = append(read, path)
	}

	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		creds := map[string]any{}
		for name, key := range opts.Credentials {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("credential %s: %w", name, err)
			}
			creds[key] = strings.TrimRight(string(b), "\r\n")
		}
		if err := k.Load(confmap.Provider(creds, delim), nil); err != nil {
			return nil, err
		}
	}

	// Shorthands load first so the "__" form, loaded second, wins over them.
	for _, shorthand := range []bool{true, false} {
		if err := k.Load(env.Provider(delim, env.Opt{Prefix: EnvPrefix, TransformFunc: func(name, value string) (string, any) {
			key, short := envKey(name, opts.Sections)
			// INABA_CONFIG names a settings file (see App.Files); it is not a setting.
			if key == "config" || short != shorthand {
				return "", nil
			}
			if opts.Lists[key] {
				return key, splitList(value)
			}
			return key, value
		}}), nil); err != nil {
			return nil, fmt.Errorf("environment: %w", err)
		}
	}

	if len(opts.Overrides) > 0 {
		if err := k.Load(confmap.Provider(opts.Overrides, delim), nil); err != nil {
			return nil, fmt.Errorf("flags: %w", err)
		}
	}
	return read, k.UnmarshalWithConf("", out, koanf.UnmarshalConf{Tag: "koanf"})
}

// envKey turns an INABA_ variable's name into its key, and says whether it used the
// one-underscore shorthand for a section. Keys are lowercase, map keys included.
func envKey(name string, sections []string) (string, bool) {
	key := strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(name, EnvPrefix)), "__", delim)
	first, rest, _ := strings.Cut(key, delim)
	for _, sec := range sections {
		if child, ok := strings.CutPrefix(first, sec+"_"); ok && child != "" {
			if rest != "" {
				child += delim + rest
			}
			return sec + delim + child, true
		}
	}
	return key, false
}

// unsetSecrets removes INABA_ variables whose names say they hold a secret or token.
func unsetSecrets() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		lower := strings.ToLower(name)
		if strings.HasPrefix(name, EnvPrefix) && (strings.Contains(lower, "secret") || strings.Contains(lower, "token")) {
			os.Unsetenv(name)
		}
	}
}

// checkSecretFile refuses a file that holds a secret while its group or others can read it.
func checkSecretFile(path string, k *koanf.Koanf, secrets []string) error {
	// Windows keeps who may read a file elsewhere, and in a user's own folders that is only them.
	if goos == "windows" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm()&0o077 == 0 {
		return err
	}
	for _, key := range secrets {
		if k.String(key) != "" {
			return fmt.Errorf("it holds %s but others can read it: chmod 600 it, or move the secret to the environment or a systemd credential", key)
		}
	}
	return nil
}

// splitList parses a comma-separated list, dropping empty entries.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// FlagOverrides turns the flags that were set on the command line into Options.Overrides,
// keyed by keys (flag name to config key), so an unset flag's default never hides a file or env value.
func FlagOverrides(fs *pflag.FlagSet, keys map[string]string) map[string]any {
	out := map[string]any{}
	fs.Visit(func(f *pflag.Flag) {
		key, ok := keys[f.Name]
		if !ok {
			return
		}
		if s, ok := f.Value.(pflag.SliceValue); ok {
			out[key] = s.GetSlice()
		} else if m, err := fs.GetStringToString(f.Name); err == nil {
			out[key] = m
		} else {
			out[key] = f.Value.String()
		}
	})
	return out
}
