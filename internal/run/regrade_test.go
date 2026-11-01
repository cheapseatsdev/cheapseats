package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// The fingerprint covers what the grader reads besides the answer, and nothing else: a change of
// brand, competitors, grader prompt or model changes it; a new answerer or prompt doesn't.
func TestGradeInputs(t *testing.T) {
	base := Snapshot{GraderPrompt: "g", Grader: "m", Brand: config.Brand{Name: "Cheap Seats", Aliases: []string{"pc"}, About: "a"},
		Set: sets.Set{Competitors: []sets.Competitor{{Name: "Gauge"}}, Prompts: []sets.Prompt{{ID: "p", Text: "p"}}}}
	fp := GradeInputs(base)
	same := base
	same.Answerers = []config.Answerer{{ID: "x"}}
	same.Set.Prompts = nil
	same.Brand.Facts = []string{"new fact"}
	same.Brand.Sites = []config.Site{{URL: "cheapseats.dev"}}
	same.AnswerPrompt = "asked differently" // a re-grade can't change how answers were asked
	if GradeInputs(same) != fp {
		t.Error("only grading inputs count")
	}
	for name, change := range map[string]func(*Snapshot){
		"grader prompt": func(s *Snapshot) { s.GraderPrompt = "g2" },
		"grader model":  func(s *Snapshot) { s.Grader = "claude-code:m" },
		"brand name":    func(s *Snapshot) { s.Brand.Name = "Kernel" },
		"aliases":       func(s *Snapshot) { s.Brand.Aliases = []string{"pc", "cheapseats.dev"} },
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
	p, err := e.PlanRegrade(ctx, runs, set, false)
	if err != nil || len(p.Runs) != 0 || p.Current != 1 || p.CurrentAnswers != 6 || p.Unfinished != 1 || p.EstimatedUSD != 0 {
		t.Fatalf("unchanged: %+v %v", p, err)
	}

	// A new competitor: every answer, under the same method.
	set.Competitors = []sets.Competitor{{Name: "Coder"}}
	p, _ = e.PlanRegrade(ctx, runs, set, false)
	if len(p.Runs) != 1 || p.Answers != 6 || p.Runs[0].FromMethod != p.Runs[0].ToMethod || p.EstimatedUSD <= 0 || p.Subscription {
		t.Fatalf("competitors: %+v", p)
	}
	// On the claude CLI it's free.
	e.Cfg.Grader = config.Model{Model: "g", Provider: config.ClaudeCode}
	if p, _ := e.PlanRegrade(ctx, runs, set, false); !p.Subscription || p.EstimatedUSD != 0 || p.Answers != 6 || p.Runs[0].FromMethod == p.Runs[0].ToMethod {
		t.Fatalf("subscription: %+v", p)
	}
	e.Cfg.Grader = config.Model{Model: "g"}

	// The brand's about changes the method too. Dry runs cost nothing.
	e.Cfg.Brand.About = "cloud workstations"
	before := calls.Load()
	p, _ = e.PlanRegrade(ctx, runs, set, false)
	if len(p.Runs) != 1 || p.Runs[0].FromMethod == p.Runs[0].ToMethod || calls.Load() != before {
		t.Fatalf("about: %+v, %d calls", p, calls.Load()-before)
	}

	// A grader that fails leaves the run as it was.
	good := llm.AnthropicURL
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "bad request", 400) }))
	defer down.Close()
	llm.AnthropicURL = down.URL
	rep, err := e.Regrade(ctx, runs, set, false)
	llm.AnthropicURL = good
	if err != nil || len(rep.Regraded) != 0 || rep.Failed[r.ID] == "" {
		t.Fatalf("failing grader: %+v %v", rep, err)
	}
	if got, _ := e.Get(ctx, r.ID); got.Method != r.Method || len(got.Regrades) != 0 {
		t.Fatalf("a failed re-grade wrote the run: %+v", got)
	}

	// A budget that can't cover it doesn't start it.
	e.Cfg.Budget.DailyUSD = 0.000001
	if rep, _ := e.Regrade(ctx, runs, set, false); rep.Stopped == "" || len(rep.Regraded) != 0 {
		t.Fatalf("over budget: %+v", rep)
	}
	e.Cfg.Budget.DailyUSD = 0

	before = calls.Load()
	rep, err = e.Regrade(ctx, runs, set, false)
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
	if got.Snapshot.AnswerPrompt != r.Snapshot.AnswerPrompt || got.Snapshot.Brand.Domains[0] != "cheapseats.example" {
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
	if p, _ := e.PlanRegrade(ctx, runs, set, false); len(p.Runs) != 0 || p.Current != 1 {
		t.Fatalf("after: %+v", p)
	}
	if spent, _ := e.DB.SpentToday(ctx); spent <= 0 {
		t.Fatal("re-grading isn't accounted")
	}
}

