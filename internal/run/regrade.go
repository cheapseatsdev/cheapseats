package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// Re-grading grades a finished run's stored answers again under today's grader prompt and model,
// brand (name, aliases, about) and competitors, without asking the answerers again. A run is
// re-graded whole or not at all, so it never mixes two rubrics: every new grade is worked out
// first and written in one transaction with the run's snapshot, whose grading parts become
// today's, so its method (and with it the trend it belongs to) is recomputed from it as for any
// run. Each answer's replaced grade is kept in results.grade_history and each re-grade in
// runs.regrades, so the run page can say "re-graded (was method X)" and nothing is lost.

// GradeInputs fingerprints what grading an answer reads besides the answer itself: the grader
// prompt and model, the brand as the grader knows it, and the competitors it folds names onto.
// A run whose fingerprint is today's would grade the same, so re-grading skips it unless forced:
// a fix to the grading code (its post-processing, say) changes grades without changing these.
func GradeInputs(s Snapshot) string {
	type competitor struct {
		Name    string
		Aliases []string
	}
	cs := []competitor{}
	for _, c := range s.Set.Competitors {
		cs = append(cs, competitor{c.Name, c.Aliases})
	}
	b := s.Brand
	return Digest(struct {
		GraderPrompt, Grader string
		Name, About          string
		Aliases              []string
		Competitors          []competitor
	}{s.GraderPrompt, s.Grader, b.Name, b.About, b.Aliases, cs})
}

// Regraded is the snapshot with its grading parts as today's config and set have them; the rest
// (how answers were asked, the prompts, the answerers) stays as the run had it.
func (e *Engine) Regraded(s Snapshot, set sets.Set) Snapshot {
	s.GraderPrompt, s.Grader = grade.System, e.Cfg.Grader.Ref()
	s.Brand.Name, s.Brand.Aliases, s.Brand.About = e.Cfg.Brand.Name, e.Cfg.Brand.Aliases, e.Cfg.Brand.About
	s.Set.Competitors = set.Competitors
	return s
}

// Regrade is one re-grade of a run, kept with it (runs.regrades, newest last).
type Regrade struct {
	At         string  `json:"at"`
	Answers    int     `json:"answers"`
	CostUSD    float64 `json:"cost_usd"`
	FromMethod string  `json:"from_method"`
	ToMethod   string  `json:"to_method"`
	FromInputs string  `json:"from_inputs"`
	ToInputs   string  `json:"to_inputs"`
	FromGrader string  `json:"from_grader"`
	ToGrader   string  `json:"to_grader"`
	// Forced: the run was already graded with today's inputs and re-graded anyway (-force).
	Forced bool `json:"forced,omitempty"`
}

// PastGrade is a grade a re-grade replaced (results.grade_history, newest last).
type PastGrade struct {
	Grade      json.RawMessage `json:"grade"`
	CostUSD    float64         `json:"cost_usd"`
	Method     string          `json:"method"`
	Inputs     string          `json:"inputs"`
	ReplacedAt string          `json:"replaced_at"`
}

// RegradePlan is what a re-grade would do: the runs whose grading inputs changed, how many answers
// they hold, and what grading them again would cost.
type RegradePlan struct {
	Grader string `json:"grader"`
	// On the claude CLI: the subscription pays, so it costs nothing.
	Subscription bool         `json:"subscription"`
	Runs         []RegradeRun `json:"runs"`
	Answers      int          `json:"answers"`
	// Runs already graded with today's inputs, and their answers: skipped unless forced.
	Current        int `json:"current_runs"`
	CurrentAnswers int `json:"current_answers"`
	// With force, those runs are in Runs instead (marked Forced): how many, their answers, and
	// their part of EstimatedUSD.
	Forced        int     `json:"forced_runs,omitempty"`
	ForcedAnswers int     `json:"forced_answers,omitempty"`
	ForcedUSD     float64 `json:"forced_usd,omitempty"`
	// Runs left alone because they aren't finished or are running.
	Unfinished int `json:"unfinished_runs"`
	// Runs whose answers were asked under another answer prompt or search policy: those are part of
	// the method too, and a re-grade can't change them, so it couldn't join them to today's trend.
	AskedDifferently int     `json:"asked_differently_runs"`
	EstimatedUSD     float64 `json:"estimated_usd"`
	// The API grader's model has no [pricing]: no estimate, and the budget refuses its calls.
	Unpriced bool `json:"unpriced,omitempty"`
}

