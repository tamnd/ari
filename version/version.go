// Package version holds the build metadata stamped at link time. It has
// no dependencies so anything, including the hello event, can read it.
package version

import "runtime/debug"

// Stamped by goreleaser via ldflags; the defaults describe a source build.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Resolve returns the version, preferring the ldflags stamp and falling
// back to module build info for a plain go install.
func Resolve() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}
