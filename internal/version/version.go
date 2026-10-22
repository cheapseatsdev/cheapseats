// Package version says which Cheap Seats this is.
package version

import "runtime/debug"

// Version is set at release build time:
//
//	go build -ldflags "-X github.com/cheapseatsdev/cheapseats/internal/version.Version=v1.2.3"
var Version = ""

// String is the release version, or for any other build the commit it was built from, or "dev".
func String() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		rev, dirty := "", false
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if len(rev) > 12 {
			rev = rev[:12]
		}
		if rev != "" {
			if dirty {
				rev += "-dirty"
			}
			return "dev-" + rev
		}
	}
	return "dev"
}