type RegradeRun struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	CreatedAt    string  `json:"created_at"`
	Answers      int     `json:"answers"`
	EstimatedUSD float64 `json:"estimated_usd"`
	FromMethod   string  `json:"from_method"`
	ToMethod     string  `json:"to_method"`
	// Already graded with today's inputs; in the plan because it was forced.
	Forced bool `json:"forced,omitempty"`
}

// RegradeReport is what a re-grade did.
type RegradeReport struct {
	RegradePlan
	Regraded []string `json:"regraded"` // run ids
	Answers  int      `json:"answers_regraded"`
	CostUSD  float64  `json:"cost_usd"`
	// Runs that failed, each left as it was, and why; and why the re-grade stopped early, if it did.
	Failed  map[string]string `json:"failed,omitempty"`
	Stopped string            `json:"stopped,omitempty"`
}

// ErrRegradeConflict is a run that changed while it was being re-graded: nothing was written.
var ErrRegradeConflict = errors.New("re-grade conflict")

// beforeRegradeWrite, when set, is called between grading a run and writing it: a test's way in.
var beforeRegradeWrite func(id string)

// estGradeOutput is the output tokens a grade is estimated at: the JSON and the model's thinking.
const estGradeOutput = 1500

// gradable is a result a re-grade grades again: done, with an answer.
type gradable struct {
	id     int64
	prompt string
	answer string
}

