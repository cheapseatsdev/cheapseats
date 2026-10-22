package env

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestTheOldNamesStillWork(t *testing.T) {
	t.Setenv("CHEAPSEATS_ADDR", "")
	t.Setenv("POPCORN_ADDR", "old")
	if Get("ADDR") != "old" || Or("ADDR", "d") != "old" {
		t.Error("POPCORN_ADDR isn't read")
	}
	if !slices.Contains(Old(), "POPCORN_ADDR") {
		t.Errorf("Old() = %v", Old())
	}
	t.Setenv("CHEAPSEATS_ADDR", "new")
	if Get("ADDR") != "new" || slices.Contains(Old(), "POPCORN_ADDR") {
		t.Error("CHEAPSEATS_ADDR doesn't win")
	}
	t.Setenv("CHEAPSEATS_ADDR", "")
	t.Setenv("POPCORN_ADDR", "")
	if Or("ADDR", "d") != "d" {
		t.Error("no default")
	}
}

func TestFile(t *testing.T) {
	dir := t.TempDir()
	if got := File(dir, "cheapseats.db", "popcorn.db"); got != filepath.Join(dir, "cheapseats.db") {
		t.Errorf("a new install: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "popcorn.db"), nil, 0o644)
	if got := File(dir, "cheapseats.db", "popcorn.db"); got != filepath.Join(dir, "popcorn.db") {
		t.Errorf("an install from before the rename: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "cheapseats.db"), nil, 0o644)
	if got := File(dir, "cheapseats.db", "popcorn.db"); got != filepath.Join(dir, "cheapseats.db") {
		t.Errorf("both: %s", got)
	}
}
