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
	"time"
)

var ErrExhausted = errors.New("daily admission budget exhausted")

type Controller struct {
	Cfg   *config.Config
	DB    *store.DB
	gate  chan struct{}
	scope string
	limit float64
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
	return &Controller{Cfg: c.Cfg, DB: c.DB, gate: c.gate, scope: kind, limit: limit}, nil
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
	res, err := c.DB.ExecContext(ctx, `INSERT INTO spend_events(created_at,kind,model,amount_usd,state) SELECT ?,?,?,?,? WHERE (?<=0 OR (SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE substr(created_at,1,10)=?)+?<=?) AND (?='' OR (SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE kind=?)+?<=?)`, store.Now(), kind, model, amount, Reserved, c.Cfg.Budget.DailyUSD, day, amount, c.Cfg.Budget.DailyUSD, c.scope, c.scope, amount, c.limit)
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
