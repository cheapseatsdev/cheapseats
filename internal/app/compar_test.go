package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// comparRun is a finished run for comparTest: when, under which rubric and answer prompt, and
// what every one of its answers said and was graded.
type comparRun struct {
	id, day, set, rubric, ask, text, verdict string
	fixes                                    string
}

// comparFakeGrader grades by the answer's text: recommended when it names the brand, else absent.
// So re-grading a run keeps what its answers said and changes only the rubric they're read under.
func comparFakeGrader(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g := `{"verdict":"absent","position":0,"top_pick":"Other","named":["Other"],"reasoning":"r","why_not_top":"w","objections":[],"other_product":false,"tone":"neutral","praise":[],"evidence":"Use Other."}`
		if strings.Contains(string(body), "Use Cheap Seats.") {
			g = `{"verdict":"recommended","position":1,"top_pick":"Cheap Seats","named":["Cheap Seats"],"reasoning":"r","why_not_top":null,"objections":[],"other_product":false,"tone":"positive","praise":[],"evidence":"Use Cheap Seats."}`
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": g}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 100, "output_tokens": 50}})
	}))
	t.Cleanup(srv.Close)
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	t.Cleanup(func() { llm.AnthropicURL = old })
	t.Setenv("ANTHROPIC_API_KEY", "test")
}

