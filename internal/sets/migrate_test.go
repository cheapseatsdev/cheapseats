package sets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A set that is a symlink is refused, so a checkout cannot aim the rewrite at a file outside the
// directory; nothing is rewritten, not even the plain sets beside it. A plain set is replaced by
// a new file with its mode kept, and no temporary file is left behind.
func TestMigrateRewritesOnlyPlainFiles(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	old := "id = \"x\"\nprompts = [\"How do I measure AI visibility?\"]\n"
	target := filepath.Join(outside, "victim.toml")
	os.WriteFile(target, []byte(old), 0o644)
	os.WriteFile(filepath.Join(dir, "a.toml"), []byte(old), 0o600)
	if err := os.Symlink(target, filepath.Join(dir, "b.toml")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	changed, err := Migrate(dir)
	if err == nil || !strings.Contains(err.Error(), "b.toml: not a regular file") || len(changed) != 0 {
		t.Fatalf("symlinked set: changed %v, err %v", changed, err)
	}
	for _, f := range []string{target, filepath.Join(dir, "a.toml")} {
		if b, _ := os.ReadFile(f); string(b) != old {
			t.Errorf("%s was rewritten:\n%s", f, b)
		}
	}

	os.Remove(filepath.Join(dir, "b.toml"))
	if changed, err := Migrate(dir); err != nil || len(changed) != 1 {
		t.Fatalf("plain set: changed %v, err %v", changed, err)
	}
	st, _ := os.Stat(filepath.Join(dir, "a.toml"))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", st.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}
