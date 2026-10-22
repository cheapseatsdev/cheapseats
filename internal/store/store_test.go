package store

import (
	"context"
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
