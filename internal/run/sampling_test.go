package run

import (
	"context"
	"testing"

	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/sets"
	"github.com/metcalfc/popcorn/internal/store"
)

// Extras go where the named rate is least certain: a cell with no history before one near 0.5,
// that before one named every time, never more than doubling a cell, and only while they fit in
// the money left.
func TestAllocate(t *testing.T) {
	cells := func() []Cell {
		return []Cell{
			{PromptID: "always", Base: 2, PriorN: 40, PriorNamed: 40},
			{PromptID: "half", Base: 2, PriorN: 40, PriorNamed: 20},
			{PromptID: "new", Base: 1},
			{PromptID: "off", Base: 0},
		}
	}
	free := []float64{0, 0, 0, 0}
	got := allocate(cells(), free, 3, 1)
	if got[2].Extra != 1 || got[1].Extra != 2 || got[0].Extra != 0 || got[3].Extra != 0 {
		t.Fatalf("%+v", got)
	}
	// Plenty to give: each uncertain cell at most doubles, and then the near-certain one gets the rest.
	got = allocate(cells(), free, 10, 1)
	if got[2].Extra != 1 || got[1].Extra != 2 || got[0].Extra != 2 || got[3].Extra != 0 {
		t.Fatalf("%+v", got)
	}
	// Money for one paid extra: it goes to the most uncertain cell it can pay for.
	got = allocate(cells(), []float64{0.1, 0.1, 0.5, 0.1}, 3, 0.15)
	if got[2].Extra != 0 || got[1].Extra != 1 || got[0].Extra != 0 {
		t.Fatalf("%+v", got)
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
	// One recall sample says least, so each prompt's recall cell gets one more, the new prompt's
	// (no history) first; search, with two samples already, gets none.
	if sp.Extra("p3", "a", answer.Recall) != 1 || sp.Extra("p1", "a", answer.Recall) != 1 || sp.Extra("p2", "a", answer.Recall) != 1 || sp.Extra("p3", "a", answer.Search) != 0 {
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
