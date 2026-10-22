package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// regradeRuns resolves a re-grade's target, a set id or a run id, into the runs to look at and the
// set as it is today (whose competitors the grader folds onto). For a set, only runs from the last
// sinceDays days, or all of them when it's 0; a run is taken whatever its age.
func (a *App) regradeRuns(ctx context.Context, target string, sinceDays int) ([]run.Run, sets.Set, error) {
	if sinceDays < 0 {
		return nil, sets.Set{}, fmt.Errorf("since %d days: want 0 (all) or more", sinceDays)
	}
	if s, ok := a.Set(target); ok {
		since := ""
		if sinceDays > 0 {
			since = time.Now().UTC().AddDate(0, 0, -sinceDays).Format(time.RFC3339)
		}
		runs, err := a.Runs.Since(ctx, s.ID, since)
		return runs, s, err
	}
	r, err := a.Runs.Get(ctx, target)
	if err != nil {
		return nil, sets.Set{}, fmt.Errorf("no prompt set or run %q", target)
	}
	s, ok := a.Set(r.SetID)
	if !ok {
		s = r.Snapshot.Set // a set no longer configured keeps the competitors it had
	}
	return []run.Run{*r}, s, nil
}

// PlanRegrade is what re-grading target would do and cost, without grading anything.
func (a *App) PlanRegrade(ctx context.Context, target string, sinceDays int) (run.RegradePlan, error) {
	runs, s, err := a.regradeRuns(ctx, target, sinceDays)
	if err != nil {
		return run.RegradePlan{}, err
	}
	return a.Runs.PlanRegrade(ctx, runs, s)
}

// Regrade grades target's stored answers again under today's grader, brand and competitors,
// skipping runs already graded that way, within the daily budget. One re-grade per set at a time.
func (a *App) Regrade(ctx context.Context, target string, sinceDays int) (run.RegradeReport, error) {
	runs, s, err := a.regradeRuns(ctx, target, sinceDays)
	if err != nil {
		return run.RegradeReport{}, err
	}
	if !a.claimRegrade(s.ID) {
		return run.RegradeReport{}, fmt.Errorf("set %s is already being re-graded", s.ID)
	}
	defer a.analyses.Delete("regrade/" + s.ID)
	return a.Runs.Regrade(ctx, runs, s)
}

// StartRegrade is Regrade in the background, for the web UI and MCP: it returns the plan at once.
func (a *App) StartRegrade(target string, sinceDays int) (run.RegradePlan, error) {
	ctx := context.Background()
	runs, s, err := a.regradeRuns(ctx, target, sinceDays)
	if err != nil {
		return run.RegradePlan{}, err
	}
	p, err := a.Runs.PlanRegrade(ctx, runs, s)
	if err != nil || len(p.Runs) == 0 {
		return p, err
	}
	if p.Unpriced {
		return p, fmt.Errorf("the grader %s has no [pricing], so its calls can't be budgeted; price it or grade on claude-code", a.Cfg.Grader.Model)
	}
	if !a.claimRegrade(s.ID) {
		return p, fmt.Errorf("set %s is already being re-graded", s.ID)
	}
	go func() {
		defer a.analyses.Delete("regrade/" + s.ID)
		rep, err := a.Runs.Regrade(ctx, runs, s)
		if err != nil {
			slog.Error("re-grade", "target", target, "err", err)
			return
		}
		slog.Info("re-grade done", "target", target, "runs", len(rep.Regraded), "answers", rep.Answers, "cost", rep.CostUSD, "failed", len(rep.Failed), "stopped", rep.Stopped)
	}()
	return p, nil
}

func (a *App) claimRegrade(setID string) bool {
	_, busy := a.analyses.LoadOrStore("regrade/"+setID, true)
	return !busy
}

// Regrading reports whether a re-grade of the set is under way.
func (a *App) Regrading(setID string) bool {
	_, busy := a.analyses.Load("regrade/" + setID)
	return busy
}
