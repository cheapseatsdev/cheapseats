package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// Share is a read-only link to one run's report, for someone without a sign-in. The link carries
// a random 128-bit token; only its SHA-256 is stored, so the link is shown once, when it's made,
// and a copy of the database doesn't open any.
type Share struct {
	ID             int64   `json:"id"`
	RunID          string  `json:"run_id"`
	IncludeAnswers bool    `json:"include_answers"`
	CreatedBy      string  `json:"created_by"`
	CreatedAt      string  `json:"created_at"`
	ExpiresAt      string  `json:"expires_at"`
	RevokedAt      *string `json:"revoked_at,omitempty"`
}

// Live reports whether the link still opens: not revoked, not expired.
func (s Share) Live() bool { return s.RevokedAt == nil && s.ExpiresAt > store.Now() }

// Share link lifetimes, in days.
const (
	ShareDays    = 30
	ShareMaxDays = 365
)

// ErrNoShare is a token that opens nothing: unknown, revoked or expired, all alike.
var ErrNoShare = errors.New("no such share")

func shareHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateShare makes a link to a finished run, good for days (ShareDays when 0), and returns it
// with its token: the only time the token is known.
func (a *App) CreateShare(ctx context.Context, runID string, days int, includeAnswers bool, by string) (Share, string, error) {
	if days <= 0 {
		days = ShareDays
	}
	if days > ShareMaxDays {
		return Share{}, "", fmt.Errorf("a share lasts at most %d days", ShareMaxDays)
	}
	r, err := a.Runs.Get(ctx, runID)
	if err != nil {
		return Share{}, "", fmt.Errorf("no run %q", runID)
	}
	if r.Status != run.Done {
		return Share{}, "", fmt.Errorf("run %s is %s; only a finished run can be shared", runID, r.Status)
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Share{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now().UTC()
	sh := Share{RunID: runID, IncludeAnswers: includeAnswers, CreatedBy: by, CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.AddDate(0, 0, days).Format(time.RFC3339)}
	res, err := a.DB.ExecContext(ctx, `INSERT INTO shares (token_hash, run_id, include_answers, created_by, created_at, expires_at) VALUES (?,?,?,?,?,?)`,
		shareHash(token), sh.RunID, sh.IncludeAnswers, sh.CreatedBy, sh.CreatedAt, sh.ExpiresAt)
	if err != nil {
		return Share{}, "", err
	}
	sh.ID, _ = res.LastInsertId()
	return sh, token, nil
}

// LatestFinishedRun is the set's newest finished run, for sharing "the set's latest".
func (a *App) LatestFinishedRun(ctx context.Context, setID string) (string, error) {
	var id string
	err := a.DB.QueryRowContext(ctx, `SELECT id FROM runs WHERE set_id=? AND status='done' ORDER BY created_at DESC LIMIT 1`, setID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("prompt set %q has no finished run", setID)
	}
	return id, err
}

const shareCols = `id, run_id, include_answers, created_by, created_at, expires_at, revoked_at`

func scanShare(sc interface{ Scan(...any) error }) (Share, error) {
	var s Share
	var revoked sql.NullString
	err := sc.Scan(&s.ID, &s.RunID, &s.IncludeAnswers, &s.CreatedBy, &s.CreatedAt, &s.ExpiresAt, &revoked)
	if revoked.Valid {
		s.RevokedAt = &revoked.String
	}
	return s, err
}

// Shares are a run's links, newest first, live or not.
func (a *App) Shares(ctx context.Context, runID string) ([]Share, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT `+shareCols+` FROM shares WHERE run_id=? ORDER BY id DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		s, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeShare stops a link working, and returns it. Revoking twice keeps the first time.
func (a *App) RevokeShare(ctx context.Context, id int64) (Share, error) {
	if _, err := a.DB.ExecContext(ctx, `UPDATE shares SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, store.Now(), id); err != nil {
		return Share{}, err
	}
	s, err := scanShare(a.DB.QueryRowContext(ctx, `SELECT `+shareCols+` FROM shares WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNoShare
	}
	return s, err
}

// OpenShare is the live share a token opens, or ErrNoShare. The token is looked up by its hash,
// so how long the lookup takes says nothing about the token, and the hash is compared again in
// constant time.
func (a *App) OpenShare(ctx context.Context, token string) (Share, error) {
	if len(token) != 22 { // 16 bytes, base64url without padding
		return Share{}, ErrNoShare
	}
	h := shareHash(token)
	var stored string
	row := a.DB.QueryRowContext(ctx, `SELECT token_hash, `+shareCols+` FROM shares WHERE token_hash=?`, h)
	var s Share
	var revoked sql.NullString
	if err := row.Scan(&stored, &s.ID, &s.RunID, &s.IncludeAnswers, &s.CreatedBy, &s.CreatedAt, &s.ExpiresAt, &revoked); err != nil {
		return Share{}, ErrNoShare
	}
	if revoked.Valid {
		s.RevokedAt = &revoked.String
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(h)) != 1 || !s.Live() {
		return Share{}, ErrNoShare
	}
	return s, nil
}
