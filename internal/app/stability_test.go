package app

import (
	"reflect"
	"testing"

	"github.com/metcalfc/popcorn/internal/answer"
)

func TestCitesHost(t *testing.T) {
	ans := &answer.Answer{Citations: []answer.Link{{URL: "https://www.Docs.Acme.example/guide#x"}}}
	for host, want := range map[string]bool{"docs.acme.example": true, "acme.example": true, "cme.example": false, "beta.example": false} {
		if got := citesHost(ans, host); got != want {
			t.Errorf("citesHost(%s) = %v", host, got)
		}
	}
}

func TestBrayCurtis(t *testing.T) {
	for _, c := range []struct {
		a, b map[string]float64
		want float64
	}{
		{map[string]float64{"a": 0.5, "b": 0.5}, map[string]float64{"a": 0.5, "b": 0.5}, 1},
		{map[string]float64{"a": 1}, map[string]float64{"b": 1}, 0},
		// Shares adding up to 1: Σ min = 0.4 + 0.2 = 0.6.
		{map[string]float64{"a": 0.6, "b": 0.4}, map[string]float64{"a": 0.4, "b": 0.2, "c": 0.4}, 0.6},
		// Counts, not shares: 1 − (|6−2| + |4−8|) / (10 + 10) = 0.6.
		{map[string]float64{"a": 6, "b": 4}, map[string]float64{"a": 2, "b": 8}, 0.6},
		{nil, nil, 1},
		{map[string]float64{"a": 1}, nil, 0},
	} {
		if got := BrayCurtis(c.a, c.b); !near(got, c.want) || !near(BrayCurtis(c.b, c.a), got) {
			t.Errorf("BrayCurtis(%v, %v) = %v, want %v both ways", c.a, c.b, got, c.want)
		}
	}
}

func TestJaccard(t *testing.T) {
	a := map[string]float64{"a": 1, "b": 1, "c": 1}
	b := map[string]float64{"b": 1, "c": 1, "d": 1, "e": 1}
	if j := Jaccard(a, b); !near(j, 2.0/5) {
		t.Errorf("2 shared of 5: %v", j)
	}
	if j := Jaccard(map[string]bool{}, map[string]bool{}); j != 1 {
		t.Errorf("two empty sets: %v", j)
	}
}

// Runs that cited nothing are skipped, the chain stops at a change of method, and there's a
// score only from three transitions.
func TestStability(t *testing.T) {
	same := map[string]float64{"a.com": 0.5, "b.com": 0.5}
	runs := []HostShares{
		{Date: "2026-10-05", RunID: "r5", Shares: map[string]float64{"a.com": 0.5, "c.com": 0.3, "d.com": 0.2}},
		{Date: "2026-10-04", RunID: "r4", Shares: nil, PromptsChanged: true}, // an outage
		{Date: "2026-10-03", RunID: "r3", Shares: same},
		{Date: "2026-10-02", RunID: "r2", Shares: same, NewMethod: true},
		{Date: "2026-10-01", RunID: "r1", Shares: map[string]float64{"z.com": 1}},
	}
	s := Stability(runs)
	if s.Runs != 3 || len(s.Transitions) != 2 || s.Score != nil || s.Note == "" {
		t.Fatalf("two transitions, no score: %+v", s)
	}
	newest := s.Transitions[0]
	if newest.FromRun != "r3" || newest.ToRun != "r5" || !near(newest.Similarity, 50) || !near(newest.Overlap, 25) || !newest.PromptsChanged ||
		!reflect.DeepEqual(newest.New, []string{"c.com", "d.com"}) || !reflect.DeepEqual(newest.Gone, []string{"b.com"}) {
		t.Errorf("newest transition: %+v", newest)
	}
	if old := s.Transitions[1]; old.FromRun != "r2" || old.Similarity != 100 || old.Overlap != 100 || len(old.New)+len(old.Gone) != 0 || old.PromptsChanged {
		t.Errorf("unchanged transition: %+v", old)
	}

	runs[3].NewMethod = false
	s = Stability(runs)
	if len(s.Transitions) != 3 || s.Score == nil || !near(*s.Score, (50+100+0)/3.0) || !near(*s.MeanOverlap, (25+100+0)/3.0) || s.Note != "" {
		t.Errorf("three transitions score: %+v", s)
	}
	if s := Stability(nil); s.Runs != 0 || s.Transitions == nil || s.Score != nil {
		t.Errorf("no runs: %+v", s)
	}
}
