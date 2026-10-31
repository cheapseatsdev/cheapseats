package budget

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

func TestGroupBudgetSurvivesOperationScopesAndConcurrentControllers(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := &config.Config{Budget: config.Budget{DailyUSD: 10, Concurrency: 8}}
	controllers := []*Controller{New(cfg, db), New(cfg, db)}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			group, e := controllers[i%2].WithinKinds([]string{"check-a", "check-b"}, 1)
			if e != nil {
				t.Error(e)
				return
			}
			kind := "check-a"
			if i%2 == 0 {
				kind = "check-b"
			}
			scoped, e := group.Scoped(kind, 5)
			if e != nil {
				t.Error(e)
				return
			}
			lease, e := scoped.Reserve(context.Background(), kind, "model", .6)
			if e == nil {
				admitted.Add(1)
				lease.Finish(0, false)
			} else if !errors.Is(e, ErrExhausted) {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("shared group admitted %d reservations", admitted.Load())
	}
	group, _ := controllers[0].WithinKinds([]string{"check-a", "check-b"}, 1)
	if _, err := group.Reserve(context.Background(), "outside", "model", .1); err == nil {
		t.Fatal("operation escaped its group")
	}
	spent, err := group.SpentKinds(context.Background(), []string{"check-a", "check-b"})
	if err != nil || spent != .6 {
		t.Fatalf("historical/reserved spend lost: %f %v", spent, err)
	}
	// Per-check limits still apply under a larger group ceiling.
	large, _ := controllers[0].WithinKinds([]string{"child"}, 5)
	child, _ := large.Scoped("child", .5)
	if _, err := child.Reserve(context.Background(), "child", "model", .6); !errors.Is(err, ErrExhausted) {
		t.Fatalf("group replaced per-check limit: %v", err)
	}
}
