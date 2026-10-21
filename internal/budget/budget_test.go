package budget

import (
	"context"
	"errors"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/store"
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentProcessesCannotReserveSameBalance(t *testing.T) {
	dir := t.TempDir()
	d1, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer d1.Close()
	d2, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer d2.Close()
	c := &config.Config{Budget: config.Budget{DailyUSD: 1, Concurrency: 20}}
	a, b := New(c, d1), New(c, d2)
	var wg sync.WaitGroup
	var admitted atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctrl := a
			if i%2 == 0 {
				ctrl = b
			}
			l, err := ctrl.Reserve(context.Background(), "test", "fake", .6)
			if err == nil {
				admitted.Add(1)
				if err = l.Finish(0, false); err != nil {
					t.Error(err)
				}
			} else if !errors.Is(err, ErrExhausted) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("admitted %d", admitted.Load())
	}
	spent, _ := d1.SpentToday(context.Background())
	if spent != .6 {
		t.Fatal(spent)
	}
}
func TestSettlementCancellationAndInvalidUsage(t *testing.T) {
	d, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	c := New(&config.Config{}, d)
	ctx, cancel := context.WithCancel(context.Background())
	l, e := c.Reserve(ctx, "test", "fake", .6)
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	if e = l.Finish(.1, true); e != nil {
		t.Fatal(e)
	}
	spent, _ := d.SpentToday(context.Background())
	if spent != .1 {
		t.Fatal(spent)
	}
	l, e = c.Reserve(context.Background(), "test", "fake", .6)
	if e != nil {
		t.Fatal(e)
	}
	if e = l.Finish(math.NaN(), true); e != nil {
		t.Fatal(e)
	}
	spent, _ = d.SpentToday(context.Background())
	if math.Abs(spent-.7) > .00001 {
		t.Fatal(spent)
	}
}
