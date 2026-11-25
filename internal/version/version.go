// Package version says which inaba this binary is.
package version

import "runtime/debug"

// version is set by release builds: -ldflags "-X github.com/Alex4386/inaba/internal/version.version=…".
var version string

// String is the version the build was given, or else what Go recorded from the
// repository it was built in, or "dev" when neither is known.
func String() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
