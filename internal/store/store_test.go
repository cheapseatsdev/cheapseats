package store

import (
	"context"
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

// A data directory from before the rename keeps its popcorn.db; a new one gets cheapseats.db.
func TestOpenKeepsTheOldDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA application_id = 42`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, f := range []string{"cheapseats.db", "cheapseats.db-wal", "cheapseats.db-shm"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			os.Rename(filepath.Join(dir, f), filepath.Join(dir, "popcorn"+f[len("cheapseats"):]))
		}
	}
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id int
	if err := db.QueryRow(`PRAGMA application_id`).Scan(&id); err != nil || id != 42 {
		t.Errorf("opened a different database: %d %v", id, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cheapseats.db")); err == nil {
		t.Error("made a new cheapseats.db beside popcorn.db")
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
	if v != Schema() || v != 12 {
		t.Errorf("schema %d", v)
	}
}