// A run that changes between being graded and being written (cheapseats regrade beside cheapseats
// serve, a retry from another process) is left as the other writer left it; in this process a
// retry can't start while the re-grade holds the run.
func TestRegradeWritesOnlyOverTheRunItRead(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	r, _ := e.Create(ctx, set, "manual")
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	e.Cfg.Brand.About = "cloud workstations"
	runs, _ := e.Since(ctx, set.ID, "")
	beforeRegradeWrite = func(id string) {
		if e.Start(id) || e.Execute(ctx, id) == nil {
			t.Error("a retry started during the re-grade")
		}
		e.DB.Exec(`UPDATE runs SET method='changed elsewhere' WHERE id=?`, id)
	}
	defer func() { beforeRegradeWrite = nil }()
	rep, err := e.Regrade(ctx, runs, set, false)
	if err != nil || len(rep.Regraded) != 0 || !strings.Contains(rep.Failed[r.ID], "changed while it was being re-graded") {
		t.Fatalf("%+v %v", rep, err)
	}
	got, _ := e.Get(ctx, r.ID)
	var history int
	e.DB.QueryRow(`SELECT COUNT(*) FROM results WHERE run_id=? AND grade_history IS NOT NULL`, r.ID).Scan(&history)
	if got.Method != "changed elsewhere" || len(got.Regrades) != 0 || got.Snapshot.Brand.About != "" || history != 0 {
		t.Fatalf("written over a changed run: %+v, %d histories", got, history)
	}
}

// The answer prompt and search policy are part of the method, and a re-grade can't change them:
// a run asked differently would land in a method of its own, so it's skipped, and said so.
func TestRegradeSkipsRunsAskedDifferently(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	r, _ := e.Create(ctx, set, "manual")
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	e.Cfg.Brand.About = "cloud workstations"
	runs, _ := e.Since(ctx, set.ID, "")
	if p, _ := e.PlanRegrade(ctx, runs, set, false); len(p.Runs) != 1 || p.AskedDifferently != 0 {
		t.Fatalf("asked the same way: %+v", p)
	}
	e.Cfg.Brand.Audience = "an HR lead" // today's answer prompt differs from the run's
	if p, _ := e.PlanRegrade(ctx, runs, set, false); len(p.Runs) != 0 || p.AskedDifferently != 1 || p.Answers != 0 {
		t.Fatalf("asked differently: %+v", p)
	}
}

// One flaky grading call doesn't throw away the run: the answer is tried again.
func TestRegradeRetriesAFlakyAnswer(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	r, err := e.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	set.Competitors = []sets.Competitor{{Name: "Coder"}}
	runs, _ := e.Since(ctx, set.ID, "")

	good := llm.AnthropicURL
	var n atomic.Int32
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if n.Add(1) == 1 {
			http.Error(w, "bad request", 400) // not retried by the client, only by the re-grade
			return
		}
		out, err := http.NewRequest(req.Method, good, req.Body)
		if err != nil {
			t.Error(err)
			return
		}
		out.Header = req.Header.Clone()
		res, err := http.DefaultClient.Do(out)
		if err != nil {
			t.Error(err)
			return
		}
		defer res.Body.Close()
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
	}))
	defer flaky.Close()
	llm.AnthropicURL = flaky.URL
	defer func() { llm.AnthropicURL = good }()

	rep, err := e.Regrade(ctx, runs, set, false)
	if err != nil || len(rep.Regraded) != 1 || len(rep.Failed) != 0 {
		t.Fatalf("a flaky answer lost the run: %+v %v", rep, err)
	}
}

// A fix to the grading code doesn't change the grading inputs, so a run already graded with
// today's is skipped unless forced. Forced, the plan counts and prices what it adds, the grades
// are replaced with the old ones kept in grade_history, and the re-grade is recorded as forced.
// A run asked differently still can't be forced into today's trend.
func TestRegradeForce(t *testing.T) {
	e, set, calls := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	r, _ := e.Create(ctx, set, "manual")
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	runs, _ := e.Since(ctx, set.ID, "")
	if p, _ := e.PlanRegrade(ctx, runs, set, false); len(p.Runs) != 0 || p.Current != 1 || p.Forced != 0 {
		t.Fatalf("unforced: %+v", p)
	}
	p, err := e.PlanRegrade(ctx, runs, set, true)
	if err != nil || len(p.Runs) != 1 || !p.Runs[0].Forced || p.Current != 0 || p.Forced != 1 || p.ForcedAnswers != 6 || p.Answers != 6 ||
		p.ForcedUSD <= 0 || p.ForcedUSD != p.EstimatedUSD || p.Runs[0].FromMethod != p.Runs[0].ToMethod {
		t.Fatalf("forced plan: %+v %v", p, err)
	}

	before := calls.Load()
	rep, err := e.Regrade(ctx, runs, set, true)
	if err != nil || len(rep.Regraded) != 1 || rep.Answers != 6 || calls.Load()-before != 6 {
		t.Fatalf("forced re-grade: %+v %v, %d calls", rep, err, calls.Load()-before)
	}
	got, _ := e.Get(ctx, r.ID)
	if got.Method != r.Method || len(got.Regrades) != 1 || !got.Regrades[0].Forced || got.Regrades[0].FromInputs != got.Regrades[0].ToInputs {
		t.Fatalf("forced regrades: %+v", got.Regrades)
	}
	var history int
	e.DB.QueryRow(`SELECT COUNT(*) FROM results WHERE run_id=? AND json_array_length(grade_history)=1`, r.ID).Scan(&history)
	if history != 6 {
		t.Fatalf("%d of 6 answers kept their replaced grade", history)
	}

	e.Cfg.Brand.Audience = "an HR lead" // today's answer prompt differs from the run's
	if p, _ := e.PlanRegrade(ctx, runs, set, true); len(p.Runs) != 0 || p.AskedDifferently != 1 || p.Forced != 0 {
		t.Fatalf("asked differently, forced: %+v", p)
	}
}
