package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// fixMatchApp is an App with [typesafe] configured and two sets, s and t, each with a finished
// run whose stored fix list has a pricing-page fix (worded differently in each; t's target is
// tTarget) and a docs fix that shares no word with anything in the other set.
func fixMatchApp(t *testing.T, tTarget string) (*App, map[string]string) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "x")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "prompt-sets"), 0o755)
	os.WriteFile(filepath.Join(dir, "cheapseats.toml"), []byte(`[brand]
name = "Cheap Seats"
about = "An AI visibility tracker. It asks assistants buyer questions."
audience = "a software developer"

[[brand.sites]]
url = "cheapseats.dev"
kind = "marketing"

[[answerers]]
id = "claude"
provider = "anthropic"
model = "m"
samples = { search = 1, recall = 1 }

[grader]
model = "g"

[typesafe]
model = "jev-1.14.0"

[pricing.m]
input = 1
output = 1
[pricing.g]
input = 1
output = 1
[pricing."jev-1.14.0"]
input = 0.042
`), 0o644)
	for _, s := range []string{"s", "t"} {
		os.WriteFile(filepath.Join(dir, "prompt-sets", s+".toml"), []byte(`id = "`+s+`"
name = "Set `+s+`"
tier = "core"
[[prompts]]
id = "`+s+`1"
text = "How much does an AI visibility tracker cost in set `+s+`?"
[[prompts]]
id = "`+s+`2"
text = "How do I set up tracking in set `+s+`?"
`), 0o644)
	}
	cfg, err := config.Load(filepath.Join(dir, "cheapseats.toml"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runs := map[string]string{}
	lists := map[string][]analysis.Fix{
		"s": {
			{Rank: 1, Type: analysis.NewPage, Title: "Publish a pricing page", Target: "https://cheapseats.dev/pricing", Claim: "per-seat price and the free tier", DoneWhen: "the page is live", Prompts: []int{1}, Effort: "S"},
			{Rank: 2, Type: analysis.NewPage, Title: "Write a metrics glossary", Target: "https://cheapseats.dev/docs/metrics", Claim: "what each number means", DoneWhen: "glossary live", Prompts: []int{2}, Effort: "S"},
		},
		"t": {
			{Rank: 1, Type: analysis.NewPage, Title: "Add a plans and pricing page with per-seat cost", Target: tTarget, Claim: "plans, seats, and what is free", DoneWhen: "answers quote a price", Prompts: []int{1}, Effort: "M"},
			{Rank: 2, Type: analysis.NewPage, Title: "Document the CLI flags", Target: "https://cheapseats.dev/docs/cli", Claim: "every flag", DoneWhen: "reference live", Prompts: []int{2}, Effort: "S"},
		},
	}
	for _, id := range []string{"s", "t"} {
		set, _ := a.Set(id)
		r, err := a.Runs.Create(ctx, set, "test")
		if err != nil {
			t.Fatal(err)
		}
		for i := range lists[id] {
			f := &lists[id][i]
			f.Verification = analysis.Verification{Status: "verified", Sources: []string{f.Target}}
			for _, gate := range []string{"existing_work", "remaining_change", "capability", "strategy", "eligibility", "completion"} {
				source := f.Target
				quote := "Fixture evidence was independently checked."
				if gate == "strategy" {
					source = "brand.advisory"
				}
				if gate == "completion" {
					source, quote = "candidate.done_when", f.DoneWhen
				}
				f.Verification.Checks = append(f.Verification.Checks, analysis.VerificationCheck{Gate: gate, Source: source, Quote: quote, Reason: "Fixture verification for this candidate."})
			}
			f.Verification.Context = analysis.VerificationContext(cfg, f.Target, f.Verification)
		}
		fj := analysis.Encode(analysis.Analysis{Fixes: lists[id]})
		if _, err := db.Exec(`UPDATE runs SET status='done', finished_at=?, fixes=?, fixes_at=? WHERE id=?`, store.Now(), fj, store.Now(), r.ID); err != nil {
			t.Fatal(err)
		}
		runs[id] = r.ID
	}
	return a, runs
}

// fakeJevMatch answers every noul with p and records the requests.
func fakeJevMatch(t *testing.T, p float64) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var asked []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req)
		answers := map[string]any{}
		for q := range req["questions"].(map[string]any) {
			answers[q] = map[string]any{"type": "noul", "noul": p}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": answers, "usage": map[string]any{"input_tokens": 400, "output_tokens": 0}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, srv.URL)
	return srv, &asked
}

