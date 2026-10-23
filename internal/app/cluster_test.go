package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/opinion"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// clusterApp is providersApp with grades that name the brand and object and praise in several
// wordings: "coverage unverified" twice and once more at length, "easy setup" twice and once at
// length, and one unrelated objection and praise each.
func clusterApp(t *testing.T) (*App, string) {
	t.Helper()
	a, id := providersApp(t)
	regrade := func(idx int, track string, reasoning string, objections, praise []string) {
		g := grade.Grade{Verdict: grade.Mentioned, Reasoning: reasoning, Objections: objections, Praise: praise, Tone: grade.Mixed, Named: []string{"Cheap Seats"}}
		gj, _ := json.Marshal(g)
		if _, err := a.DB.Exec(`UPDATE results SET grade=?, verdict='mentioned' WHERE run_id=? AND prompt_idx=? AND track=?`, string(gj), id, idx, track); err != nil {
			t.Fatal(err)
		}
	}
	regrade(0, "search", "It names Cheap Seats. It says the coverage is unverified.", []string{"Coverage unverified"}, []string{"Easy setup"})
	regrade(0, "recall", "Named as an option. Coverage is unverified.", []string{"coverage unverified."}, []string{"easy setup"})
	regrade(1, "search", "Named but doubted. The coverage of ChatGPT and Claude is unverified.", []string{"Coverage of ChatGPT and Claude unverified"}, []string{"Setup is easy and quick"})
	regrade(1, "recall", "Named with a caveat about price.", []string{"No free tier"}, []string{"Cheap"})
	return a, id
}

// fakeJevCluster answers every choice with the first candidate at p, and records the requests.
func fakeJevCluster(t *testing.T, p float64) *[]map[string]any {
	t.Helper()
	var asked []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req)
		answers := map[string]any{}
		for q := range req["questions"].(map[string]any) {
			answers[q] = map[string]any{"type": "choice", "choice": "c0", "confidence": 0.6, "probabilities": map[string]float64{"c0": p, "none": 1 - p}}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": answers, "usage": map[string]any{"input_tokens": 400, "output_tokens": 10}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, srv.URL)
	return &asked
}

func objectionRows(s report.Summary) map[string]report.Objection {
	out := map[string]report.Objection{}
	for _, o := range s.Objections {
		out[o.Text] = o
	}
	return out
}

// praiseRows keys the praise by its folded text: which wording of a cluster is displayed is the
// first one the results list, and the results' order isn't the point here.
func praiseRows(s opinion.Sentiment) map[string]int {
	out := map[string]int{}
	for _, c := range s.Praise {
		out[grade.Fold(c.Text)] = c.N
	}
	return out
}

// same reports whether two wordings fold to the same phrase.
func same(a, b string) bool { return grade.Fold(a) == grade.Fold(b) }

