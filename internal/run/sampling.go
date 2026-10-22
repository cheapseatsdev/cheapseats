package run

import (
	"context"
	"math"
	"time"

	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/sets"
)

// Adaptive sampling ([sampling] adaptive): before a run starts, up to max_extra extra answers go to
// the cells (prompt x answerer x track) whose named rate is least certain, judged from earlier runs
// with the same method. It's all decided before the first answer, never from this run's own answers,
// so stopping early can't bias a rate.
//
// Each extra goes where it most shrinks the run's total variance, sum of p(1-p)/n over cells: the
// cell with the largest p(1-p)/(n(n+1)). That's Neyman allocation (n proportional to sqrt(p(1-p)))
// one answer at a time. p is the Wilson centre of the cell's earlier answers, so a cell with little
// history leans to 0.5 and counts as uncertain, and one named in 0 of 40 gets nothing. A cell gets
// at most as many extras as it has planned samples, extras on API answerers have to fit in what's
// left of today's budget, and they're asked after every planned answer.

// Window is how far back adaptive sampling looks for earlier answers.
const Window = 28 * 24 * time.Hour

// Sampling is what adaptive sampling decided for a run.
type Sampling struct {
	MaxExtra int `json:"max_extra"`
	// Earlier runs with the same method in the window, and the answers from them it used.
	PriorRuns    int `json:"prior_runs"`
	PriorAnswers int `json:"prior_answers"`
	// The cells given extras, each with its sample count before and after.
	Cells []Cell `json:"cells"`
}

// Cell is one prompt on one answerer and track.
type Cell struct {
	PromptID string       `json:"prompt_id"`
	Answerer string       `json:"answerer"`
	Track    answer.Track `json:"track"`
	Base     int          `json:"base"`
	Extra    int          `json:"extra"`
	// Its earlier answers, how many named the brand, and the width of that rate's 95% Wilson
	// interval (1 with no history).
	PriorN     int     `json:"prior_n"`
	PriorNamed int     `json:"prior_named"`
	Width      float64 `json:"width"`
}

// Extra is how many extra samples the cell gets; nil Sampling gives none.
func (s *Sampling) Extra(promptID, answerer string, t answer.Track) int {
	if s == nil {
		return 0
	}
	for _, c := range s.Cells {
		if c.PromptID == promptID && c.Answerer == answerer && c.Track == t {
			return c.Extra
		}
	}
	return 0
}

// plan decides a new run's extras from the database.
func (e *Engine) plan(ctx context.Context, set sets.Set, snap Snapshot) (*Sampling, error) {
	sp := &Sampling{MaxExtra: e.Cfg.Sampling.MaxExtra, Cells: []Cell{}}
	type key struct{ prompt, sha, answerer, track string }
	type tally struct{ n, named int }
	prior := map[key]*tally{}
	type spend struct {
		n   int
		usd float64
	}
	spent := map[string]*spend{} // per answerer
	runs := map[string]bool{}
	since := time.Now().UTC().Add(-Window).Format(time.RFC3339)
	rows, err := e.DB.QueryContext(ctx, `SELECT x.run_id, x.prompt_id, x.prompt_sha, x.answerer, x.track, x.verdict, x.answer_cost_usd + x.grade_cost_usd
		FROM results x JOIN runs r ON r.id = x.run_id WHERE r.method = ? AND r.created_at >= ? AND x.status = 'done' AND x.verdict IS NOT NULL`, Method(snap), since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var run, prompt, sha, answerer, track, verdict string
		var cost float64
		if err := rows.Scan(&run, &prompt, &sha, &answerer, &track, &verdict, &cost); err != nil {
			rows.Close()
			return nil, err
		}
		runs[run] = true
		sp.PriorAnswers++
		k := key{prompt, sha, answerer, track}
		if prior[k] == nil {
			prior[k] = &tally{}
		}
		prior[k].n++
		if verdict == "recommended" || verdict == "mentioned" {
			prior[k].named++
		}
		if spent[answerer] == nil {
			spent[answerer] = &spend{}
		}
		spent[answerer].n++
		spent[answerer].usd += cost
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sp.PriorRuns = len(runs)
	if sp.PriorRuns == 0 {
		return sp, nil // nothing to judge by: the first run of a method takes the planned samples
	}

	// What an answer costs on each answerer, from its recent answers (grading included); with none,
	// a reservation's worth unless nothing in it is billed.
	perAnswer := func(a config.Answerer) float64 {
		if t := spent[a.ID]; t != nil && t.n > 0 {
			return t.usd / float64(t.n)
		}
		if config.Subscription(a.Provider) && e.Cfg.Grader.Via() == config.ClaudeCode {
			return 0
		}
		return e.Budget.Allowance()
	}
	var cells []Cell
	var cost []float64
	planned := 0.0
	for _, p := range set.Prompts {
		for _, a := range snap.Answerers {
			for _, t := range []answer.Track{answer.Search, answer.Recall} {
				base := e.samples(set, a, t)
				if base == 0 {
					continue
				}
				c := Cell{PromptID: p.ID, Answerer: a.ID, Track: t, Base: base}
				if x := prior[key{p.ID, p.SHA(), a.ID, string(t)}]; x != nil {
					c.PriorN, c.PriorNamed = x.n, x.named
				}
				_, lo, hi := wilson(c.PriorNamed, c.PriorN)
				c.Width = hi - lo
				cells = append(cells, c)
				cost = append(cost, perAnswer(a))
				planned += float64(base) * perAnswer(a)
			}
		}
	}
	money := math.Inf(1)
	if d := e.Cfg.Budget.DailyUSD; d > 0 {
		today, err := e.DB.SpentToday(ctx)
		if err != nil {
			return nil, err
		}
		money = d - today - planned
	}
	for _, c := range allocate(cells, cost, sp.MaxExtra, money) {
		if c.Extra > 0 {
			sp.Cells = append(sp.Cells, c)
		}
	}
	return sp, nil
}

// allocate hands out up to limit extras one at a time, each to the cell where it most reduces the
// summed variance, while its cost fits in money. cost is per answer, by cell.
func allocate(cells []Cell, cost []float64, limit int, money float64) []Cell {
	for k := 0; k < limit; k++ {
		best, score := -1, 0.0
		for i, c := range cells {
			if c.Base == 0 || c.Extra >= c.Base || cost[i] > money {
				continue
			}
			p, _, _ := wilson(c.PriorNamed, c.PriorN)
			n := float64(c.Base + c.Extra)
			if s := p * (1 - p) / (n * (n + 1)); s > score {
				best, score = i, s
			}
		}
		if best < 0 {
			break
		}
		cells[best].Extra++
		money -= cost[best]
	}
	return cells
}

// wilson is the centre of the 95% Wilson interval for hits out of n, and its bounds; with n = 0,
// 0.5 and [0, 1].
func wilson(hits, n int) (mid, lo, hi float64) {
	if n == 0 {
		return 0.5, 0, 1
	}
	const z = 1.96
	nf, p := float64(n), float64(hits)/float64(n)
	den := 1 + z*z/nf
	mid = (p + z*z/(2*nf)) / den
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / den
	return mid, math.Max(0, mid-half), math.Min(1, mid+half)
}