func TestMatchFixesFoldsTwoSetsPricingPagesIntoOneCard(t *testing.T) {
	a, runs := fixMatchApp(t, "https://cheapseats.dev/plans")
	_, asked := fakeJevMatch(t, 0.9)
	ctx := context.Background()

	before, _ := a.QuickWins(ctx)
	if len(before) != 4 {
		t.Fatalf("before the pass, four cards: %d", len(before))
	}

	n, err := a.MatchFixes(ctx, runs["s"])
	if err != nil {
		t.Fatal(err)
	}
	// Only the pricing pair shares a word ("pricing"); the docs fixes share nothing with each
	// other or with the pricing fixes, so they were never asked about.
	if n != 1 || len(*asked) != 1 {
		t.Fatalf("decided %d pairs in %d requests", n, len(*asked))
	}
	req := (*asked)[0]
	st := req["state"].(map[string]any)
	if !strings.HasPrefix(st["category"].(string), "An AI visibility tracker.") {
		t.Errorf("state should carry the category: %v", st["category"])
	}
	fa, fb := st["a"].(map[string]any), st["b"].(map[string]any)
	titles := fa["title"].(string) + " / " + fb["title"].(string)
	if !strings.Contains(titles, "Publish a pricing page") || !strings.Contains(titles, "Add a plans and pricing page") {
		t.Errorf("both fixes in the state: %s", titles)
	}
	for _, f := range []map[string]any{fa, fb} {
		for _, k := range []string{"title", "target", "claim", "done_when"} {
			if f[k] == "" || f[k] == nil {
				t.Errorf("fix state lacks %s: %v", k, f)
			}
		}
	}
	qs := req["questions"].(map[string]any)
	q, ok := qs["same_page"].(map[string]any)
	if len(qs) != 1 || !ok || q["type"] != "noul" || !strings.Contains(q["instructions"].(string), "same page") {
		t.Errorf("one noul question about the same page: %v", qs)
	}

	matches, _ := a.FixMatches(ctx)
	if len(matches) != 1 || matches[0].Source != "jev" || matches[0].Confidence != 0.9 || !matches[0].Matches {
		t.Fatalf("stored: %+v", matches)
	}
	if matches[0].FixA >= matches[0].FixB {
		t.Errorf("fix_a is the lexically first signature: %q / %q", matches[0].FixA, matches[0].FixB)
	}

	after, err := a.QuickWins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 3 {
		t.Fatalf("after the pass, three cards: %d", len(after))
	}
	var kept *QuickWin
	for i := range after {
		if len(after[i].AlsoIn) > 0 {
			kept = &after[i]
		}
	}
	if kept == nil || !strings.Contains(kept.Title, "pricing") {
		t.Fatalf("the pricing card carries the other set: %+v", after)
	}
	// The kept card is the one the existing sort put first (s's one-day fix beats t's week), and
	// its prompts are still its own set's.
	if kept.SetID != "s" || kept.Rank != 1 || len(kept.Prompts) != 1 || kept.Prompts[0] != 1 {
		t.Errorf("kept %s#%d prompts %v", kept.SetID, kept.Rank, kept.Prompts)
	}
	also := kept.AlsoIn[0]
	if len(kept.AlsoIn) != 1 || also.SetID != "t" || also.SetName != "Set t" || also.RunID != runs["t"] || also.Rank != 1 || also.Confidence != 0.9 {
		t.Errorf("also in: %+v", kept.AlsoIn)
	}
	for _, q := range after {
		if q.SetID == "t" && q.Rank == 1 {
			t.Errorf("the merged card should be gone from the list")
		}
	}
	// Filtering by the other set includes the card it is also asking for.
	if got := a.FilterQuickWins(after, QuickWinFilter{Set: "t"}); len(got) != 2 {
		t.Errorf("set t's filter: %d cards", len(got))
	}
	// Asking again asks nothing: the pair is decided.
	if n, err := a.MatchFixes(ctx, runs["t"]); err != nil || n != 0 || len(*asked) != 1 {
		t.Errorf("second pass decided %d, %d requests (%v)", n, len(*asked), err)
	}
	// A stricter threshold reads the same decision differently, without asking.
	a.Cfg.TypeSafe.MinConfidence = 0.95
	if split, _ := a.QuickWins(ctx); len(split) != 4 || len(*asked) != 1 {
		t.Errorf("below the threshold the cards split again: %d cards, %d requests", len(split), len(*asked))
	}
	var events int
	a.DB.QueryRow(`SELECT COUNT(*) FROM spend_events WHERE kind LIKE 'provider-attempt/fix-matches/%'`).Scan(&events)
	if events == 0 {
		t.Error("the request should be in the ledger")
	}
}

func TestMatchFixesEqualTargetsMatchByRuleWithoutAsking(t *testing.T) {
	a, runs := fixMatchApp(t, "https://www.cheapseats.dev/pricing/")
	_, asked := fakeJevMatch(t, 0.1)
	ctx := context.Background()
	n, err := a.MatchFixes(ctx, runs["s"])
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(*asked) != 0 {
		t.Fatalf("decided %d pairs in %d requests; equal targets need no question", n, len(*asked))
	}
	matches, _ := a.FixMatches(ctx)
	if len(matches) != 1 || matches[0].Source != "rule" || matches[0].Confidence != 1 {
		t.Fatalf("stored: %+v", matches)
	}
	qw, _ := a.QuickWins(ctx)
	if len(qw) != 3 {
		t.Errorf("three cards: %d", len(qw))
	}
}

func TestMatchFixesBelowTheThresholdKeepsTheCardsApart(t *testing.T) {
	a, runs := fixMatchApp(t, "https://cheapseats.dev/plans")
	_, asked := fakeJevMatch(t, 0.3)
	ctx := context.Background()
	if n, err := a.MatchFixes(ctx, runs["s"]); err != nil || n != 1 || len(*asked) != 1 {
		t.Fatalf("decided %d (%v)", n, err)
	}
	qw, _ := a.QuickWins(ctx)
	if len(qw) != 4 {
		t.Errorf("a no from Jev leaves four cards: %d", len(qw))
	}
	for _, q := range qw {
		if len(q.AlsoIn) != 0 {
			t.Errorf("nothing folded: %+v", q.AlsoIn)
		}
	}
	matches, _ := a.FixMatches(ctx)
	if len(matches) != 1 || matches[0].Matches {
		t.Errorf("stored below the threshold: %+v", matches)
	}
}
