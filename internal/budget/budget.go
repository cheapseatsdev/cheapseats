// Package budget admits every billable attempt against a SQLite ledger shared by all processes.
package budget

import (
	"context"
	"errors"
	"fmt"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/state"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"math"
	"strings"
	"time"
)

var ErrExhausted = errors.New("admission budget exhausted")

type Controller struct {
	Cfg        *config.Config
	DB         *store.DB
	gate       chan struct{}
	scope      string
	limit      float64
	kinds      []string
	groupLimit float64
}

func New(c *config.Config, d *store.DB) *Controller {
	n := c.Budget.Concurrency
	if n < 1 {
		n = 1
	}
	return &Controller{Cfg: c, DB: d, gate: make(chan struct{}, n)}
}

// Scoped shares the daily ledger and concurrency gate while also limiting one
// explicit operation across every process, provider retry and calendar day.
func (c *Controller) Scoped(kind string, limit float64) (*Controller, error) {
	if kind == "" || limit <= 0 || math.IsNaN(limit) || math.IsInf(limit, 0) {
		return nil, fmt.Errorf("an operation and positive admission budget are required")
	}
	next := *c
	next.scope, next.limit = kind, limit
	return &next, nil
}

// WithinKinds adds one shared admission ceiling without replacing per-operation
// or daily limits. Kinds retain their existing ledger identities across resumes.
func (c *Controller) WithinKinds(kinds []string, limit float64) (*Controller, error) {
	if len(kinds) == 0 || len(kinds) > 500 || len(c.kinds) != 0 || limit <= 0 || math.IsNaN(limit) || math.IsInf(limit, 0) {
		return nil, fmt.Errorf("choose up to 500 operations and a positive group admission budget")
	}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if kind == "" || seen[kind] {
			return nil, fmt.Errorf("operation identities must be distinct and nonempty")
		}
		seen[kind] = true
	}
	next := *c
	next.kinds, next.groupLimit = append([]string(nil), kinds...), limit
	return &next, nil
}

func (c *Controller) SpentKinds(ctx context.Context, kinds []string) (float64, error) {
	if len(kinds) == 0 {
		return 0, nil
	}
	args := make([]any, len(kinds))
	for i, kind := range kinds {
		args[i] = kind
	}
	var spent float64
	err := c.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE kind IN (`+strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")+`)`, args...).Scan(&spent)
	return spent, err
}

type Lease struct {
	c        *Controller
	id       int64
	reserved float64
	released bool
}

func (c *Controller) Reserve(ctx context.Context, kind, model string, amount float64) (*Lease, error) {
	if c.scope != "" && kind != c.scope {
		return nil, fmt.Errorf("operation does not match its admission budget")
	}
	if len(c.kinds) > 0 {
		member := false
		for _, k := range c.kinds {
			member = member || k == kind
		}
		if !member {
			return nil, fmt.Errorf("operation is outside this group admission budget")
		}
	}
	if amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return nil, fmt.Errorf("invalid reservation")
	}
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	day := time.Now().UTC().Format("2006-01-02")
	Transitions.Check("spend_events", state.Start, Reserved)
	query := `INSERT INTO spend_events(created_at,kind,model,amount_usd,state) SELECT ?,?,?,?,? WHERE (?<=0 OR (SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE substr(created_at,1,10)=?)+?<=?) AND (?='' OR (SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE kind=?)+?<=?)`
	args := []any{store.Now(), kind, model, amount, Reserved, c.Cfg.Budget.DailyUSD, day, amount, c.Cfg.Budget.DailyUSD, c.scope, c.scope, amount, c.limit}
	if len(c.kinds) > 0 {
		query += ` AND (SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE kind IN (` + strings.TrimSuffix(strings.Repeat("?,", len(c.kinds)), ",") + `))+?<=?`
		for _, k := range c.kinds {
			args = append(args, k)
		}
		args = append(args, amount, c.groupLimit)
	}
	res, err := c.DB.ExecContext(ctx, query, args...)
	if err != nil {
		<-c.gate
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		<-c.gate
		if err != nil {
			return nil, err
		}
		return nil, ErrExhausted
	}
	id, err := res.LastInsertId()
	if err != nil {
		<-c.gate
		return nil, err
	}
	return &Lease{c: c, id: id, reserved: amount}, nil
}

// Finish reconciles known usage; unknown/cancelled attempts retain their reservation.
func (l *Lease) Finish(amount float64, known bool) error {
	if l == nil || l.released {
		return nil
	}
	l.released = true
	defer func() { <-l.c.gate }()
	st := Settled
	if !known {
		amount = max(amount, l.reserved)
		st = Unknown
	}
	if amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		amount = l.reserved
		st = Unknown
	}
	Transitions.Check("spend_events", Reserved, st)
	_, err := l.c.DB.Exec(`UPDATE spend_events SET amount_usd=?,state=? WHERE id=?`, amount, st, l.id)
	return err
}
func (c *Controller) Allowance() float64 {
	if c.Cfg.Budget.ReservationUSD > 0 {
		return c.Cfg.Budget.ReservationUSD
	}
	return 0.60
}
