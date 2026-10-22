package app

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/run"
)

// Usable is one provider Popcorn calls (an answerer, the grader, the analyst) and whether this
// machine can call it: its API key is set, or its CLI is on the PATH.
type Usable struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Usable   bool   `json:"usable"`
	Missing  string `json:"missing,omitempty"`
}

// Readiness is what popcorn check says about the providers, for the front door: which answerers
// a run would ask (the others are skipped), and whether grading and the fix list can run here.
type Readiness struct {
	Answerers []Usable `json:"answerers"`
	Grader    Usable   `json:"grader"`
	Analyst   Usable   `json:"analyst"`
	// A run can start: at least one answerer and the grader are usable.
	CanRun bool `json:"can_run"`
}

func (a *App) Readiness() Readiness {
	use := func(id, provider string) Usable {
		m := config.Missing(provider)
		return Usable{ID: id, Provider: provider, Usable: m == "", Missing: m}
	}
	r := Readiness{Answerers: []Usable{}, Grader: use("grader", a.Cfg.Grader.Via()), Analyst: use("analyst", a.Cfg.Analyst.Via())}
	some := false
	for _, x := range a.Cfg.Answerers {
		u := use(x.ID, x.Provider)
		some = some || u.Usable
		r.Answerers = append(r.Answerers, u)
	}
	r.CanRun = some && r.Grader.Usable
	return r
}

// Plan is a dry run: what starting a run over a set would do now, and roughly what it would cost,
// without creating it.
type Plan struct {
	DryRun  bool   `json:"dry_run"`
	Set     string `json:"set"`
	Tier    string `json:"tier"`
	Prompts int    `json:"prompts"`
	// Answers per answerer and track, and the configured answerers a run would leave out.
	Answerers []PlanAnswerer `json:"answerers"`
	Skipped   []Usable       `json:"skipped"`
	Answers   int            `json:"answers"`
	// Of Answers, the extras adaptive sampling would add to the least certain prompts.
	Extra int `json:"adaptive_extra"`
	// Estimates: answers with their grading, then the fix list and themes after the run, from the
	// average cost of recent ones (priced by [pricing]); a subscription call costs nothing.
	AnswersUSD  float64 `json:"est_answers_usd"`
	AnalysisUSD float64 `json:"est_analysis_usd"`
	TotalUSD    float64 `json:"est_total_usd"`
	SpentToday  float64 `json:"spent_today_usd"`
	DailyBudget float64 `json:"daily_budget_usd"`
	// What's left of today's budget (UTC); absent with no daily budget.
	Remaining *float64 `json:"remaining_today_usd,omitempty"`
	// The estimate fits what's left today. A run that doesn't stops when the money is gone, with
	// status budget, and finishes when retried the next day.
	FitsBudget bool `json:"fits_budget"`
	// The set's run in progress: start_run would refuse with it.
	Running string `json:"run_in_progress,omitempty"`
	// Why start_run would refuse, if it would.
	Blocked string `json:"blocked,omitempty"`
}

type PlanAnswerer struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Search   int    `json:"search_answers"`
	Recall   int    `json:"recall_answers"`
	Extra    int    `json:"adaptive_extra"`
	// The estimate per answer, grading included, and where it comes from.
	PerAnswer float64 `json:"est_usd_per_answer"`
	Basis     string  `json:"cost_basis"`
	USD       float64 `json:"est_usd"`
}