func TestClusterPhrasesCountsWordingsAsOne(t *testing.T) {
	a, id := clusterApp(t)
	asked := fakeJevCluster(t, 0.9)
	ctx := context.Background()
	r, _ := a.Runs.Get(ctx, id)
	res, _ := a.Runs.Results(ctx, id)

	// Before the pass, every wording is its own row.
	if rows := objectionRows(a.Summary(r, res)); len(rows) != 3 || rows["coverage unverified"].Answers != 2 || rows["coverage unverified"].Wordings != 1 {
		t.Fatalf("before: %+v", rows)
	}
	if p := praiseRows(a.Opinion(ctx, id, res).Sentiment); len(p) != 3 || p["easy setup"] != 2 {
		t.Fatalf("before: %+v", p)
	}

	n, err := a.ClusterPhrases(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// One question per kind: the long wording about the established one. "no free tier" and
	// "cheap" share no word with anything, so they are their own clusters without a question.
	if n != 2 || len(*asked) != 2 {
		t.Fatalf("asked %d wordings in %d requests", n, len(*asked))
	}
	byKind := map[string]map[string]any{}
	for _, req := range *asked {
		st := req["state"].(map[string]any)
		byKind[st["kind"].(string)] = st
		if !strings.HasPrefix(st["category"].(string), "An AI visibility tracker.") {
			t.Errorf("state should carry the category: %v", st["category"])
		}
		cands := st["candidates"].([]any)
		if len(cands) != 1 {
			t.Errorf("only the plausible candidate: %v", cands)
		}
		q := req["questions"].(map[string]any)["q"].(map[string]any)
		crit := q["criteria"].(map[string]any)
		if _, ok := crit["none"]; !ok || len(crit) != 2 {
			t.Errorf("criteria: one per candidate and none: %v", crit)
		}
		if !strings.Contains(q["instructions"].(string), "says the same thing") {
			t.Errorf("instructions: %v", q["instructions"])
		}
	}
	if st := byKind["objection"]; st == nil || st["phrase"] != "Coverage of ChatGPT and Claude unverified" ||
		!same(st["candidates"].([]any)[0].(map[string]any)["phrase"].(string), "coverage unverified") || !strings.Contains(st["context"].(string), "ChatGPT and Claude") {
		t.Errorf("objection request: %v", st)
	}
	if st := byKind["praise"]; st == nil || st["phrase"] != "Setup is easy and quick" || !same(st["candidates"].([]any)[0].(map[string]any)["phrase"].(string), "easy setup") {
		t.Errorf("praise request: %v", st)
	}

	// After: one row under the established wording, with both wordings counted.
	rows := objectionRows(a.Summary(r, res))
	if len(rows) != 2 || rows["coverage unverified"].Answers != 3 || rows["coverage unverified"].Wordings != 2 || rows["no free tier"].Answers != 1 || rows["no free tier"].Wordings != 1 {
		t.Fatalf("after: %+v", rows)
	}
	if p := praiseRows(a.Opinion(ctx, id, res).Sentiment); len(p) != 2 || p["easy setup"] != 3 || p["cheap"] != 1 {
		t.Fatalf("praise after: %+v", p)
	}
	// What was stored: the probability of the chosen cluster, and the unasked wordings as their
	// own clusters by rule.
	decisions, _ := a.PhraseDecisions(ctx)
	got := map[string]PhraseDecision{}
	for _, d := range decisions {
		got[d.Kind+"/"+d.Phrase] = d
	}
	if d := got["objection/coverage of chatgpt and claude unverified"]; d.Cluster != "coverage unverified" || !same(d.Display, "coverage unverified") || d.Confidence != 0.9 || d.Source != "jev" || !d.Folds {
		t.Errorf("stored: %+v", d)
	}
	if d := got["objection/no free tier"]; d.Cluster != "" || d.Source != "rule" || d.Display != "No free tier" {
		t.Errorf("an unrelated wording is its own cluster, unasked: %+v", d)
	}
	if len(decisions) != 6 {
		t.Errorf("six wordings decided: %+v", decisions)
	}

	// A second pass asks nothing.
	if n, err := a.ClusterPhrases(ctx, id); err != nil || n != 0 || len(*asked) != 2 {
		t.Errorf("second pass asked %d (%v), %d requests", n, err, len(*asked))
	}
	// A stricter threshold splits the rows again, reading the same decisions, asking nothing.
	a.Cfg.TypeSafe.MinConfidence = 0.95
	a.invalidatePhrases()
	if rows := objectionRows(a.Summary(r, res)); len(rows) != 3 || rows["coverage unverified"].Answers != 2 {
		t.Errorf("at 0.95: %+v", rows)
	}
	if p := praiseRows(a.Opinion(ctx, id, res).Sentiment); len(p) != 3 || p["easy setup"] != 2 {
		t.Errorf("praise at 0.95: %+v", p)
	}
	if len(*asked) != 2 {
		t.Errorf("tightening the threshold asked %d more", len(*asked)-2)
	}
	// Each request settled in the ledger under the pass's name.
	var events int
	a.DB.QueryRow(`SELECT COUNT(*) FROM spend_events WHERE kind = 'provider-attempt/cluster-phrases/' || ?`, id).Scan(&events)
	if events < 2 {
		t.Errorf("ledger events %d", events)
	}
}

func TestClusterPhrasesIsRegisteredAndRunsOnDemand(t *testing.T) {
	p, ok := PassByCommand("cluster")
	if !ok || p.Name != "cluster-phrases" || p.Order != 15 || p.Provider != config.TypeSafeProvider {
		t.Fatalf("%+v %v", p, ok)
	}
	if !p.On(&config.Config{TypeSafe: &config.TypeSafe{}}) || p.On(&config.Config{}) {
		t.Error("on with [typesafe], off without")
	}
	a, id := clusterApp(t)
	asked := fakeJevCluster(t, 0.9)
	if n, err := a.RunPass(context.Background(), p, id); err != nil || n != 2 || len(*asked) != 2 {
		t.Errorf("on demand: %d %v", n, err)
	}
	// With nothing stored and no provider, every count is as before.
	b, bid := clusterApp(t)
	t.Setenv(config.TypeSafeBaseURLVar, "")
	ctx := context.Background()
	r, _ := b.Runs.Get(ctx, bid)
	res, _ := b.Runs.Results(ctx, bid)
	if rows := objectionRows(b.Summary(r, res)); len(rows) != 3 {
		t.Errorf("the floor: %+v", rows)
	}
	if _, err := b.ClusterPhrases(ctx, bid); err == nil {
		t.Error("no key, no proxy: the pass should refuse")
	}
}
