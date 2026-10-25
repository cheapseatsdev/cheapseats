package suggest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// The review's scratch copy of the sets stays inside its directory: whatever was left there is
// removed (a symlink is removed, not followed), every file is created new, and a set id that is a
// path is refused rather than written outside.
func TestReviewWriteSetsStaysInDir(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "victim.toml")
	os.WriteFile(target, []byte("untouched"), 0o644)
	if err := os.Symlink(target, filepath.Join(dir, "a.toml")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	os.WriteFile(filepath.Join(dir, "stale.toml"), []byte("x"), 0o644)
	if err := reviewWriteSets(dir, []sets.Set{{ID: "a", Name: "A"}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "untouched" {
		t.Errorf("wrote through the symlink: %q", b)
	}
	if st, err := os.Lstat(filepath.Join(dir, "a.toml")); err != nil || !st.Mode().IsRegular() {
		t.Errorf("a.toml: %v %v", st, err)
	}
	if names, _ := os.ReadDir(dir); len(names) != 1 {
		t.Errorf("left over: %v", names)
	}

	err := reviewWriteSets(dir, []sets.Set{{ID: "../escape"}})
	if err == nil || !strings.Contains(err.Error(), "not a file name") {
		t.Errorf("a path as an id: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.toml")); err == nil {
		t.Error("wrote outside the directory")
	}
}
