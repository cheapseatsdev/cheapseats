package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/sets"
)

// The fingerprint covers what the grader reads besides the answer, and nothing else: a change of
// brand, competitors, grader prompt or model changes it; a new answerer or prompt doesn't.
func TestGradeInputs(t *testing.T) {
	base := Snapshot{GraderPrompt: "g", Grader: "m", Brand: config.Brand{Name: "Popcorn", Aliases: []string{"pc"}, About: "a"},
		Set: sets.Set{Competitors: []sets.Competitor{{Name: "Gauge"}}, Prompts: []sets.Prompt{{ID: "p", Text: "p"}}}}
	fp := GradeInputs(base)
	same := base
	same.Answerers = []config.Answerer{{ID: "x"}}
	same.Set.Prompts = nil
	same.Brand.Facts = []string{"new fact"}
	same.Brand.Sites = []config.Site{{URL: "popcorn.zone"}}
	same.AnswerPrompt = "asked differently" // a re-grade can't change how answers were asked
	if GradeInputs(same) != fp {
		t.Error("only grading inputs count")
	}
	for name, change := range map[string]func(*Snapshot){
		"grader prompt": func(s *Snapshot) { s.GraderPrompt = "g2" },
		"grader model":  func(s *Snapshot) { s.Grader = "claude-code:m" },
		"brand name":    func(s *Snapshot) { s.Brand.Name = "Kernel" },
		"aliases":       func(s *Snapshot) { s.Brand.Aliases = []string{"pc", "popcorn.zone"} },
		"about":         func(s *Snapshot) { s.Brand.About = "b" },
		"competitor":    func(s *Snapshot) { s.Set.Competitors = []sets.Competitor{{Name: "Gauge"}, {Name: "Peec AI"}} },
		"its aliases": func(s *Snapshot) {
			s.Set.Competitors = []sets.Competitor{{Name: "Gauge", Aliases: []string{"withgauge"}}}
		},
	} {
		s := base
		s.Brand.Aliases = append([]string{}, base.Brand.Aliases...)
		change(&s)
		if GradeInputs(s) == fp {
			t.Errorf("%s doesn't change the fingerprint", name)
		}
	}
}

func TestRegrade(t *testing.T) {
	e, set, calls := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	r, err := e.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	// One that isn't finished is left alone.
	if _, err := e.Create(ctx, set, "manual"); err != nil {
		t.Fatal(err)
	}
	runs, _ := e.Since(ctx, set.ID, "")
	if len(runs) != 2 {
		t.Fatalf("%d runs", len(runs))
	}

	// Nothing changed: nothing to do, and the dry run says so.
	p, err := e.PlanRegrade(ctx, runs, set)
	if err != nil || len(p.Runs) != 0 || p.Current != 1 || p.CurrentAnswers != 6 || p.Unfinished != 1 || p.EstimatedUSD != 0 {
		t.Fatalf("unchanged: %+v %v", p, err)
	}

	// A new competitor: every answer, under the same method.
	set.Competitors = []sets.Competitor{{Name: "Coder"}}
	p, _ = e.PlanRegrade(ctx, runs, set)
	if len(p.Runs) != 1 || p.Answers != 6 || p.Runs[0].FromMethod != p.Runs[0].ToMethod || p.EstimatedUSD <= 0 || p.Subscription {
		t.Fatalf("competitors: %+v", p)
	}
	// On the claude CLI it's free.
	e.Cfg.Grader = config.Model{Model: "g", Provider: config.ClaudeCode}
	if p, _ := e.PlanRegrade(ctx, runs, set); !p.Subscription || p.EstimatedUSD != 0 || p.Answers != 6 || p.Runs[0].FromMethod == p.Runs[0].ToMethod {
		t.Fatalf("subscription: %+v", p)
	}
	e.Cfg.Grader = config.Model{Model: "g"}

	// The brand's about changes the method too. Dry runs cost nothing.
	e.Cfg.Brand.About = "cloud workstations"
	before := calls.Load()
	p, _ = e.PlanRegrade(ctx, runs, set)
	if len(p.Runs) != 1 || p.Runs[0].FromMethod == p.Runs[0].ToMethod || calls.Load() != before {
		t.Fatalf("about: %+v, %d calls", p, calls.Load()-before)
	}

	// A grader that fails leaves the run as it was.
	good := llm.AnthropicURL
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "bad request", 400) }))
	defer down.Close()
	llm.AnthropicURL = down.URL
	rep, err := e.Regrade(ctx, runs, set)
	llm.AnthropicURL = good
	if err != nil || len(rep.Regraded) != 0 || rep.Failed[r.ID] == "" {
		t.Fatalf("failing grader: %+v %v", rep, err)
	}
	if got, _ := e.Get(ctx, r.ID); got.Method != r.Method || len(got.Regrades) != 0 {
		t.Fatalf("a failed re-grade wrote the run: %+v", got)
	}

	// A budget that can't cover it doesn't start it.
	e.Cfg.Budget.DailyUSD = 0.000001
	if rep, _ := e.Regrade(ctx, runs, set); rep.Stopped == "" || len(rep.Regraded) != 0 {
		t.Fatalf("over budget: %+v", rep)
	}
	e.Cfg.Budget.DailyUSD = 0

	before = calls.Load()
	rep, err = e.Regrade(ctx, runs, set)
	if err != nil || len(rep.Regraded) != 1 || rep.Answers != 6 || rep.CostUSD <= 0 || calls.Load()-before != 6 {
		t.Fatalf("re-grade: %+v %v, %d calls", rep, err, calls.Load()-before)
	}
	got, _ := e.Get(ctx, r.ID)
	if got.Snapshot.Brand.About != "cloud workstations" || len(got.Snapshot.Set.Competitors) != 1 || got.Method != Method(got.Snapshot) || got.Method == r.Method {
		t.Fatalf("the run takes today's grading: %+v", got)
	}
	if len(got.Regrades) != 1 || got.Regrades[0].FromMethod != r.Method || got.Regrades[0].ToMethod != got.Method || got.Regrades[0].Answers != 6 {
		t.Fatalf("regrades: %+v", got.Regrades)
	}
	if got.Snapshot.AnswerPrompt != r.Snapshot.AnswerPrompt || got.Snapshot.Brand.Domains[0] != "popcorn.example" {
		t.Fatal("how answers were asked is kept")
	}
	rows, _ := e.DB.Query(`SELECT grade_history, verdict FROM results WHERE run_id=?`, r.ID)
	n := 0
	for rows.Next() {
		var h sql.NullString
		var v string
		rows.Scan(&h, &v)
		var past []PastGrade
		if json.Unmarshal([]byte(h.String), &past); len(past) != 1 || past[0].Method != r.Method || len(past[0].Grade) == 0 || v == "" {
			t.Errorf("history %s, verdict %q", h.String, v)
		}
		n++
	}
	rows.Close()
	if n != 6 {
		t.Fatalf("%d results", n)
	}
	// Done: a second pass skips it.
	runs, _ = e.Since(ctx, set.ID, "")
	if p, _ := e.PlanRegrade(ctx, runs, set); len(p.Runs) != 0 || p.Current != 1 {
		t.Fatalf("after: %+v", p)
	}
	if spent, _ := e.DB.SpentToday(ctx); spent <= 0 {
		t.Fatal("re-grading isn't accounted")
	}
}
