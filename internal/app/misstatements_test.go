package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

func TestKeepMisstatementsValidates(t *testing.T) {
	facts := []string{"Cheap Seats is MIT licensed.", "It runs as one Docker container."}
	text := "Cheap Seats is a **hosted SaaS** at $49 a month. It is MIT licensed."
	raw := []rawMisstatement{
		{Fact: 2, Claim: "Says it's hosted.", Quote: "Cheap Seats is a hosted SaaS at $49 a month"}, // markdown folded away: kept
		{Fact: 2, Claim: "Again.", Quote: "hosted SaaS"},                                            // the same fact twice: one row
		{Fact: 1, Claim: "Not in the answer.", Quote: "Cheap Seats is GPL licensed"},                // quote the answer doesn't have: dropped
		{Fact: 3, Claim: "No such fact.", Quote: "hosted SaaS"},                                     // fact off the list: dropped
		{Fact: 0, Claim: "Zero.", Quote: "hosted SaaS"},
	}
	got := keepMisstatements(text, facts, raw)
	if len(got) != 1 || got[0].FactIdx != 1 || got[0].Fact != facts[1] || got[0].Quote != "Cheap Seats is a **hosted SaaS** at $49 a month" {
		t.Fatalf("kept %+v", got)
	}
}

func TestFactsHashChangesWithTheList(t *testing.T) {
	a := FactsHash([]string{"a", "b"})
	if a != FactsHash([]string{" a ", "b"}) {
		t.Error("whitespace around a fact shouldn't change the hash")
	}
	for _, other := range [][]string{{"a"}, {"b", "a"}, {"a", "b", "c"}, {"a", "c"}, {"ab"}} {
		if FactsHash(other) == a {
			t.Errorf("%q hashes like [a b]", other)
		}
	}
}