func (e *Engine) gradable(ctx context.Context, id string) ([]gradable, error) {
	rows, err := e.DB.QueryContext(ctx, `SELECT id, prompt, answer FROM results WHERE run_id=? AND status='done' AND answer IS NOT NULL ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gradable
	for rows.Next() {
		var g gradable
		var ans string
		if err := rows.Scan(&g.id, &g.prompt, &ans); err != nil {
			return nil, err
		}
		var a struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(ans), &a) == nil && a.Text != "" {
			g.answer = a.Text
			out = append(out, g)
		}
	}
	return out, rows.Err()
}

// PlanRegrade works out a re-grade of runs, whose set today is set, without grading anything.
// force also takes runs already graded with today's inputs, for a change to the grading code those
// inputs don't cover; runs asked differently are still skipped.
func (e *Engine) PlanRegrade(ctx context.Context, runs []Run, set sets.Set, force bool) (RegradePlan, error) {
	p := RegradePlan{Grader: e.Cfg.Grader.Ref(), Subscription: e.Cfg.Grader.Via() == config.ClaudeCode, Runs: []RegradeRun{}}
	_, priced := e.Cfg.Pricing[e.Cfg.Grader.Model]
	p.Unpriced = !p.Subscription && !priced
	for _, r := range runs {
		if r.Status != Done || e.IsActive(r.ID) {
			p.Unfinished++
			continue
		}
		if !r.Snapshot.AskedAsToday(e.Cfg) {
			p.AskedDifferently++
			continue
		}
		gs, err := e.gradable(ctx, r.ID)
		if err != nil {
			return p, stored(err)
		}
		now := e.Regraded(r.Snapshot, set)
		current := GradeInputs(r.Snapshot) == GradeInputs(now)
		if current && !force {
			p.Current++
			p.CurrentAnswers += len(gs)
			continue
		}
		rr := RegradeRun{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, Answers: len(gs), FromMethod: r.Method, ToMethod: Method(now), Forced: current}
		if !p.Subscription {
			cfg := *e.Cfg
			for _, g := range gs {
				in := grade.Input(&cfg, now.Set, g.prompt, g.answer)
				// About four characters a token, and the schema on top.
				rr.EstimatedUSD += e.Cfg.Cost(e.Cfg.Grader.Model, config.Usage{Input: int64(len(grade.System)+len(in))/4 + 400, Output: estGradeOutput})
			}
		}
		p.Runs = append(p.Runs, rr)
		p.Answers += rr.Answers
		p.EstimatedUSD += rr.EstimatedUSD
		if rr.Forced {
			p.Forced++
			p.ForcedAnswers += rr.Answers
			p.ForcedUSD += rr.EstimatedUSD
		}
	}
	return p, nil
}

// Regrade grades the planned runs' answers again and records the new grades, run by run. A run
// whose grading fails is left as it was; one the day's budget can't cover isn't started. force is
// as for PlanRegrade.
func (e *Engine) Regrade(ctx context.Context, runs []Run, set sets.Set, force bool) (RegradeReport, error) {
	p, err := e.PlanRegrade(ctx, runs, set, force)
	rep := RegradeReport{RegradePlan: p, Regraded: []string{}}
	if err != nil {
		return rep, err
	}
	if p.Unpriced && len(p.Runs) > 0 {
		return rep, fmt.Errorf("the grader %s has no [pricing], so its calls can't be budgeted; price it or grade on claude-code", e.Cfg.Grader.Model)
	}
	byID := map[string]Run{}
	for _, r := range runs {
		byID[r.ID] = r
	}
	for _, rr := range p.Runs {
		if ctx.Err() != nil {
			rep.Stopped = string(Cancelled)
			break
		}
		if !p.Subscription && e.Cfg.Budget.DailyUSD > 0 {
			spent, err := e.DB.SpentToday(ctx)
			if err != nil {
				return rep, stored(err)
			}
			if spent+rr.EstimatedUSD > e.Cfg.Budget.DailyUSD {
				rep.Stopped = fmt.Sprintf("today's budget can't cover re-grading run %s (about $%.2f, $%.2f of $%.0f spent); the rest are left as they were", rr.ID, rr.EstimatedUSD, spent, e.Cfg.Budget.DailyUSD)
				break
			}
		}
		n, cost, err := e.regradeRun(ctx, byID[rr.ID], set, rr.Forced)
		rep.CostUSD += cost
		if err != nil {
			if rep.Failed == nil {
				rep.Failed = map[string]string{}
			}
			rep.Failed[rr.ID] = err.Error()
			var se *StorageError
			if errors.Is(err, errBudget) || errors.As(err, &se) || ctx.Err() != nil {
				rep.Stopped = err.Error()
				break
			}
			continue
		}
		rep.Regraded = append(rep.Regraded, rr.ID)
		rep.Answers += n
	}
	return rep, nil
}

// regradeTries is how many times re-grading tries one answer before the run is given up.
const regradeTries = 3

// regradeRun grades one run's answers again, then writes them all, or nothing if any failed.
// forced records that the run was already graded with today's inputs.
func (e *Engine) regradeRun(ctx context.Context, r Run, set sets.Set, forced bool) (int, float64, error) {
	e.mu.Lock()
	if _, ok := e.active[r.ID]; ok || e.stopping.Load() {
		e.mu.Unlock()
		return 0, 0, fmt.Errorf("run is active")
	}
	ctx, cancel := context.WithCancel(ctx)
	e.active[r.ID] = cancel
	e.running.Add(1)
	e.mu.Unlock()
	defer e.running.Done()
	defer func() { cancel(); e.mu.Lock(); delete(e.active, r.ID); e.mu.Unlock() }()

	gs, err := e.gradable(ctx, r.ID)
	if err != nil {
		return 0, 0, stored(err)
	}
	now := e.Regraded(r.Snapshot, set)
	cfg := *e.Cfg
	cfg.Brand = now.Brand
	ctx = llm.WithOperation(llm.WithAccounting(ctx, e.Budget), "regrade/"+r.ID)
	grades := make([]grade.Grade, len(gs))
	costs := make([]float64, len(gs))
	errs := make([]error, len(gs))
	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(max(1, e.Cfg.Budget.Concurrency), len(gs)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				// A run is written all or nothing, so one flaky call would waste the rest:
				// try each answer a few times before giving the run up.
				for try := 0; try < regradeTries; try++ {
					var c float64
					grades[i], c, errs[i] = grade.Answer(ctx, &cfg, now.Set, gs[i].prompt, gs[i].answer)
					costs[i] += c
					if errs[i] == nil || ctx.Err() != nil || errors.Is(errs[i], errBudget) {
						break
					}
					slog.Warn("re-grade: grading failed, trying again", "run", r.ID, "result", gs[i].id, "try", try+1, "err", errs[i])
				}
				if errs[i] != nil {
					cancel() // the run can't be written, so the rest would be wasted
				}
			}
		}()
	}
	for i := range gs {
		if ctx.Err() != nil {
			break
		}
		work <- i
	}
	close(work)
	wg.Wait()
	var cost float64
	for _, c := range costs {
		cost += c
	}
	for i, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			return 0, cost, fmt.Errorf("grading result %d: %w", gs[i].id, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, cost, err
	}

	if beforeRegradeWrite != nil {
		beforeRegradeWrite(r.ID)
	}
	at := store.Now()
	from, to := GradeInputs(r.Snapshot), GradeInputs(now)
	tx, err := e.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return 0, cost, stored(err)
	}
	defer tx.Rollback()
	// Another process (cheapseats regrade beside cheapseats serve, a retry) may have changed the run
	// while it was being graded: write only over the run as it was read, else nothing.
	var snapJS, method string
	var status Status
	var prior sql.NullString
	if err := tx.QueryRow(`SELECT snapshot, method, status, regrades FROM runs WHERE id=?`, r.ID).Scan(&snapJS, &method, &status, &prior); err != nil {
		return 0, cost, stored(err)
	}
	var cur Snapshot
	if status != Done || method != r.Method || json.Unmarshal([]byte(snapJS), &cur) != nil || GradeInputs(cur) != from {
		return 0, cost, fmt.Errorf("%w: run %s changed while it was being re-graded (now %s); nothing written", ErrRegradeConflict, r.ID, status)
	}
	var regrades []Regrade
	if prior.Valid {
		json.Unmarshal([]byte(prior.String), &regrades)
	}
	regrades = append(regrades, Regrade{At: at, Answers: len(gs), CostUSD: cost, FromMethod: r.Method, ToMethod: Method(now),
		FromInputs: from, ToInputs: to, FromGrader: r.Snapshot.Grader, ToGrader: now.Grader, Forced: forced})
	sj, _ := json.Marshal(now)
	rj, _ := json.Marshal(regrades)
	res, err := tx.Exec(`UPDATE runs SET snapshot=?, method=?, regrades=? WHERE id=? AND status='done' AND method=? AND snapshot=?`, string(sj), Method(now), string(rj), r.ID, method, snapJS)
	if err != nil {
		return 0, cost, stored(err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return 0, cost, fmt.Errorf("%w: run %s changed while it was being re-graded; nothing written", ErrRegradeConflict, r.ID)
	}
	for i, g := range gs {
		// The grade and its history as they are now, not as they were when grading started.
		var old, hist sql.NullString
		var oldCost float64
		if err := tx.QueryRow(`SELECT grade, grade_cost_usd, grade_history FROM results WHERE id=?`, g.id).Scan(&old, &oldCost, &hist); err != nil {
			return 0, cost, stored(err)
		}
		var past []PastGrade
		if hist.Valid {
			json.Unmarshal([]byte(hist.String), &past)
		}
		if old.Valid {
			past = append(past, PastGrade{Grade: json.RawMessage(old.String), CostUSD: oldCost, Method: r.Method, Inputs: from, ReplacedAt: at})
		}
		hj, _ := json.Marshal(past)
		gj, _ := json.Marshal(grades[i])
		if _, err := tx.Exec(`UPDATE results SET grade=?, verdict=?, top_pick=?, grade_cost_usd=grade_cost_usd+?, grade_history=? WHERE id=?`,
			string(gj), string(grades[i].Verdict), grades[i].IsTopPick(now.Brand.Name), costs[i], string(hj), g.id); err != nil {
			return 0, cost, stored(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, cost, stored(err)
	}
	slog.Info("re-graded", "run", r.ID, "answers", len(gs), "cost", cost)
	return len(gs), cost, nil
}

// Since is the set's runs created at or after since (RFC 3339, or a prefix of it), oldest first.
func (e *Engine) Since(ctx context.Context, setID, since string) ([]Run, error) {
	rows, err := e.DB.QueryContext(ctx, `SELECT id FROM runs WHERE set_id=? AND created_at>=? ORDER BY created_at`, setID, since)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var out []Run
	for _, id := range ids {
		r, err := e.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, nil
}
