package budget

import (
	"context"
	"errors"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"sync"
	"sync/atomic"
	"testing"
)

func TestScopedBudgetIsAtomicAcrossProcessesAndKeepsGlobalLimit(t *testing.T) {
	dir := t.TempDir()
	a, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	cfg := &config.Config{Budget: config.Budget{DailyUSD: 1.5, Concurrency: 8}}
	one, _ := New(cfg, a).Scoped("test-operation", 1)
	two, _ := New(cfg, b).Scoped("test-operation", 1)
	var wg sync.WaitGroup
	var n atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := one
			if i%2 != 0 {
				c = two
			}
			lease, err := c.Reserve(context.Background(), "test-operation", "m", .6)
			if err == nil {
				n.Add(1)
				lease.Finish(0, false)
			} else if !errors.Is(err, ErrExhausted) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("scope admitted %d reservations", n.Load())
	}
	if _, err := one.Reserve(context.Background(), "wrong-operation", "m", .1); err == nil {
		t.Fatal("operation escaped its budget")
	}
	global := New(cfg, a)
	lease, err := global.Reserve(context.Background(), "another-operation", "m", .6)
	if err != nil {
		t.Fatal(err)
	}
	lease.Finish(0, false)
	third, _ := global.Scoped("third-operation", 10)
	if _, err := third.Reserve(context.Background(), "third-operation", "m", .6); !errors.Is(err, ErrExhausted) {
		t.Fatalf("scope escaped daily budget: %v", err)
	}
}