// comparGradedRun writes run x: one prompt, six answers a track, all saying x.text and graded
// x.verdict under x.rubric.
func comparGradedRun(t *testing.T, db *store.DB, x comparRun) {
	t.Helper()
	snap := map[string]any{
		"answer_prompt": x.ask,
		"search_policy": answer.SearchPolicy,
		"grader_prompt": x.rubric,
		"set":           map[string]any{"id": x.set, "name": "S", "tier": "core", "prompts": []any{map[string]any{"id": "p", "text": "Which tool?"}}, "version": "v"},
		"brand":         map[string]any{"Name": "Cheap Seats"},
		"answerers":     []any{map[string]any{"ID": "claude", "Provider": "anthropic", "Model": "m", "Samples": map[string]any{"Search": 6, "Recall": 6}}},
		"grader":        "g",
	}
	js, _ := json.Marshal(snap)
	var decoded run.Snapshot
	json.Unmarshal(js, &decoded)
	var fixes any
	if x.fixes != "" {
		fixes = x.fixes
	}
	at := x.day + "T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO runs (id,set_id,set_version,name,trigger,status,snapshot,created_at,finished_at,profile,design,method,fixes,fixes_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		x.id, x.set, "v", "S "+x.day, "schedule", "done", string(js), at, at, "p", "d", run.Method(decoded), fixes, at); err != nil {
		t.Fatal(err)
	}
	ans, _ := json.Marshal(answer.Answer{Text: x.text})
	g, _ := json.Marshal(map[string]any{"verdict": x.verdict, "named": []string{}})
	for _, track := range []string{"search", "recall"} {
		for sample := 0; sample < 6; sample++ {
			if _, err := db.Exec(`INSERT INTO results (run_id,prompt_idx,prompt,prompt_id,prompt_sha,answerer,model,track,sample,status,answer,grade,verdict,finished_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				x.id, 0, "Which tool?", "p", sets.Prompt{Text: "Which tool?"}.SHA(), "claude", "m", track, sample, "done", string(ans), string(g), x.verdict, at); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// An attempt whose set has only run since under another grading method reads needs regrade and
// names the runs to re-grade: the fix's, the ones pooled before it, and runs since on older
// methods. Re-grading them measures it. Pending stays pending, and an attempt whose set has only
// run since with answers asked differently is not comparable, as no re-grade could join them.
func TestAttemptsStayMeasurableAcrossAMethodChange(t *testing.T) {
	comparFakeGrader(t)
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
	ask := answer.System("a software developer")
	fixes := `[{"rank":1,"type":"new-page","title":"Publish a comparison","target":"https://cheapseats.example/compare","claim":"c","done_when":"d","evidence":"e","prompts":[1],"prompt_ids":["p"],"effort":"S","displaces":null}]`
	// The fix's run and one before it, under an older rubric, with no answer naming the brand.
	comparGradedRun(t, db, comparRun{"r-0", day(14), "s", "older rubric", ask, "Use Other.", "absent", ""})
	comparGradedRun(t, db, comparRun{"r-1", day(12), "s", "older rubric", ask, "Use Other.", "absent", fixes})
	a, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at, err := a.MarkFix(ctx, "r-1", 1, "https://github.com/acme/site/pull/1", Accepted, "", "test")
	if err != nil || at.Outcome.Status() != OutcomePending {
		t.Fatalf("marked: %+v %v", at.Outcome, err)
	}
	db.Exec(`UPDATE fix_attempts SET created_at=?`, day(11)+"T12:00:00Z")
	outcome := func() Outcome {
		t.Helper()
		all, err := a.Attempts(ctx, "s")
		if err != nil || len(all) != 1 {
			t.Fatalf("attempts: %+v %v", all, err)
		}
		return all[0].Outcome
	}

	// The set runs again, every answer naming the brand, but the grader changed twice meanwhile.
	comparGradedRun(t, db, comparRun{"r-2", day(5), "s", "middle rubric", ask, "Use Cheap Seats.", "recommended", ""})
	comparGradedRun(t, db, comparRun{"r-3", day(3), "s", grade.System, ask, "Use Cheap Seats.", "recommended", ""})
	comparGradedRun(t, db, comparRun{"r-4", day(2), "s", grade.System, ask, "Use Cheap Seats.", "recommended", ""})
	o := outcome()
	if o.Status() != OutcomeNeedsRegrade || strings.Join(o.RegradeRuns, " ") != "r-1 r-0 r-2" || o.RegradeSinceDays < 14 || o.RegradeSinceDays > 15 || o.Incomparable {
		t.Fatalf("after a method change: %+v %s", o, o.Status())
	}
	if s := o.Summary(); !strings.HasPrefix(s, "needs regrade: ") || !strings.Contains(s, "re-grade 3 runs (r-1, r-0, r-2)") {
		t.Errorf("summary: %s", s)
	}
	js, _ := json.Marshal(o)
	if !strings.Contains(string(js), `"status":"needs regrade"`) || !strings.Contains(string(js), `"regrade_runs":["r-1","r-0","r-2"]`) || !strings.Contains(string(js), `"regrade_since_days":`) {
		t.Errorf("json: %s", js)
	}

	// Re-grading the set brings the three runs to today's method; the attempt is measured: the
	// brand went from named in none of the 24 answers before to all 36 after.
	rep, err := a.Regrade(ctx, "s", o.RegradeSinceDays)
	if err != nil || len(rep.Regraded) != 3 || rep.Current != 2 {
		t.Fatalf("regrade: %+v %v", rep, err)
	}
	o = outcome()
	if o.Status() != OutcomeMoved || o.RunID != "r-4" || o.BeforeRuns != 2 || o.AfterRuns != 3 || len(o.RegradeRuns) != 0 ||
		o.Prompts[0].Before.N != 24 || o.Prompts[0].Before.Named != 0 || o.Prompts[0].After.N != 36 || o.Prompts[0].After.Recommended != 36 {
		t.Fatalf("after re-grading: %+v %s", o, o.Summary())
	}

	// Another set's attempt, whose set has since run only with answers asked differently: nothing
	// to re-grade, and it says so.
	comparGradedRun(t, db, comparRun{"t-1", day(12), "t", grade.System, ask, "Use Other.", "absent", fixes})
	if _, err := a.MarkFix(ctx, "t-1", 1, "https://github.com/acme/site/pull/2", Accepted, "", "test"); err != nil {
		t.Fatal(err)
	}
	db.Exec(`UPDATE fix_attempts SET created_at=? WHERE run_id='t-1'`, day(11)+"T12:00:00Z")
	comparGradedRun(t, db, comparRun{"t-2", day(2), "t", grade.System, answer.System("a CTO"), "Use Cheap Seats.", "recommended", ""})
	all, _ := a.Attempts(ctx, "t")
	if len(all) != 1 || all[0].Outcome.Status() != OutcomeIncomparable || len(all[0].Outcome.RegradeRuns) != 0 || !strings.HasPrefix(all[0].Outcome.Summary(), "not comparable: ") {
		t.Fatalf("asked differently: %+v", all)
	}
}