// DryRun plans a run over the set without recording anything.
func (a *App) DryRun(ctx context.Context, setID string) (Plan, error) {
	s, ok := a.Set(setID)
	if !ok {
		return Plan{}, fmt.Errorf("no prompt set %q", setID)
	}
	pv, err := a.Runs.Preview(ctx, s)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{DryRun: true, Set: s.ID, Tier: string(s.Tier), Prompts: len(s.Prompts), Answerers: []PlanAnswerer{}, Skipped: []Usable{}, DailyBudget: a.Cfg.Budget.DailyUSD}
	for _, id := range pv.Snapshot.Skipped {
		for _, x := range a.Cfg.Answerers {
			if x.ID == id {
				p.Skipped = append(p.Skipped, Usable{ID: id, Provider: x.Provider, Missing: config.Missing(x.Provider)})
			}
		}
	}
	since := time.Now().UTC().Add(-run.Window).Format(time.RFC3339)
	method := run.Method(pv.Snapshot)
	for _, x := range pv.Snapshot.Answerers {
		pa := PlanAnswerer{ID: x.ID, Provider: x.Provider, Model: x.Model}
		for _, c := range pv.Cells {
			if c.Answerer != x.ID {
				continue
			}
			if c.Track == answer.Search {
				pa.Search += c.Planned + c.Extra
			} else {
				pa.Recall += c.Planned + c.Extra
			}
			pa.Extra += c.Extra
		}
		// By answerer, not model: results keep the model the provider reported (a dated snapshot
		// id), rarely the configured name. Runs asked and graded the same way first, then any.
		// grade_cost_usd also carries any re-grade's cost, so grading may be overestimated.
		var n int
		var ans, grd float64
		for _, m := range []string{method, ""} {
			a.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(x.answer_cost_usd), 0), COALESCE(SUM(x.grade_cost_usd), 0)
				FROM results x JOIN runs r ON r.id = x.run_id
				WHERE x.answerer=? AND x.status='done' AND x.finished_at>=? AND (?='' OR r.method=?)`, x.ID, since, m, m).Scan(&n, &ans, &grd)
			if n > 0 {
				break
			}
		}
		switch {
		case n > 0:
			pa.PerAnswer, pa.Basis = (ans+grd)/float64(n), fmt.Sprintf("average of %d answers in the last %d days, grading included", n, int(run.Window.Hours()/24))
		case config.Subscription(x.Provider) && a.Cfg.Grader.Via() == config.ClaudeCode:
			pa.Basis = "subscription CLI, grading on the claude CLI: nothing billed"
		default:
			pa.PerAnswer, pa.Basis = a.Runs.Budget.Allowance(), "no recent answers: the budget's reservation per answer, an upper bound"
		}
		pa.USD = pa.PerAnswer * float64(pa.Search+pa.Recall)
		p.Answers += pa.Search + pa.Recall
		p.Extra += pa.Extra
		p.AnswersUSD += pa.USD
		p.Answerers = append(p.Answerers, pa)
	}
	// The fix list (and the theme pass) after the run, from recent runs' analysis cost.
	var runs int
	var analysis float64
	a.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(analysis_cost_usd), 0) FROM runs WHERE status='done' AND fixes_at IS NOT NULL AND created_at>=?`, since).Scan(&runs, &analysis)
	switch {
	case runs > 0:
		p.AnalysisUSD = analysis / float64(runs)
	case a.Cfg.Analyst.Via() != config.ClaudeCode:
		p.AnalysisUSD = a.Runs.Budget.Allowance()
	}
	p.TotalUSD = round2(p.AnswersUSD + p.AnalysisUSD)
	p.AnswersUSD, p.AnalysisUSD = round2(p.AnswersUSD), round2(p.AnalysisUSD)
	spent, _ := a.DB.SpentToday(ctx)
	p.SpentToday = round2(spent)
	p.FitsBudget = true
	if d := a.Cfg.Budget.DailyUSD; d > 0 {
		left := round2(math.Max(0, d-spent))
		p.Remaining, p.FitsBudget = &left, p.TotalUSD <= left
	}
	a.DB.QueryRowContext(ctx, `SELECT id FROM runs WHERE set_id=? AND status IN ('queued','running') ORDER BY created_at LIMIT 1`, s.ID).Scan(&p.Running)
	switch rd := a.Readiness(); {
	case p.Running != "":
		p.Blocked = "this set already has a run in progress: " + p.Running
	case len(pv.Snapshot.Answerers) == 0:
		p.Blocked = "no answerer is available: set ANTHROPIC_API_KEY or OPENAI_API_KEY, or install the claude or codex CLI"
	case !rd.Grader.Usable:
		p.Blocked = fmt.Sprintf("grading needs the grader's provider (%s): %s", rd.Grader.Provider, rd.Grader.Missing)
	}
	return p, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
