// Package store keeps runs and results in SQLite (pure Go driver, so the binary cross-compiles
// with no C toolchain).
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
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

//go:embed 010_providers.sql
var providers string

//go:embed 011_judgments.sql
var judgments string

//go:embed 012_misstatements.sql
var misstatements string

//go:embed 013_next_steps.sql
var nextSteps string

//go:embed 014_evaluation.sql
var evaluation string

// Each entry is applied once, in order; append, never edit.
var migrations = []string{schema, promptIDs, provenance, fixAttempts, siteChecks, themes, regrades, shares, promptReviews, providers, judgments, misstatements, nextSteps, evaluation}

// Schema is the schema version this Cheap Seats brings a database to.
func Schema() int { return len(migrations) }

type DB struct{ *sql.DB }

func Open(dataDir string) (*DB, error) { return open(dataDir, len(migrations)) }

// OpenAt opens the database with only the first n migrations applied, as a release that knew
// only those would leave it: for tests that upgrade from it.
func OpenAt(dataDir string, n int) (*DB, error) { return open(dataDir, n) }

func open(dataDir string, n int) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "cheapseats.db")
	// The database holds every answer, fix and share token: only this user reads it. A new one
	// starts as an empty file made 0600, and SQLite gives its -wal and -shm the database's mode.
	// One made by an older release, or copied in, is tightened here.
	if f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
		f.Close()
	} else if !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	tighten(dataDir, path)
	// _txlock=immediate: every transaction here reads and then writes, and a deferred one that read
	// before another committed can't take the write lock (SQLITE_BUSY, which busy_timeout doesn't
	// wait out). Taking it at BEGIN makes concurrent writers queue on busy_timeout instead.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := migrate(db, path, migrations[:n]); err != nil {
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
		return fmt.Errorf("the database is from a newer cheapseats (schema %d, this one knows %d); upgrade cheapseats rather than run an older one on it", v, len(migrations))
	}
	// Before changing an existing database, copy it, so a bad upgrade can be undone by putting
	// the copy back and running the previous version.
	if v > 0 && v < len(migrations) && backup != "" {
		dst := fmt.Sprintf("%s.bak-v%d", backup, v)
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			if err := vacuumInto(context.Background(), db, dst); err != nil {
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

// Backup writes a consistent copy of the database to dst (SQLite's VACUUM INTO, safe while serve
// runs), readable only by this user. dst must not exist.
func (db *DB) Backup(ctx context.Context, dst string) error { return vacuumInto(ctx, db.DB, dst) }

// vacuumInto copies the database into a new file at dst made 0600 first: VACUUM INTO accepts an
// empty file, and would otherwise create it with the umask's mode.
func vacuumInto(ctx context.Context, db *sql.DB, dst string) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// tighten makes the data directory 0700 and the database, its sidecars and its pre-migration
// copies 0600, where an older release or a copy left them open to other users. It never follows
// a symlink, and a mode it can't change (a mount owned by someone else) is logged, not fatal.
func tighten(dataDir, path string) {
	if fi, err := os.Lstat(dataDir); err == nil && fi.IsDir() && fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dataDir, 0o700); err != nil {
			slog.Warn("can't make the data directory private", "dir", dataDir, "err", err)
		}
	}
	baks, _ := filepath.Glob(path + ".bak-v*")
	for _, p := range append([]string{path, path + "-wal", path + "-shm"}, baks...) {
		if err := chmodNoFollow(p, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("can't make a database file private", "file", p, "err", err)
		}
	}
}

// chmodNoFollow sets a regular file's mode when it has any group or other bits, through a
// descriptor opened with O_NOFOLLOW, so a symlink at p is refused rather than followed.
func chmodNoFollow(p string, mode fs.FileMode) error {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s isn't a regular file", p)
	}
	if fi.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return f.Chmod(mode)
}

func Now() string { return time.Now().UTC().Format(time.RFC3339) }

// SpentToday is everything charged since 00:00 UTC: answers, grading and analysis.
func (db *DB) SpentToday(ctx context.Context) (float64, error) {
	day := time.Now().UTC().Format("2006-01-02")
	var total float64
	err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE substr(created_at,1,10)=?`, day).Scan(&total)
	return total, err
}
