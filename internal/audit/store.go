package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// keepAudits is how many audits are kept.
const keepAudits = 20

// Save keeps an audit, and drops all but the last few.
func Save(ctx context.Context, db *sql.DB, r Result) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO site_audits (created_at, result) VALUES (?, ?)`, r.At, string(b)); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM site_audits WHERE id NOT IN (SELECT id FROM site_audits ORDER BY id DESC LIMIT ?)`, keepAudits)
	return err
}

// Latest is the newest audit, or nil when there's none.
func Latest(ctx context.Context, db *sql.DB) (*Result, error) {
	var js string
	err := db.QueryRowContext(ctx, `SELECT result FROM site_audits ORDER BY id DESC LIMIT 1`).Scan(&js)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Result
	return &r, json.Unmarshal([]byte(js), &r)
}

// SaveCheck keeps a run's citation check, replacing any earlier one.
func SaveCheck(ctx context.Context, db *sql.DB, c CitationCheck) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT OR REPLACE INTO citation_checks (run_id, created_at, result) VALUES (?, ?, ?)`, c.RunID, c.At, string(b))
	return err
}

// LatestCheck is a run's citation check, or nil when it hasn't had one.
func LatestCheck(ctx context.Context, db *sql.DB, runID string) (*CitationCheck, error) {
	var js string
	err := db.QueryRowContext(ctx, `SELECT result FROM citation_checks WHERE run_id=?`, runID).Scan(&js)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c CitationCheck
	return &c, json.Unmarshal([]byte(js), &c)
}
