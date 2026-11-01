package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// A re-graded run takes today's method, so the trend reads it as today's from its own date on:
// it pools with today's runs and the break moves back to before it. Runs not re-graded keep
// their method, and the line between them and the re-graded ones is still a break.
func TestTrendsTakeRegradedRunsAsTheNewMethod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g := `{"verdict":"recommended","position":1,"top_pick":"Cheap Seats","named":["Cheap Seats"],"reasoning":"r","why_not_top":null,"objections":[],"other_product":false,"tone":"positive","praise":[],"evidence":"Use Cheap Seats."}`
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": g}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 100, "output_tokens": 50}})
	}))
	defer srv.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "test")

	dir := t.TempDir()
	writeConfig(t, dir, "[[prompts]]\nid = \"p\"\ntext = \"Which tool?\"\n")
	cfg, err := config.Load(filepath.Join(dir, "cheapseats.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Pricing = map[string]config.Pricing{"g": {Input: 1, Output: 5}}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	day := func(n int) string { return time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02") }
	// Four days, the first three graded under an older rubric.
	for i, rubric := range []string{"older rubric", "older rubric", "older rubric", grade.System} {
		gradedRun(t, db, "r-"+string(rune('1'+i)), day(4-i), rubric)
	}
	a, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	series := func() []Day {
		t.Helper()
		rk, err := a.Rankings(ctx, 30, 7, "s")
		if err != nil || len(rk) == 0 {
			t.Fatalf("rankings: %v %v", rk, err)
		}
		return rk[0].Days // newest first
	}
	d := series()
	if len(d) != 4 || !d[0].NewMethod || d[1].NewMethod || d[0].Window != 1 {
		t.Fatalf("before: %+v", d)
	}

	// The dry run counts answers and grades nothing.
	p, err := a.PlanRegrade(ctx, "s", 0, false)
	if err != nil || len(p.Runs) != 3 || p.Answers != 6 || p.Current != 1 || p.CurrentAnswers != 2 || p.EstimatedUSD <= 0 {
		t.Fatalf("plan: %+v %v", p, err)
	}
	if got, _ := a.Runs.Get(ctx, "r-1"); got.Method == a.latestMethod(t, "r-4") {
		t.Fatal("a dry run changed the run")
	}

	// Re-grade the two most recent old runs, one by one.
	for _, id := range []string{"r-2", "r-3"} {
		rep, err := a.Regrade(ctx, id, 0, false)
		if err != nil || len(rep.Regraded) != 1 || rep.Answers != 2 {
			t.Fatalf("%s: %+v %v", id, rep, err)
		}
	}
	d = series()
	if d[0].NewMethod || d[1].NewMethod || !d[2].NewMethod {
		t.Fatalf("the break moves to before the re-graded runs: %+v", d)
	}
	if d[0].Window != 3 || d[0].Method != d[2].Method || d[3].Method == d[0].Method {
		t.Fatalf("today's rolling window pools the re-graded days: %+v", d)
	}
	// Backfill recomputes methods from snapshots at every start; a re-graded run keeps its new one.
	if err := a.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Runs.Get(ctx, "r-2"); got.Method != a.latestMethod(t, "r-4") || len(got.Regrades) != 1 {
		t.Fatalf("after backfill: %+v", got)
	}
	res, _ := a.Runs.Results(ctx, "r-2")
	if res[0].Grade.Evidence != "Use Cheap Seats." {
		t.Fatalf("re-graded with evidence: %+v", res[0].Grade)
	}
}

func (a *App) latestMethod(t *testing.T, id string) string {
	t.Helper()
	r, err := a.Runs.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r.Method
}

// gradedRun writes a finished run of set s graded under rubric: one prompt, both tracks, each
// answer naming Cheap Seats.
func gradedRun(t *testing.T, db *store.DB, id, day, rubric string) {
	t.Helper()
	snap := map[string]any{
		"answer_prompt": answer.System("a software developer"),
		"search_policy": answer.SearchPolicy,
		"grader_prompt": rubric,
		"set":           map[string]any{"id": "s", "name": "S", "tier": "core", "prompts": []any{map[string]any{"id": "p", "text": "Which tool?"}}, "version": "v"},
		"brand":         map[string]any{"Name": "Cheap Seats"},
		"answerers":     []any{map[string]any{"ID": "claude", "Provider": "anthropic", "Model": "m", "Samples": map[string]any{"Search": 1, "Recall": 1}}},
		"grader":        "g",
	}
	js, _ := json.Marshal(snap)
	at := day + "T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO runs (id,set_id,set_version,name,trigger,status,snapshot,created_at,finished_at,profile,design) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, "s", "v", "S "+day, "schedule", "done", string(js), at, at, "p", "d"); err != nil {
		t.Fatal(err)
	}
	for _, track := range []string{"search", "recall"} {
		ans, _ := json.Marshal(answer.Answer{Text: "Use Cheap Seats."})
		g, _ := json.Marshal(map[string]any{"verdict": "mentioned", "named": []string{"Cheap Seats"}})
		if _, err := db.Exec(`INSERT INTO results (run_id,prompt_idx,prompt,prompt_id,prompt_sha,answerer,model,track,sample,status,answer,grade,verdict,finished_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, 0, "Which tool?", "p", "", "claude", "m", track, 0, "done", string(ans), string(g), "mentioned", at); err != nil {
			t.Fatal(err)
		}
	}
}
