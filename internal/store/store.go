// Package store keeps runs and results in SQLite (pure Go driver, so the binary cross-compiles
// with no C toolchain).
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

//go:embed 002_prompt_ids.sql
var promptIDs string

//go:embed 003_provenance.sql
var provenance string

//go:embed 004_fix_attempts.sql
var fixAttempts string

//go:embed 005_site_checks.sql
var siteChecks string

//go:embed 006_themes.sql
var themes string

//go:embed 007_regrades.sql
var regrades string

//go:embed 008_shares.sql
var shares string

//go:embed 009_prompt_reviews.sql
var promptReviews string

// Each entry is applied once, in order; append, never edit.
var migrations = []string{schema, promptIDs, provenance, fixAttempts, siteChecks, themes, regrades, shares, promptReviews}

// Schema is the schema version this Popcorn brings a database to.
func Schema() int { return len(migrations) }

type DB struct{ *sql.DB }

func Open(dataDir string) (*DB, error) { return open(dataDir, len(migrations)) }

// OpenAt opens the database with only the first n migrations applied, as a release that knew
// only those would leave it: for tests that upgrade from it.
func OpenAt(dataDir string, n int) (*DB, error) { return open(dataDir, n) }

func open(dataDir string, n int) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dataDir, "popcorn.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := migrate(db, filepath.Join(dataDir, "popcorn.db"), migrations[:n]); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db}, nil
}

// migrate applies the migrations the database lacks. backup is its path, for the copy taken first.
func migrate(db *sql.DB, backup string, migrations []string) error {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("the database is from a newer popcorn (schema %d, this one knows %d); upgrade popcorn rather than run an older one on it", v, len(migrations))
	}
	// Before changing an existing database, copy it, so a bad upgrade can be undone by putting
	// the copy back and running the previous version.
	if v > 0 && v < len(migrations) && backup != "" {
		dst := fmt.Sprintf("%s.bak-v%d", backup, v)
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			if _, err := db.Exec(`VACUUM INTO ?`, dst); err != nil {
				return fmt.Errorf("backing up before migrating: %w", err)
			}
		}
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func Now() string { return time.Now().UTC().Format(time.RFC3339) }

// SpentToday is everything charged since 00:00 UTC: answers, grading and analysis.
func (db *DB) SpentToday(ctx context.Context) (float64, error) {
	day := time.Now().UTC().Format("2006-01-02")
	var total float64
	err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE substr(created_at,1,10)=?`, day).Scan(&total)
	return total, err
}
