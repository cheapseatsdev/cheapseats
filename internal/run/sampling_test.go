package run

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/sets"
	"github.com/metcalfc/popcorn/internal/store"
)

// Whole prompts are doubled, the one whose rate doubling makes most certain first: a prompt with
// no history before one near 0.5, that before one named every time, while its answers fit in what's
// left of the limit and its cost in the money.
func TestAllocate(t *testing.T) {
	prompts := []promptCells{
		{id: "always", cells: []Cell{{Base: 2, PriorN: 40, PriorNamed: 40}}, cost: 0.2},
		{id: "half", cells: []Cell{{Base: 2, PriorN: 40, PriorNamed: 20}, {Base: 1, PriorN: 20, PriorNamed: 10}}, cost: 0.3},
		{id: "new", cells: []Cell{{Base: 1}}, cost: 0.5},
		{id: "off"},
	}
	ids := func(ps []promptCells) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.id)
		}
		return strings.Join(out, ",")
	}
	free := math.Inf(1)
	for limit, want := range map[int]string{4: "half,new", 3: "always,new", 1: "new", 0: "", 100: "always,half,new"} {
		if got := ids(allocate(prompts, limit, free)); got != want {
			t.Errorf("limit %d: %s, want %s", limit, got, want)
		}
	}
	if got := ids(allocate(prompts, 10, 0.4)); got != "half" {
		t.Errorf("money for one: %s", got)
	}
	if _, lo, hi := wilson(0, 0); lo != 0 || hi != 1 {
		t.Fatal("no history is the widest interval")
	}
	if _, lo, hi := wilson(20, 40); hi-lo > 0.31 || hi-lo < 0.29 {
		t.Fatalf("20/40: width %v", hi-lo)
	}
}

// With adaptive sampling on, a run's extras are decided from earlier runs of the same method and
// recorded in its snapshot, and neither its method nor its design changes.
func TestAdaptiveSampling(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1] // 2 search + 1 recall a prompt
	ctx := context.Background()
	first, err := e.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(ctx, first.ID); err != nil {
		t.Fatal(err)
	}

	e.Cfg.Sampling.Adaptive, e.Cfg.Sampling.MaxExtra = true, 3
	grown := set
	grown.Prompts = append(append([]sets.Prompt{}, set.Prompts...), sets.Prompt{ID: "p3", Text: "p3"})
	r, err := e.Create(ctx, grown, "manual")
	if err != nil {
		t.Fatal(err)
	}
	sp := r.Snapshot.Sampling
	if sp == nil || sp.PriorRuns != 1 || sp.PriorAnswers != 6 {
		t.Fatalf("%+v", sp)
	}
	// The new prompt has no history, so it's doubled: every one of its cells gets its planned
	// samples again, and the three extras are spent.
	if strings.Join(sp.Prompts, ",") != "p3" || sp.Extra("p3", "a", answer.Search) != 2 || sp.Extra("p3", "a", answer.Recall) != 1 || sp.Extra("p1", "a", answer.Recall) != 0 {
		t.Fatalf("%+v", sp.Cells)
	}
	var n int
	e.DB.QueryRow(`SELECT COUNT(*) FROM results WHERE run_id=?`, r.ID).Scan(&n)
	if n != 3*3+3 {
		t.Fatalf("%d results", n)
	}
	got, _ := e.Get(ctx, r.ID)
	if got.Snapshot.Sampling == nil || got.Method != first.Method {
		t.Fatalf("the snapshot keeps the extras and the method is unchanged: %+v", got)
	}
	plain := r.Snapshot
	plain.Sampling = nil
	if Design(r.Snapshot) != Design(plain) {
		t.Fatal("extras changed the design")
	}
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Get(ctx, r.ID); got.Status != "done" {
		t.Fatalf("status %s", got.Status)
	}

	// With today's budget all but spent, paid extras don't fit and none are planned.
	e.Cfg.Budget.DailyUSD = 1
	e.DB.Exec(`INSERT INTO spend_events (created_at,kind,model,amount_usd,state) VALUES (?,'test','m',0.99,'settled')`, store.Now())
	r, err = e.Create(ctx, grown, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if r.Snapshot.Sampling == nil || len(r.Snapshot.Sampling.Cells) != 0 {
		t.Fatalf("%+v", r.Snapshot.Sampling)
	}
}

// A finished run gets its theme pass, stored with it; [grader] themes = false leaves it out.
func TestThemePassAfterARun(t *testing.T) {
	e, set, calls := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	r, _ := e.Create(ctx, set, "manual")
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	th, err := e.Themes(ctx, r.ID)
	// The fake grader's one objection is too little to group, so no call was made.
	if err != nil || th == nil || th.Skipped == "" || th.Inputs != 1 || calls.Load() != 12 {
		t.Fatalf("%+v %v calls=%d", th, err, calls.Load())
	}
	if rep := e.Opinion(ctx, r.ID, nil); rep.Themes == nil || rep.Sentiment.Named != 0 {
		t.Fatalf("%+v", rep)
	}
	off := false
	e.Cfg.Grader.Themes = &off
	r, _ = e.Create(ctx, set, "manual")
	e.Execute(ctx, r.ID)
	if th, err := e.Themes(ctx, r.ID); th != nil || err != nil {
		t.Fatalf("%+v %v", th, err)
	}
}

// Extras that fail once every planned answer is in are dropped, and the run is done, so it counts
// in rankings and trends and gets its fix list.
func TestFailedExtrasLeaveTheRunDone(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	first, _ := e.Create(ctx, set, "manual")
	if err := e.Execute(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	// p3's first three answers (its planned ones: extras go last) work, the rest fail.
	fake, _ := url.Parse(llm.AnthropicURL)
	proxy := httputil.NewSingleHostReverseProxy(fake)
	var p3 atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		if !strings.Contains(string(body), "output_config") && strings.Contains(string(body), `"content":"p3"`) && p3.Add(1) > 3 {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"no"}}`))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()
	llm.AnthropicURL = srv.URL
	e.Cfg.Budget.Concurrency = 1
	e.Cfg.Sampling.Adaptive, e.Cfg.Sampling.MaxExtra = true, 3
	grown := set
	grown.Prompts = append(append([]sets.Prompt{}, set.Prompts...), sets.Prompt{ID: "p3", Text: "p3"})
	r, err := e.Create(ctx, grown, "manual")
	if err != nil || len(r.Snapshot.Sampling.Prompts) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Get(ctx, r.ID)
	res, _ := e.Results(ctx, r.ID)
	if got.Status != "done" || len(res) != 9 || p3.Load() < 6 {
		t.Fatalf("status %s, %d results, %d p3 answers: %v", got.Status, len(res), p3.Load(), got.Error)
	}
}
