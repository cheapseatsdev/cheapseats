package server

import (
	"net/http"

	"github.com/cheapseatsdev/cheapseats/internal/app"
)

// nextStepsView starts with an investigation when no action has been verified yet.
func nextStepsView(requested string, steps app.NextSteps) string {
	switch requested {
	case "actions", "investigations", "market-findings":
		return requested
	}
	if len(steps.Actions) == 0 {
		if len(steps.Investigations) > 0 {
			return "investigations"
		}
		if len(steps.Findings) > 0 {
			return "market-findings"
		}
	}
	return "actions"
}

// selectNextSteps keeps the three categories separate. Action filters do not turn an
// investigation into a task, and set filtering applies to every category.
func (s *Server) selectNextSteps(steps app.NextSteps, f app.QuickWinFilter, kind string, limit int, brief bool) app.NextSteps {
	steps.Actions = s.App.FilterQuickWins(steps.Actions, f)
	investigations := make([]app.NextInvestigation, 0, len(steps.Investigations))
	for _, x := range steps.Investigations {
		if f.Set == "" || x.SetID == f.Set {
			investigations = append(investigations, x)
		}
	}
	findings := make([]app.NextFinding, 0, len(steps.Findings))
	for _, x := range steps.Findings {
		if f.Set == "" || x.SetID == f.Set {
			findings = append(findings, x)
		}
	}
	steps.Investigations, steps.Findings = investigations, findings
	if limit > 0 {
		steps.Actions = head(steps.Actions, limit)
		steps.Investigations = head(steps.Investigations, limit)
		steps.Findings = head(steps.Findings, limit)
	}
	steps.Actions = actionHandoffs(steps.Actions, brief)

	steps.Investigations = readOnlyInvestigations(steps.Investigations)

	switch kind {
	case "actions":
		steps.Investigations, steps.Findings = []app.NextInvestigation{}, []app.NextFinding{}
	case "investigations":
		steps.Actions, steps.Findings = []app.QuickWin{}, []app.NextFinding{}
	case "market_findings", "market-findings":
		steps.Actions, steps.Investigations = []app.QuickWin{}, []app.NextInvestigation{}
	}
	return steps
}

func (s *Server) apiNextSteps(w http.ResponseWriter, r *http.Request) {
	steps, err := s.App.NextSteps(r.Context())
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	q := r.URL.Query()
	writeJSON(w, s.selectNextSteps(steps, app.QuickWinFilter{Gap: q.Get("gap"), Set: q.Get("set"), Site: q.Get("site"), Status: q.Get("status"), Priority: q.Get("priority")}, q.Get("kind"), atoi(q.Get("limit")), q.Get("brief") == "true"))
}

// readOnlyInvestigations retains prior work without exposing an old implementation handoff.
func readOnlyInvestigations(items []app.NextInvestigation) []app.NextInvestigation {
	out := append([]app.NextInvestigation{}, items...)
	for i := range out {
		if old := out[i].Legacy; old != nil {
			copy := *old
			copy.ClaudePrompt = ""
			copy.Attempts, copy.Earlier = briefAttempts(copy.Attempts), briefAttempts(copy.Earlier)
			out[i].Legacy = &copy
		}
	}
	return out
}

// actionHandoffs exposes only the current action's coding prompt, never a historical
// handoff embedded in its recorded work. It does not mutate the stored attempt.
func actionHandoffs(actions []app.QuickWin, brief bool) []app.QuickWin {
	out := append([]app.QuickWin{}, actions...)
	for i := range out {
		if brief {
			out[i].ClaudePrompt = ""
		}
		out[i].Attempts, out[i].Earlier = briefAttempts(out[i].Attempts), briefAttempts(out[i].Earlier)
	}
	return out
}
