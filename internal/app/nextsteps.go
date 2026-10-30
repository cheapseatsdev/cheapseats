package app

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// NextSteps separates readiness from the work status recorded on each action.
type NextSteps struct {
	Actions        []QuickWin          `json:"actions"`
	Investigations []NextInvestigation `json:"investigations"`
	Findings       []NextFinding       `json:"market_findings"`
}

type NextInvestigation struct {
	analysis.Investigation
	SetID   string    `json:"set_id"`
	SetName string    `json:"set_name"`
	RunID   string    `json:"run_id"`
	Legacy  *QuickWin `json:"legacy,omitempty"`
}

type NextFinding struct {
	analysis.Finding
	SetID   string `json:"set_id"`
	SetName string `json:"set_name"`
	RunID   string `json:"run_id"`
}

func emptyNextSteps() NextSteps {
	return NextSteps{Actions: []QuickWin{}, Investigations: []NextInvestigation{}, Findings: []NextFinding{}}
}

// NextSteps reads the latest completed analysis of each active prompt set. It does
// no analysis, fetching or provider calls while serving the dashboard.
func (a *App) NextSteps(ctx context.Context) (NextSteps, error) {
	out := emptyNextSteps()
	all, err := a.quickWins(ctx)
	if err != nil {
		return out, err
	}
	for _, q := range all {
		if q.Verification.Status == "verified" {
			out.Actions = append(out.Actions, q)
		} else {
			out.Investigations = append(out.Investigations, legacyInvestigation(q))
		}
	}
	for _, s := range a.Sets() {
		var id, body string
		err := a.DB.QueryRowContext(ctx, `SELECT id, fixes FROM runs WHERE set_id=? AND fixes IS NOT NULL AND status='done' ORDER BY created_at DESC LIMIT 1`, s.ID).Scan(&id, &body)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return out, err
		}
		an, err := analysis.DecodeAnalysis(body)
		if err != nil {
			return out, fmt.Errorf("analysis %s: %w", id, err)
		}
		r, err := a.Runs.Get(ctx, id)
		if err != nil {
			return out, err
		}
		for _, inv := range an.Investigations {
			inv.Prompts, inv.PromptIDs = currentAdvicePrompts(inv.Prompts, inv.PromptIDs, r.Snapshot.Set, s)
			if len(inv.Prompts) > 0 {
				out.Investigations = append(out.Investigations, NextInvestigation{Investigation: inv, SetID: s.ID, SetName: s.Name, RunID: id})
			}
		}
		for _, f := range an.Findings {
			f.Prompts, f.PromptIDs = currentAdvicePrompts(f.Prompts, f.PromptIDs, r.Snapshot.Set, s)
			if len(f.Prompts) > 0 {
				out.Findings = append(out.Findings, NextFinding{Finding: f, SetID: s.ID, SetName: s.Name, RunID: id})
			}
		}
	}
	out.Actions = a.groupQuickWins(ctx, out.Actions)
	return out, nil
}

// RunNextSteps preserves the historical run's prompt numbering and work history.
func (a *App) RunNextSteps(ctx context.Context, id string) (NextSteps, error) {
	out := emptyNextSteps()
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return out, err
	}
	var body sql.NullString
	var at sql.NullString
	if err := a.DB.QueryRowContext(ctx, `SELECT fixes, fixes_at FROM runs WHERE id=?`, id).Scan(&body, &at); err != nil {
		return out, err
	}
	if !body.Valid {
		return out, nil
	}
	an, err := analysis.DecodeAnalysis(body.String)
	if err != nil {
		return out, err
	}
	tried := a.FixAttempts(ctx)
	for _, f := range an.Fixes {
		if current, ok := a.Set(r.SetID); ok {
			for _, n := range f.Prompts {
				if n < 1 || n > len(r.Snapshot.Set.Prompts) {
					continue
				}
				old := r.Snapshot.Set.Prompts[n-1]
				p, exists := current.Prompt(old.ID)
				if !exists || p.SHA() != old.SHA() || p.Info() != old.Info() {
					f.Verification.Status = "unverified"
					f.Verification.Summary = "A target question changed or was retired; review its current relevance before implementation."
				}
			}
		} else {
			f.Verification.Status = "unverified"
			f.Verification.Summary = "The prompt set is no longer active; review whether this work still serves a current decision."
		}
		q := QuickWin{Fix: f, SetID: r.SetID, SetName: r.Snapshot.Set.Name, Tier: string(r.Snapshot.Set.Tier), RunID: id,
			Status: tried.Status(id, f, at.String), Attempts: tried.On(id, f.Rank), Earlier: tried.Earlier(id, f)}
		q.Attempts, q.Earlier = readOnlyWorkHistory(q.Attempts), readOnlyWorkHistory(q.Earlier)
		// Triage only regenerates an implementation brief for verified work.
		q.Fix = analysis.Triage(a.Cfg, r.Snapshot.Set, f)
		if q.Verification.Status == "verified" {
			out.Actions = append(out.Actions, q)
		} else {
			out.Investigations = append(out.Investigations, legacyInvestigation(q))
		}
	}
	for _, inv := range an.Investigations {
		out.Investigations = append(out.Investigations, NextInvestigation{Investigation: inv, SetID: r.SetID, SetName: r.Snapshot.Set.Name, RunID: id})
	}
	for _, f := range an.Findings {
		out.Findings = append(out.Findings, NextFinding{Finding: f, SetID: r.SetID, SetName: r.Snapshot.Set.Name, RunID: id})
	}
	return out, nil
}

func legacyInvestigation(q QuickWin) NextInvestigation {
	q.ClaudePrompt = ""
	why := q.Verification.Summary
	if q.Verification.Context == "" {
		why = "Verification was not recorded for this earlier suggestion."
	}
	why += " Its work status is preserved."
	return NextInvestigation{Investigation: analysis.Investigation{
		Title: q.Title, Target: q.Target, Prompts: q.Prompts, PromptIDs: q.PromptIDs,
		Why:      why,
		Check:    "Read the current artifact and relevant product facts. Check whether the change already exists, whether it fits the brand's positioning, and any eligibility rules. Do not implement it during this check.",
		Decision: "Keep only a verified, in-scope change as an action; close an already completed or unsuitable suggestion, or record a supported market finding.",
		Evidence: q.Evidence,
	}, SetID: q.SetID, SetName: q.SetName, RunID: q.RunID, Legacy: &q}
}

func currentAdvicePrompts(ns []int, ids []string, then, now sets.Set) ([]int, []string) {
	if len(ids) == 0 {
		for _, n := range ns {
			if n > 0 && n <= len(then.Prompts) {
				ids = append(ids, then.Prompts[n-1].ID)
			}
		}
	}
	var nums []int
	var kept []string
	for i, p := range now.Prompts {
		for _, id := range ids {
			if p.ID == id {
				nums = append(nums, i+1)
				kept = append(kept, id)
				break
			}
		}
	}
	return nums, kept
}

// An attempt preserves the submitted work, not an executable historical handoff.
func readOnlyWorkHistory(items []Attempt) []Attempt {
	out := append([]Attempt(nil), items...)
	for i := range out {
		out[i].Fix.ClaudePrompt = ""
	}
	return out
}
