// Package budget admits every billable attempt against a SQLite ledger shared by all processes.
package budget

import (
	"context"
	"errors"
	"fmt"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/store"
	"math"
	"time"
)

var ErrExhausted = errors.New("daily admission budget exhausted")

type Controller struct {
	Cfg  *config.Config
	DB   *store.DB
	gate chan struct{}
}

func New(c *config.Config, d *store.DB) *Controller {
	n := c.Budget.Concurrency
	if n < 1 {
		n = 1
	}
	return &Controller{c, d, make(chan struct{}, n)}
}

type Lease struct {
	c        *Controller
	id       int64
	reserved float64
	released bool
}

func (c *Controller) Reserve(ctx context.Context, kind, model string, amount float64) (*Lease, error) {
	if amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return nil, fmt.Errorf("invalid reservation")
	}
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	day := time.Now().UTC().Format("2006-01-02")
	res, err := c.DB.ExecContext(ctx, `INSERT INTO spend_events(created_at,kind,model,amount_usd,state) SELECT ?,?,?,?,'reserved' WHERE ?<=0 OR (SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE substr(created_at,1,10)=?)+?<=?`, store.Now(), kind, model, amount, c.Cfg.Budget.DailyUSD, day, amount, c.Cfg.Budget.DailyUSD)
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
	state := "settled"
	if !known {
		amount = max(amount, l.reserved)
		state = "unknown"
	}
	if amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		amount = l.reserved
		state = "unknown"
	}
	_, err := l.c.DB.Exec(`UPDATE spend_events SET amount_usd=?,state=? WHERE id=?`, amount, state, l.id)
	return err
}
func (c *Controller) Allowance() float64 {
	if c.Cfg.Budget.ReservationUSD > 0 {
		return c.Cfg.Budget.ReservationUSD
	}
	return 0.60
}
