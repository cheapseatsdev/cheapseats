// Package env reads Cheap Seats' settings from the environment. Each is CHEAPSEATS_<name>; the
// POPCORN_<name> it had before the rename still works, so an upgrade doesn't need its env file
// rewritten first.
package env

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	Prefix    = "CHEAPSEATS_"
	OldPrefix = "POPCORN_"
)

// Get is CHEAPSEATS_<name>, else POPCORN_<name>, else "".
func Get(name string) string {
	if v := os.Getenv(Prefix + name); v != "" {
		return v
	}
	return os.Getenv(OldPrefix + name)
}

// Or is Get, or d when neither is set.
func Or(name, d string) string {
	if v := Get(name); v != "" {
		return v
	}
	return d
}

// Old lists the POPCORN_ variables that are set without their CHEAPSEATS_ name, for a warning
// at startup.
func Old() []string {
	var old []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if name, ok := strings.CutPrefix(k, OldPrefix); ok && os.Getenv(k) != "" && os.Getenv(Prefix+name) == "" {
			old = append(old, k)
		}
	}
	sort.Strings(old)
	return old
}

// File is dir/name, unless only dir/old exists: the file a release before the rename wrote, such
// as popcorn.toml or popcorn.db.
func File(dir, name, old string) string {
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		if o := filepath.Join(dir, old); exists(o) {
			return o
		}
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
