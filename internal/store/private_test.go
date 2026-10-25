package store

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// privateMode fails the test unless p has exactly the given permissions.
func privateMode(t *testing.T, p string, want fs.FileMode) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s is %o, want %o", filepath.Base(p), got, want)
	}
}

// Under the usual umask (022) everything the store writes would be world-readable: the database
// holds every answer and share token. A new data directory, database, WAL, shared memory and
// backup must be this user's alone.
func TestNewDatabaseIsPrivate(t *testing.T) {
	defer syscall.Umask(syscall.Umask(0o022))
	dir := filepath.Join(t.TempDir(), "data")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (n INTEGER); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cheapseats.db")
	privateMode(t, dir, 0o700)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		privateMode(t, p, 0o600)
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := db.Backup(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
	privateMode(t, dst, 0o600)
	if err := db.Backup(context.Background(), dst); err == nil {
		t.Error("a backup over an existing file succeeded")
	}
}

// A database an older release left 0644 in a 0755 directory is tightened when it opens, and so
// is the copy taken before migrating; a symlink among its files is left alone, not followed.
func TestExistingDatabaseIsTightened(t *testing.T) {
	defer syscall.Umask(syscall.Umask(0o022))
	dir := t.TempDir()
	db, err := OpenAt(dir, len(migrations)-1)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	path := filepath.Join(dir, "cheapseats.db")
	outside := filepath.Join(t.TempDir(), "someone-elses")
	if err := os.WriteFile(outside, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path+".bak-v0"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	privateMode(t, dir, 0o700)
	privateMode(t, path, 0o600)
	privateMode(t, fmt.Sprintf("%s.bak-v%d", path, len(migrations)-1), 0o600)
	privateMode(t, outside, 0o644)
}