// A pass over a graded run: named answers are checked once per facts list, what the model wrote is
// validated, a namesake and an absent answer are never sent, and changing the facts checks again.
func TestCheckFactsOncePerFactsList(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv(config.AnthropicBaseURLVar, "")
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gradedRun(t, db, "r-1", "2026-10-22", "rubric") // two mentioned answers, "Use Cheap Seats."
	// A namesake (other_product) and an absent answer, which the pass must not read.
	for _, g := range []map[string]any{
		{"verdict": "mentioned", "named": []string{"Cheap Seats"}, "other_product": true},
		{"verdict": "absent", "named": []string{"Gauge"}},
	} {
		ans, _ := json.Marshal(answer.Answer{Text: "Use Cheap Seats."})
		js, _ := json.Marshal(g)
		if _, err := db.Exec(`INSERT INTO results (run_id,prompt_idx,prompt,prompt_id,prompt_sha,answerer,model,track,sample,status,answer,grade,verdict,finished_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"r-1", 0, "Which tool?", "p", "", "claude", "m", "search", len(g), "done", string(ans), string(js), g["verdict"], "2026-10-22T12:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Brand: config.Brand{Name: "Cheap Seats", Facts: []string{"Cheap Seats is free.", "It is self-hosted."}}, Grader: config.Model{Model: "m"}}
	a := &App{Cfg: cfg, DB: db, Runs: run.New(cfg, db)}

	calls := 0
	var lastInput string
	old := factCheck
	defer func() { factCheck = old }()
	factCheck = func(ctx context.Context, cfg *config.Config, user string) ([]rawMisstatement, float64, error) {
		calls++
		lastInput = user
		return []rawMisstatement{
			{Fact: 2, Claim: "Calls it hosted.", Quote: "Use Cheap Seats"},
			{Fact: 5, Claim: "Off the list.", Quote: "Use Cheap Seats"},
			{Fact: 1, Claim: "Not in the answer.", Quote: "costs money"},
		}, 0.001, nil
	}
	ctx := context.Background()
	n, err := a.CheckFacts(ctx, "r-1")
	if err != nil || n != 2 || calls != 2 {
		t.Fatalf("checked %d (%d calls) %v: the two named answers, not the namesake or the absent one", n, calls, err)
	}
	for _, want := range []string{"Brand: Cheap Seats", "1. Cheap Seats is free.", "2. It is self-hosted.", "<answer>\nUse Cheap Seats.\n</answer>", "<question>\nWhich tool?\n</question>"} {
		if !strings.Contains(lastInput, want) {
			t.Errorf("input lacks %q:\n%s", want, lastInput)
		}
	}
	results, _ := a.Runs.Results(ctx, "r-1")
	rep := a.Misstatements(ctx, "r-1", results)
	if rep == nil || rep.Checked != 2 || rep.Stale != 0 || len(rep.Facts) != 1 || rep.Facts[0].Idx != 1 || rep.Facts[0].Answers != 2 || len(rep.ByResult) != 2 || rep.ByPrompt[0] != 2 {
		t.Fatalf("report %+v", rep)
	}
	if m := rep.Facts[0].Items[0]; m.Quote != "Use Cheap Seats" || m.Claim != "Calls it hosted." || m.Fact != "It is self-hosted." || m.Answerer != "claude" {
		t.Errorf("item %+v", m)
	}
	// Again: nothing to do.
	if n, err := a.CheckFacts(ctx, "r-1"); err != nil || n != 0 || calls != 2 {
		t.Fatalf("second pass checked %d (%d calls) %v", n, calls, err)
	}
	// The analyst's evidence names the fact, the prompt and the quote.
	lines := a.misstatementLines(ctx, "r-1", results)
	if len(lines) != 1 || !strings.Contains(lines[0], `Fact 2 ("It is self-hosted.") is contradicted by 2 answers, on prompts 1`) || !strings.Contains(lines[0], `"Use Cheap Seats"`) {
		t.Errorf("lines %q", lines)
	}
	// A changed facts list: the report says the checks are stale, and the next pass redoes them.
	cfg.Brand.Facts = []string{"Cheap Seats is free."}
	if rep := a.Misstatements(ctx, "r-1", results); rep == nil || rep.Stale != 2 {
		t.Fatalf("stale %+v", rep)
	}
	factCheck = func(ctx context.Context, cfg *config.Config, user string) ([]rawMisstatement, float64, error) {
		calls++
		return nil, 0, nil
	}
	if n, err := a.CheckFacts(ctx, "r-1"); err != nil || n != 2 || calls != 4 {
		t.Fatalf("after a facts change checked %d (%d calls) %v", n, calls, err)
	}
	if rep := a.Misstatements(ctx, "r-1", results); rep == nil || rep.Checked != 2 || rep.Stale != 0 || len(rep.Facts) != 0 || len(rep.ByResult) != 0 {
		t.Fatalf("after re-check %+v", rep)
	}
	if lines := a.misstatementLines(ctx, "r-1", results); lines != nil {
		t.Errorf("lines after re-check %q", lines)
	}
	// No facts: the pass refuses rather than checking against nothing.
	cfg.Brand.Facts = nil
	if _, err := a.CheckFacts(ctx, "r-1"); err == nil || !strings.Contains(err.Error(), "facts") {
		t.Errorf("no facts: %v", err)
	}
	if a.Misstatements(ctx, "r-2", nil) != nil {
		t.Error("a run never checked has no report")
	}
}

func TestMisstatementsPassIsRegisteredOnTheGrader(t *testing.T) {
	p, ok := PassByCommand("misstatements")
	if !ok || p.Provider != config.GraderProvider || p.Order >= 100 || p.Name != "misstatements" {
		t.Fatalf("%+v %v", p, ok)
	}
	off := false
	for _, c := range []struct {
		cfg  config.Config
		want bool
	}{
		{config.Config{}, false},
		{config.Config{Brand: config.Brand{Facts: []string{"f"}}}, true},
		{config.Config{Brand: config.Brand{Facts: []string{"f"}}, Grader: config.Model{CheckFacts: &off}}, false},
	} {
		if got := p.On(&c.cfg); got != c.want {
			t.Errorf("on with facts %v, check_facts %v: %v", c.cfg.Brand.Facts, c.cfg.Grader.CheckFacts, got)
		}
	}
	a := &App{Cfg: &config.Config{Grader: config.Model{Provider: config.ClaudeCode}}}
	if a.provider(p) != config.ClaudeCode {
		t.Error("a grader pass is called through whatever the grader is")
	}
}
