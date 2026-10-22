package app

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// Citation stability: how much the hosts answers cite move from one run to the next. A run's
// citations are a vector of host citation shares (each host's share of the run's citations, so
// the vector adds up to 1); two runs are compared by the Bray–Curtis similarity of their vectors
// and the Jaccard overlap of the hosts cited at all. Shares that hold still say the answers lean
// on the same sources run after run, so a host entering or leaving the cited set means something;
// shares that churn say one run's sources are a sample, and a single run's sources table is noise.

// StabilityMinTransitions is how many run-to-run transitions a set needs before its stability gets
// a headline score: one or two can be a fluke either way.
const StabilityMinTransitions = 3

// HostShares is one run's citations by host, for stability.
type HostShares struct {
	Date   string
	RunID  string
	Shares map[string]float64 // host -> share of the run's citations
	// Asked or graded differently from the run before, so the chain stops here.
	NewMethod      bool
	PromptsChanged bool // it asked different prompts from the run before
}

// Transition is one run against the run before it.
type Transition struct {
	From    string `json:"from"` // the earlier run's day
	To      string `json:"to"`
	FromRun string `json:"from_run"`
	ToRun   string `json:"to_run"`
	// Bray–Curtis similarity of the two runs' host citation shares, ×100: 100 when every host
	// kept its share, 0 when no host was cited in both.
	Similarity float64 `json:"similarity"`
	// Jaccard overlap of the hosts cited, ×100: hosts cited in both over hosts cited in either.
	Overlap float64 `json:"overlap"`
	// Hosts cited this run and not the one before, and the other way round, by share.
	New  []string `json:"new"`
	Gone []string `json:"gone"`
	// The run asked different prompts from the one before: some churn is the questions.
	PromptsChanged bool `json:"prompts_changed,omitempty"`
}

// CitationStability is a set's citation stability on one track.
type CitationStability struct {
	SetID    string `json:"set_id"`
	Track    string `json:"track"`
	LastDays int    `json:"last_days"`
	// Runs with citations in the newest same-method stretch, and the transitions between them,
	// newest first.
	Runs        int          `json:"runs"`
	Transitions []Transition `json:"transitions"`
	// Mean Similarity and Overlap over the transitions, once there are StabilityMinTransitions.
	Score       *float64 `json:"score,omitempty"`
	MeanOverlap *float64 `json:"overlap,omitempty"`
	// What it's short of, when it has no score.
	Note string `json:"note,omitempty"`
}

// BrayCurtis is the Bray–Curtis similarity of two non-negative vectors, keyed by component:
// 1 − Σ|a−b| / Σ(a+b), so 1 when they're the same and 0 when no key is in both. For vectors
// that each add up to 1 it's Σ min(a, b), the share the two have in common. Two empty vectors
// are the same: 1.
func BrayCurtis(a, b map[string]float64) float64 {
	diff, sum := 0.0, 0.0
	for k, x := range a {
		diff += math.Abs(x - b[k])
		sum += x + b[k]
	}
	for k, y := range b {
		if _, ok := a[k]; !ok {
			diff += y
			sum += y
		}
	}
	if sum == 0 {
		return 1
	}
	return 1 - diff/sum
}

// Jaccard is the overlap of two sets of keys: in both over in either, 1 when both are empty.
func Jaccard[V any](a, b map[string]V) float64 {
	both, either := 0, len(b)
	for k := range a {
		if _, ok := b[k]; ok {
			both++
		} else {
			either++
		}
	}
	if either == 0 {
		return 1
	}
	return float64(both) / float64(either)
}

// Stability works out transitions over runs, newest first: each run with citations against the
// next older one with citations, back to a change of method (the run that changed it is the
// oldest compared). Runs that cited nothing (an outage, or a track without search) are skipped
// rather than read as every host leaving. The score needs StabilityMinTransitions.
func Stability(runs []HostShares) CitationStability {
	var chain []HostShares
	for _, r := range runs {
		if len(r.Shares) > 0 {
			chain = append(chain, r)
		}
		if r.NewMethod {
			break
		}
	}
	out := CitationStability{Runs: len(chain), Transitions: []Transition{}}
	sim, ovl := 0.0, 0.0
	for i := 0; i+1 < len(chain); i++ {
		now, prev := chain[i], chain[i+1]
		t := Transition{From: prev.Date, To: now.Date, FromRun: prev.RunID, ToRun: now.RunID,
			Similarity: 100 * BrayCurtis(now.Shares, prev.Shares), Overlap: 100 * Jaccard(now.Shares, prev.Shares),
			New: onlyIn(now.Shares, prev.Shares), Gone: onlyIn(prev.Shares, now.Shares), PromptsChanged: now.PromptsChanged}
		// A run skipped between the two may have changed the prompts.
		for _, r := range runs {
			if r.Date > prev.Date && r.Date < now.Date && r.PromptsChanged {
				t.PromptsChanged = true
			}
		}
		out.Transitions = append(out.Transitions, t)
		sim, ovl = sim+t.Similarity, ovl+t.Overlap
	}
	if n := len(out.Transitions); n >= StabilityMinTransitions {
		s, o := sim/float64(n), ovl/float64(n)
		out.Score, out.MeanOverlap = &s, &o
	} else {
		out.Note = fmt.Sprintf("needs %d run-to-run transitions of the same method to score; has %d", StabilityMinTransitions, n)
	}
	return out
}

// onlyIn is the keys of a not in b, largest share first.
func onlyIn(a, b map[string]float64) []string {
	out := []string{}
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if a[out[i]] != a[out[j]] {
			return a[out[i]] > a[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// CitationStability is the set's citation stability on track over the last lastDays days, from
// the same daily runs the trends use (each day's newest finished run).
func (a *App) CitationStability(ctx context.Context, s sets.Set, track string, lastDays int) (CitationStability, error) {
	byTrack, err := a.series(ctx, s, lastDays, Windows[0])
	if err != nil {
		return CitationStability{SetID: s.ID, Track: track, LastDays: lastDays, Transitions: []Transition{}}, err
	}
	var runs []HostShares
	for _, d := range byTrack[track] {
		runs = append(runs, HostShares{Date: d.Date, RunID: d.RunID, Shares: d.shares, NewMethod: d.NewMethod, PromptsChanged: d.Change.PromptsChanged()})
	}
	out := Stability(runs)
	out.SetID, out.Track, out.LastDays = s.ID, track, lastDays
	return out, nil
}
