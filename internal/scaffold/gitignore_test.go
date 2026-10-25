package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The config repo init writes ignores .env and its variants (keys may live there, and the next
// step is a commit) but not .env.example, and the .gitignore is written with the rest.
func TestScaffoldIgnoresDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, Build(site(), nil).Files()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		lines[strings.TrimSpace(l)] = true
	}
	for _, want := range []string{".env", ".env.*", "!.env.example"} {
		if !lines[want] {
			t.Errorf(".gitignore lacks %q:\n%s", want, b)
		}
	}
}
