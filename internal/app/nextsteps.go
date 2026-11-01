package app

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// NextSteps separates readiness from the work status recorded on each action.
type NextSteps struct {
	Actions        []QuickWin          `json:"actions"`
	Investigations []NextInvestigation `json:"investigations"`
	Findings       []NextFinding       `json:"market_findings"`
	// Sets whose latest finished run's analysis failed. The other lists may still hold an
	// older run's analysis of the set, so a failure can't hide behind it.
	Errors []AnalysisError `json:"analysis_errors"`
}

// AnalysisError is a set's latest finished run whose analysis failed. ShownRunID is the run whose
// analysis the lists show for the set instead, "" when there's none, and Shown says which
// analysis that is (ShownEarlier and the rest).
type AnalysisError struct {
	SetID      string `json:"set_id"`
	SetName    string `json:"set_name"`
	RunID      string `json:"run_id"`
	ShownRunID string `json:"shown_run_id,omitempty"`
	Shown      string `json:"shown"`
	AnalysisFailure
}

// What is shown beside a failed analysis. A failed build never replaces an analysis the run
// already has, and only a failed verification on a run with none stores its result with the
// error, marked VerificationFailed. So the failed run's own stored analysis is the degraded
// result of a failed verification when it carries that mark, and otherwise an earlier, complete
// analysis that the failed rebuild left in place. The mark is the signal rather than comparing
// fixes_at with the time of the error, which the database doesn't keep.
const (
	ShownEarlier    = "earlier"    // the failed run's earlier analysis, kept
	ShownUnverified = "unverified" // the failed run's candidates as investigations, unverified
	ShownOlderRun   = "older-run"  // an older run's analysis; the failed run has none
	ShownNone       = "none"       // no analysis of the set at all
)

func shownBeside(failedRun, shownRun string, shown analysis.Analysis) string {
	switch {
	case shownRun == "":
		return ShownNone
	case shownRun != failedRun:
		return ShownOlderRun
	case shown.VerificationFailed:
		return ShownUnverified
	default:
		return ShownEarlier
	}
}

// Showing says in a sentence which analysis is shown beside the error.
func (e AnalysisError) Showing() string {
	switch e.Shown {
	case ShownEarlier:
		return "Showing the run's earlier analysis, which the failed rebuild left in place."
	case ShownUnverified:
		return "Its candidates are shown as investigations, unverified: the run has no verified analysis to show instead."
	case ShownOlderRun:
		return "Showing the analysis from run " + e.ShownRunID + " instead."
	default:
		return "There is no earlier analysis to show."
	}
}

// AnalysisFailure is a stored analysis error made readable: a provider's JSON reply becomes its
// message, with the request id kept for support.
type AnalysisFailure struct {
	Message   string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

var (
	errorMessage   = regexp.MustCompile(`"message"\s*:\s*"((?:[^"\\]|\\.)*)`) // to its closing quote, or the cut
	errorRequestID = regexp.MustCompile(`"request_id"\s*:\s*"([^"\\]+)"`)
)

// ReadAnalysisError reads a stored fixes_error. The provider's reply is cut to 400 characters
// when stored (llm.APIError), so a long one is no longer valid JSON: the message and request id
// are found by pattern, and an error with neither is returned as it is.
func ReadAnalysisError(stored string) AnalysisFailure {
	out := AnalysisFailure{Message: stored}
	i := strings.Index(stored, "{")
	if i < 0 {
		return out
	}
	body := stored[i:]
	if m := errorRequestID.FindStringSubmatch(body); m != nil {
		out.RequestID = m[1]
	}
	if m := errorMessage.FindStringSubmatch(body); m != nil {
		msg, err := strconv.Unquote(`"` + m[1] + `"`)
		if err != nil {
			msg = m[1]
		}
		out.Message = strings.TrimSpace(strings.TrimSpace(stored[:i]) + " " + msg)
	}
	return out
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
	return NextSteps{Actions: []QuickWin{}, Investigations: []NextInvestigation{}, Findings: []NextFinding{}, Errors: []AnalysisError{}}
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
		if err != nil && err != sql.ErrNoRows {
			return out, err
		}
		an, err := analysis.DecodeAnalysis(body)
		if err != nil {
			return out, fmt.Errorf("analysis %s: %w", id, err)
		}
		failed, err := a.latestAnalysisError(ctx, s, id, an)
		if err != nil {
			return out, err
		}
		if failed != nil {
			out.Errors = append(out.Errors, *failed)
		}
		if id == "" {
			continue
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

// latestAnalysisError is the set's latest finished run's analysis error, if it has one, beside
// shown, the run whose analysis the lists use for the set, and that analysis.
func (a *App) latestAnalysisError(ctx context.Context, s sets.Set, shownRun string, shown analysis.Analysis) (*AnalysisError, error) {
	var id string
	var stored sql.NullString
	err := a.DB.QueryRowContext(ctx, `SELECT id, fixes_error FROM runs WHERE set_id=? AND status='done' ORDER BY created_at DESC LIMIT 1`, s.ID).Scan(&id, &stored)
	if err == sql.ErrNoRows || err == nil && !stored.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &AnalysisError{SetID: s.ID, SetName: s.Name, RunID: id, ShownRunID: shownRun, Shown: shownBeside(id, shownRun, shown), AnalysisFailure: ReadAnalysisError(stored.String)}, nil
}

// RunNextSteps preserves the historical run's prompt numbering and work history.
func (a *App) RunNextSteps(ctx context.Context, id string) (NextSteps, error) {
	out := emptyNextSteps()
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return out, err
	}
	var body, at, failed sql.NullString
	if err := a.DB.QueryRowContext(ctx, `SELECT fixes, fixes_at, fixes_error FROM runs WHERE id=?`, id).Scan(&body, &at, &failed); err != nil {
		return out, err
	}
	an, err := analysis.DecodeAnalysis(body.String)
	if err != nil {
		return out, err
	}
	if failed.Valid {
		shown := ""
		if body.Valid {
			shown = id
		}
		out.Errors = append(out.Errors, AnalysisError{SetID: r.SetID, SetName: r.Snapshot.Set.Name, RunID: id, ShownRunID: shown, Shown: shownBeside(id, shown, an), AnalysisFailure: ReadAnalysisError(failed.String)})
	}
	if !body.Valid {
		return out, nil
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
