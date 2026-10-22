package run

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/sets"
)

// Adaptive sampling ([sampling] adaptive): before a run starts, up to max_extra extra answers go to
// the prompts whose named rate is least certain, judged from earlier runs with the same method. It's
// all decided before the first answer, never from this run's own answers, so stopping early can't
// bias a rate.
//
// A prompt is the unit: one chosen for extras is asked twice over, every answerer and track getting
// as many extra samples as it planned. So within a prompt every answerer and track keeps its weight,
// and the prompt's rate means what it always did, only measured more closely. (Totals pooled over a
// run's answers do weigh a doubled prompt twice; per-prompt means, which the trends use, don't.)
//
// Prompts are doubled in order of the variance doubling removes from the prompt's rate: the mean of
// its cells' p(1-p), weighted by planned samples, over its planned answers. p is the Wilson centre
// of a cell's earlier answers, so a cell with little history leans to 0.5 and counts as uncertain,
// and one named in 0 of 40 adds nothing. A prompt is doubled only if its answers fit in what's left
// of max_extra and, for API answerers, of today's budget; extras are asked after every planned
// answer, and any not done when the planned ones are are dropped (see execute).

// Window is how far back adaptive sampling looks for earlier answers.
const Window = 28 * 24 * time.Hour

// Sampling is what adaptive sampling decided for a run.
type Sampling struct {
	MaxExtra int `json:"max_extra"`
	// The prompts doubled.
	Prompts []string `json:"prompts"`
	// Earlier runs with the same method in the window, and the answers from them it used.
	PriorRuns    int `json:"prior_runs"`
	PriorAnswers int `json:"prior_answers"`
	// The cells of those prompts, each with its planned and extra samples.
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
	var prompts []promptCells
	planned := 0.0
	for _, p := range set.Prompts {
		pc := promptCells{id: p.ID}
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
				pc.cells = append(pc.cells, c)
				pc.cost += float64(base) * perAnswer(a)
			}
		}
		planned += pc.cost
		prompts = append(prompts, pc)
	}
	money := math.Inf(1)
	if d := e.Cfg.Budget.DailyUSD; d > 0 {
		today, err := e.DB.SpentToday(ctx)
		if err != nil {
			return nil, err
		}
		money = d - today - planned
	}
	for _, pc := range allocate(prompts, sp.MaxExtra, money) {
		sp.Prompts = append(sp.Prompts, pc.id)
		for _, c := range pc.cells {
			c.Extra = c.Base
			sp.Cells = append(sp.Cells, c)
		}
	}
	return sp, nil
}

// promptCells is one prompt's cells, and what asking all of them once more costs.
type promptCells struct {
	id    string
	cells []Cell
	cost  float64
}

// answers is how many answers the prompt plans: what doubling it adds.
func (p promptCells) answers() int {
	n := 0
	for _, c := range p.cells {
		n += c.Base
	}
	return n
}

// gain is the variance of the prompt's rate that doubling it removes: half the mean per-answer
// p(1-p) of its cells over its planned answers.
func (p promptCells) gain() float64 {
	n := float64(p.answers())
	if n == 0 {
		return 0
	}
	v := 0.0
	for _, c := range p.cells {
		q, _, _ := wilson(c.PriorNamed, c.PriorN)
		v += float64(c.Base) * q * (1 - q)
	}
	return v / n / n / 2
}

// allocate picks the prompts to double, largest gain first, while their answers fit in limit and
// their cost in money. It returns them in the set's order.
func allocate(prompts []promptCells, limit int, money float64) []promptCells {
	order := make([]int, len(prompts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return prompts[order[a]].gain() > prompts[order[b]].gain() })
	chosen := make([]bool, len(prompts))
	for _, i := range order {
		p := prompts[i]
		if n := p.answers(); n > 0 && n <= limit && p.cost <= money && p.gain() > 0 {
			chosen[i] = true
			limit -= n
			money -= p.cost
		}
	}
	var out []promptCells
	for i, p := range prompts {
		if chosen[i] {
			out = append(out, p)
		}
	}
	return out
}

// IsExtra reports whether a result's sample is one of adaptive sampling's extras rather than a
// planned one.
func (s *Sampling) IsExtra(promptID, answerer string, t answer.Track, sample int) bool {
	if s == nil {
		return false
	}
	for _, c := range s.Cells {
		if c.PromptID == promptID && c.Answerer == answerer && c.Track == t {
			return sample >= c.Base
		}
	}
	return false
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
