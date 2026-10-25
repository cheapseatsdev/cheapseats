package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fanoutGauge counts work in flight and the most there ever was.
type fanoutGauge struct{ now, peak atomic.Int32 }

func (g *fanoutGauge) enter() {
	n := g.now.Add(1)
	for p := g.peak.Load(); n > p && !g.peak.CompareAndSwap(p, n); p = g.peak.Load() {
	}
}

// fanoutHold waits until want are in flight, then watches for a while that no more start, and
// fails if one does: an unbounded fan-out passes want almost at once.
func fanoutHold(t *testing.T, g *fanoutGauge, want int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for g.now.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("only %d in flight; want %d", g.now.Load(), want)
		}
		runtime.Gosched()
	}
	watch := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(watch) {
		if p := g.peak.Load(); p > want {
			t.Fatalf("%d in flight at once; the bound is %d", p, want)
		}
		runtime.Gosched()
	}
}

func TestEachRunsAFewAtATime(t *testing.T) {
	var g fanoutGauge
	release := make(chan struct{})
	done := make(chan struct{})
	ran := atomic.Int32{}
	go func() {
		each(100, func(int) {
			g.enter()
			<-release
			g.now.Add(-1)
			ran.Add(1)
		})
		close(done)
	}()
	fanoutHold(t, &g, eachWorkers)
	close(release) // on a failure the held goroutines are left; the test binary exits
	<-done
	if ran.Load() != 100 {
		t.Errorf("ran %d of 100", ran.Load())
	}
}

// Five Fetchers of four would put 20 requests in flight; the process-wide slots hold it to 16.
func TestFetchersShareOneLimit(t *testing.T) {
	var g fanoutGauge
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.enter()
		<-release
		g.now.Add(-1)
	}))
	defer srv.Close()
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	defer open() // before srv.Close, so a failing test doesn't hang on held handlers
	var wg sync.WaitGroup
	for f := 0; f < 5; f++ {
		fetcher := NewFetcher(srv.Client(), 4)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := fetcher.Get(context.Background(), srv.URL); err != nil {
					t.Error(err)
				}
			}()
		}
	}
	fanoutHold(t, &g, 16)
	open()
	wg.Wait()
}
