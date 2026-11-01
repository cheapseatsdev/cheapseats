package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Transactions that read and then write, several at once, as re-grades of different sets do,
// must wait their turn rather than fail with "database is locked": a deferred transaction that
// read before another one committed can't upgrade to a write, and busy_timeout doesn't help it.
func TestConcurrentReadThenWriteTransactions(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE counter (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO counter VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	const workers, rounds = 4, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				err := func() error {
					tx, err := db.BeginTx(context.Background(), nil)
					if err != nil {
						return err
					}
					defer tx.Rollback()
					var n int
					if err := tx.QueryRow(`SELECT n FROM counter`).Scan(&n); err != nil {
						return err
					}
					if _, err := tx.Exec(`UPDATE counter SET n=?`, n+1); err != nil {
						return err
					}
					return tx.Commit()
				}()
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT n FROM counter`).Scan(&n); err != nil || n != workers*rounds {
		t.Fatalf("counter = %d, want %d (%v): increments were lost", n, workers*rounds, err)
	}
}

// The optional providers' tables arrive with schema 10; a database from before gets them.
func TestUpgradeAddsProviderTables(t *testing.T) {
	dir := t.TempDir()
	old, err := OpenAt(dir, 9)
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Schema 10 brought the first two; 11 the rest of the list.
	for _, table := range []string{"name_decisions", "coverage", "phrase_clusters", "grade_opinions", "prompt_judgments",
		"fix_matches", "page_judgments", "prospects", "retrieval_baseline", "competitor_suggestions", "exa_contents"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s after upgrade: %d %v", table, n, err)
		}
	}
	var v int
	db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != Schema() {
		t.Errorf("schema %d", v)
	}
}

func TestUpgradeAddsFactCheckTables(t *testing.T) {
	dir := t.TempDir()
	old, err := OpenAt(dir, 11)
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"fact_checks", "misstatements"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s after upgrade: %d %v", table, n, err)
		}
	}
	var v int
	db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != Schema() {
		t.Errorf("schema %d", v)
	}
}

// After a pre-migration copy is restored and the database used on the older release, the next
// upgrade must copy the database as it now is. Keeping the first copy would hand anyone who rolls
// back again data from before the first upgrade, silently dropping everything since.
func TestUpgradeAfterRestoreBacksUpCurrentData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cheapseats.db")
	bak := fmt.Sprintf("%s.bak-v%d", path, len(migrations)-1)
	setMarker := func(v string) {
		t.Helper()
		db, err := OpenAt(dir, len(migrations)-1)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS upgrade_marker (v TEXT); DELETE FROM upgrade_marker; INSERT INTO upgrade_marker VALUES (?)`, v); err != nil {
			t.Fatal(err)
		}
	}
	upgrade := func() {
		t.Helper()
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	backedUp := func() string {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+bak+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var v string
		if err := db.QueryRow(`SELECT v FROM upgrade_marker`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	setMarker("before the first upgrade")
	upgrade()
	if got := backedUp(); got != "before the first upgrade" {
		t.Fatalf("first backup holds %q", got)
	}
	// Roll back: put the copy back, run the older release on it, and change something.
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		os.Remove(f)
	}
	b, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	setMarker("after the restore")
	upgrade()
	if got := backedUp(); got != "after the restore" {
		t.Fatalf("the backup before the second upgrade holds %q: the stale copy from the first upgrade was kept", got)
	}
	if _, err := os.Stat(bak + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("the partial copy was left behind: %v", err)
	}
}
