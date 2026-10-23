package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// providersApp is an App over a real database with the optional providers configured, and one
// finished run whose grades name "Scrunch" and "Scrunch AI" as two products.
func providersApp(t *testing.T) (*App, string) {
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

[[competitors]]
name = "Profound"
aliases = ["tryprofound"]

[typesafe]
model = "jev-1.14.0"

[exa]
results = 2

[pricing.m]
input = 1
output = 1
[pricing.g]
input = 1
output = 1
[pricing."jev-1.14.0"]
input = 0.042
[pricing.exa-search]
search_per_1k = 4
`), 0o644)
	os.WriteFile(filepath.Join(dir, "prompt-sets", "s.toml"), []byte(`id = "s"
name = "S"
tier = "core"
[[prompts]]
id = "p1"
text = "What's the best AI visibility tool?"
[[prompts]]
id = "p2"
text = "How do I measure share of voice?"
`), 0o644)
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
	set, _ := a.Set("s")
	r, err := a.Runs.Create(context.Background(), set, "test")
	if err != nil {
		t.Fatal(err)
	}
	// Finish its results by hand: prompt 1's answers name Scrunch two ways; prompt 2 cites our page.
	finish := func(idx int, track string, named []string, top string, text string, cited string) {
		g := grade.Grade{Verdict: grade.Absent, Named: named}
		if top != "" {
			g.TopPick = &top
		}
		ans := answer.Answer{Text: text}
		if cited != "" {
			ans.Citations = []answer.Link{{URL: cited}}
			ans.Searches = []answer.Query{{Query: "q", Completed: true, Results: []answer.Link{{URL: cited}, {URL: "https://cheapseats.dev/other"}}}}
		}
		gj, _ := json.Marshal(g)
		aj, _ := json.Marshal(ans)
		if _, err := db.Exec(`UPDATE results SET status='done', grade=?, answer=?, verdict=?, finished_at=? WHERE run_id=? AND prompt_idx=? AND track=?`, string(gj), string(aj), "absent", store.Now(), r.ID, idx, track); err != nil {
			t.Fatal(err)
		}
	}
	finish(0, "search", []string{"Scrunch AI", "Profound"}, "Scrunch AI", "Scrunch AI is a strong pick. Profound is the enterprise option.", "")
	finish(0, "recall", []string{"Scrunch", "tryprofound"}, "Scrunch", "I'd start with Scrunch, which tracks AI answers.", "")
	finish(1, "search", []string{"Peec AI"}, "", "Peec AI measures share of voice.", "https://cheapseats.dev/docs/metrics/")
	finish(1, "recall", []string{}, "", "Count how often you're named.", "")
	if _, err := db.Exec(`UPDATE runs SET status='done', finished_at=? WHERE id=?`, store.Now(), r.ID); err != nil {
		t.Fatal(err)
	}
	return a, r.ID
}

func TestFoldNamesStoresJevsDecisionAndFoldsTheBoard(t *testing.T) {
	a, id := providersApp(t)
	var asked []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req)
		// Whatever is asked, say the first candidate, confidently.
		answers := map[string]any{}
		for q := range req["questions"].(map[string]any) {
			answers[q] = map[string]any{"type": "choice", "choice": "c0", "confidence": 0.93, "probabilities": map[string]float64{"c0": 0.95, "none": 0.05}}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": answers, "usage": map[string]any{"input_tokens": 500, "output_tokens": 10}})
	}))
	defer srv.Close()
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, srv.URL)

	ctx := context.Background()
	r, _ := a.Runs.Get(ctx, id)
	res, _ := a.Runs.Results(ctx, id)
	before := a.Summary(r, res)
	if rows := voiceRows(before, "scrunch"); rows != 2 {
		t.Fatalf("before folding, Scrunch and Scrunch AI are two rows: %d", rows)
	}

	n, err := a.FoldNames(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// "scrunch" is the only new name with a plausible candidate: "scrunch ai" (shares a word).
	// "peec ai" has no candidate; "tryprofound" is a configured alias; Profound is a competitor.
	if n != 1 || len(asked) != 1 {
		t.Fatalf("asked %d names in %d requests", n, len(asked))
	}
	st := asked[0]["state"].(map[string]any)
	if !strings.HasPrefix(st["category"].(string), "An AI visibility tracker.") {
		t.Errorf("state should carry the category: %v", st["category"])
	}
	names := st["names"].([]any)
	first := names[0].(map[string]any)
	if first["name"] != "Scrunch" || !strings.Contains(first["context"].(string), "Scrunch") {
		t.Errorf("the variant and its sentence: %v", first)
	}
	var decided []NameDecision
	decided, _ = a.NameDecisions(ctx)
	if len(decided) != 1 || decided[0].Variant != "scrunch" || decided[0].Display != "Scrunch AI" || !decided[0].Folds {
		t.Fatalf("decisions %+v", decided)
	}
	if al := a.Aliases(); al["scrunch"] != "Scrunch AI" || al["tryprofound"] != "Profound" {
		t.Errorf("aliases %v", al)
	}
	after := a.Summary(r, res)
	if rows := voiceRows(after, "scrunch"); rows != 1 {
		t.Errorf("after folding, one row: %d", rows)
	}
	for _, v := range after.Voice {
		if v.Name == "Scrunch AI" && (v.Answers != 2 || v.TopPick != 2) {
			t.Errorf("Scrunch AI should have both answers and both top picks: %+v", v)
		}
		if v.Name == "Profound" && v.Answers != 2 {
			t.Errorf("the configured alias should fold too: %+v", v)
		}
	}
	// Asking again asks nothing: the decision is stored.
	if n, err := a.FoldNames(ctx, id); err != nil || n != 0 || len(asked) != 1 {
		t.Errorf("second pass asked %d (%v)", n, err)
	}
	// A stricter threshold reads the same decision differently, without asking.
	a.Cfg.TypeSafe.MinConfidence = 0.99
	a.invalidateAliases()
	if _, ok := a.Aliases()["scrunch"]; ok {
		t.Error("below the threshold the names stay apart")
	}
}

func voiceRows(s interface{ VoiceNames() []string }, contains string) int {
	n := 0
	for _, name := range s.VoiceNames() {
		if strings.Contains(strings.ToLower(name), contains) {
			n++
		}
	}
	return n
}

func TestCheckCoverageStoresExasPagesWithTheirStatus(t *testing.T) {
	a, id := providersApp(t)
	var queries []string
	var domains []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		queries = append(queries, req["query"].(string))
		domains = req["includeDomains"].([]any)
		var results []any
		if strings.Contains(req["query"].(string), "share of voice") {
			results = []any{
				map[string]any{"title": "Metrics", "url": "https://cheapseats.dev/docs/metrics"},
				map[string]any{"title": "Guide", "url": "https://cheapseats.dev/guides/benchmark"},
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results, "costDollars": map[string]any{"total": 0.004}})
	}))
	defer srv.Close()
	t.Setenv(exa.KeyVar, "k")
	t.Setenv(config.ExaBaseURLVar, "")
	a.Exa().URL = "" // the client reads the env; point it at the fake through the proxy variable
	t.Setenv(config.ExaBaseURLVar, srv.URL)

	ctx := context.Background()
	n, err := a.CheckCoverage(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(queries) != 2 || queries[0] != "What's the best AI visibility tool?" {
		t.Fatalf("searched %d: %v", n, queries)
	}
	if len(domains) != 1 || domains[0] != "cheapseats.dev" {
		t.Errorf("restricted to the brand's sites: %v", domains)
	}
	res, _ := a.Runs.Results(ctx, id)
	cov := a.Coverage(ctx, id, res)
	if len(cov) != 2 {
		t.Fatalf("coverage rows %d", len(cov))
	}
	if len(cov[0].Pages) != 0 {
		t.Errorf("prompt 1 has no page of ours: %+v", cov[0].Pages)
	}
	p := cov[1].Pages
	if len(p) != 2 || p[0].Status != "cited" || p[1].Status != "unseen" {
		t.Errorf("prompt 2: metrics was cited (trailing slash and all), the guide never seen: %+v", p)
	}
	if cov[1].CostUSD != 0.004 {
		t.Errorf("cost %f", cov[1].CostUSD)
	}
	// Checked prompts aren't searched again.
	if n, err := a.CheckCoverage(ctx, id); err != nil || n != 0 || len(queries) != 2 {
		t.Errorf("second pass searched %d (%v)", n, err)
	}
	var spent float64
	a.DB.QueryRow(`SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE kind LIKE 'provider-attempt/coverage/%'`).Scan(&spent)
	if fmt.Sprintf("%.3f", spent) != "0.008" {
		t.Errorf("two searches settled in the ledger: %f", spent)
	}
}
