package app

import (
	"encoding/json"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/metcalfc/popcorn/internal/report"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

// A change is tested on the smoothed variance of both sides, prompt by prompt; prompts missing
// either side are left out.
func TestPairedDiff(t *testing.T) {
	d := PairedDiff([]Hits{{3, 6}, {1, 6}}, []Hits{{5, 6}, {0, 0}})
	// 3/6 -> 5/6: D = 1/3; var = (5.5/7)(1.5/7)/6 + (3.5/7)(3.5/7)/6 = 0.06973.
	if d.Prompts != 1 || !near(d.D, 1.0/3) || !near(d.Margin, 1.96*math.Sqrt(0.069728)) {
		t.Fatalf("one prompt: %+v", d)
	}
	if !d.WithinNoise() || d.Sign() != 0 || d.String() != "+33 pts ±52" {
		t.Errorf("3/6 to 5/6 on one prompt is within noise: %+v %s", d, d)
	}
	// Seven prompts all going 0/6 -> 6/6 is not chance.
	var before, after []Hits
	for range 7 {
		before, after = append(before, Hits{0, 6}), append(after, Hits{6, 6})
	}
	if d := PairedDiff(before, after); d.Sign() != 1 || !near(d.D, 1) {
		t.Errorf("all prompts flipped: %+v", d)
	}
	if d := PairedDiff(after, before); d.Sign() != -1 {
		t.Errorf("all prompts lost: %+v", d)
	}
	if d := PairedDiff(nil, nil); d.Sign() != 0 || d.Prompts != 0 {
		t.Errorf("nothing to compare: %+v", d)
	}
}

func TestIntervals(t *testing.T) {
	// Fixed set: mean of 2/4 and 4/4 is 0.75, var (2.5/5)(2.5/5)/4 + (4.5/5)(.5/5)/4 = 0.085.
	iv := FixedSet([]Hits{{2, 4}, {4, 4}, {0, 0}})
	if m := 1.96 * math.Sqrt(0.085) / 2; !near(iv.Lo, 0.75-m) || !near(iv.Hi, 1) {
		t.Errorf("fixed set: %+v", iv)
	}
	if iv := FixedSet(nil); iv != (Interval{}) {
		t.Errorf("no cells: %+v", iv)
	}
	// Across prompts: rates 0.2, 0.4, 0.6 have s = 0.2, so se = 0.2/sqrt(3).
	iv = AcrossPrompts([]float64{.2, .4, .6}, .4)
	if m := 1.96 * .2 / math.Sqrt(3); !near(iv.Lo, .4-m) || !near(iv.Hi, .4+m) {
		t.Errorf("across prompts: %+v", iv)
	}
	if iv := AcrossPrompts([]float64{.3}, .3); iv.Lo != .3 || iv.Hi != .3 {
		t.Errorf("one rate: %+v", iv)
	}
}

// With every run drawn from the same rates, the spread between runs is what sampling gives, so
// samples are paying off; with each run all-or-nothing, it's all drift.
func TestSplitVariance(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var noise [][]Hits
	for k := range 10 {
		p := float64(k+1) / 11
		var runs []Hits
		for range 8 {
			h := 0
			for range 4 {
				if rng.Float64() < p {
					h++
				}
			}
			runs = append(runs, Hits{h, 4})
		}
		noise = append(noise, runs)
	}
	v := SplitVariance(noise)
	if v.Cells != 10 || v.Runs != 8 || v.Widening() < 1.5 {
		t.Errorf("binomial noise alone should make samples worth it: %+v widening %.2f", v, v.Widening())
	}
	drift := [][]Hits{{{0, 3}, {3, 3}, {0, 3}, {3, 3}}, {{3, 3}, {3, 3}, {0, 3}, {0, 3}}, {{1, 1}}}
	v = SplitVariance(drift)
	if v.Cells != 2 || v.Within != 0 || v.Between == 0 || !near(v.Widening(), 1) {
		t.Errorf("all-or-nothing runs are all drift: %+v widening %.2f", v, v.Widening())
	}
}

func outcomeOf(before, after []Tally, days float64) Outcome {
	o := Outcome{RunID: "r-after", Days: days, BeforeRuns: 1, AfterRuns: 1}
	for i := range before {
		o.Prompts = append(o.Prompts, PromptOutcome{PromptID: string(rune('a' + i)), Before: before[i], After: after[i]})
	}
	return o
}

func tl(n, named, rec int) Tally { return Tally{N: n, Named: named, Recommended: rec, Choice: n} }

// The old rule called any prompt that rose "moved". With six answers a prompt, one of several
// prompts rises by chance most of the time; the stratified test calls that within noise.
func TestOutcomeNoiseIsNotMoved(t *testing.T) {
	o := outcomeOf([]Tally{tl(6, 3, 1), tl(6, 2, 0), tl(6, 4, 2)}, []Tally{tl(6, 4, 2), tl(6, 2, 0), tl(6, 3, 1)}, 10)
	if s := o.Status(); s != OutcomeNoChange {
		t.Fatalf("a prompt up one and another down one: %s", s)
	}
	if s := o.Summary(); !strings.HasPrefix(s, "no detectable change (±") || !strings.Contains(s, "named +0 pts ±") || !strings.Contains(s, "a named 3/6→4/6, recommended 1/6→2/6") {
		t.Errorf("summary: %s", s)
	}

	// Simulated: seven prompts, each with its own true rate, six answers before and after, nothing
	// changed. The old rule says moved most of the time; this one about 5% at most.
	rng := rand.New(rand.NewSource(1))
	draw := func(p float64) int {
		h := 0
		for range 6 {
			if rng.Float64() < p {
				h++
			}
		}
		return h
	}
	const trials = 2000
	moved, old := 0, 0
	for range trials {
		var before, after []Tally
		for k := range 7 {
			pn := float64(k+1) / 9
			nb, na := draw(pn), draw(pn)
			rb, ra := draw(pn/2), draw(pn/2)
			before, after = append(before, tl(6, nb, min(rb, nb))), append(after, tl(6, na, min(ra, na)))
		}
		o := outcomeOf(before, after, 10)
		if o.Status() == OutcomeMoved || o.Status() == OutcomeMixed {
			moved++
		}
		for k := range before {
			if after[k].Named > before[k].Named || after[k].Recommended > before[k].Recommended {
				old++
				break
			}
		}
	}
	t.Logf("pure noise: moved %.1f%%, the old rule %.1f%%", 100*float64(moved)/trials, 100*float64(old)/trials)
	if rate := float64(moved) / trials; rate > 0.06 {
		t.Errorf("pure noise reported moved %.1f%% of the time", 100*rate)
	}
	if rate := float64(old) / trials; rate < 0.5 {
		t.Errorf("the old rule should be fooled most of the time, was %.1f%%", 100*rate)
	}
}

func TestOutcomeStatus(t *testing.T) {
	lost := []Tally{tl(6, 1, 0), tl(6, 0, 0), tl(6, 1, 0), tl(6, 0, 0)}
	won := []Tally{tl(6, 6, 5), tl(6, 5, 4), tl(6, 6, 6), tl(6, 5, 5)}
	for _, c := range []struct {
		name          string
		before, after []Tally
		days          float64
		want          string
	}{
		{"moved", lost, won, 10, OutcomeMoved},
		{"worse", won, lost, 10, OutcomeWorse},
		{"too soon", lost, won, 3, OutcomeTooEarly},
		{"too few answers", lost, []Tally{tl(2, 2, 2), tl(2, 2, 2), tl(2, 2, 2), tl(2, 2, 2)}, 10, OutcomeTooEarly},
		{"named up, recommended down", []Tally{tl(8, 0, 8), tl(8, 0, 8), tl(8, 0, 8)}, []Tally{tl(8, 8, 0), tl(8, 8, 0), tl(8, 8, 0)}, 10, OutcomeMixed},
	} {
		if got := outcomeOf(c.before, c.after, c.days).Status(); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if s := (Outcome{}).Status(); s != OutcomePending {
		t.Errorf("no run since: %s", s)
	}
	if s := (Outcome{Incomparable: true}).Summary(); !strings.HasPrefix(s, "not comparable") {
		t.Errorf("only other methods since: %s", s)
	}
	// The target turning up is evidence, not a move.
	o := outcomeOf(lost, lost, 10)
	o.TargetAfter = true
	if o.Status() != OutcomeNoChange || !strings.Contains(o.Summary(), "target not seen→seen (newly surfaced)") {
		t.Errorf("target newly surfaced: %s", o.Summary())
	}
	// So is its host entering or leaving the hosts cited.
	o.TargetHost, o.HostCitedBefore = "docs.acme.example", true
	if o.Status() != OutcomeNoChange || !strings.Contains(o.Summary(), "host docs.acme.example cited→not cited (left the cited set)") {
		t.Errorf("host left: %s", o.Summary())
	}
	o.HostCitedBefore, o.HostCitedAfter = false, true
	if !strings.Contains(o.Summary(), "not cited→cited (entered the cited set)") {
		t.Errorf("host entered: %s", o.Summary())
	}
	// Info prompts have no choice to count: recommended leaves them out.
	o = outcomeOf([]Tally{{N: 6, Named: 0}, tl(6, 0, 0)}, []Tally{{N: 6, Named: 6}, tl(6, 0, 0)}, 10)
	if _, rec := o.Changes(); rec.Prompts != 1 {
		t.Errorf("recommended over %d prompts, want 1", rec.Prompts)
	}
	// The JSON carries what it works out to, and still reads back.
	b, _ := json.Marshal(outcomeOf(lost, won, 10))
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["status"] != OutcomeMoved || m["summary"] == nil || m["named"] == nil || m["run_id"] != "r-after" {
		t.Errorf("json: %s", b)
	}
	var back Outcome
	if err := json.Unmarshal(b, &back); err != nil || back.Status() != OutcomeMoved {
		t.Errorf("read back: %v %+v", err, back)
	}
}

// dayOf is a day of one answerer whose prompts went hits/n, with named counts for one competitor.
func dayOf(date string, rank int, cells []Hits, rival []int) dayData {
	d := dayData{Day: Day{Date: date, Method: "m", Rank: rank}, prompts: map[string]string{}, info: map[string]bool{},
		models: map[string]string{"a": "x"}, samples: map[string]int{"a": 6}, tallies: map[string]map[string]*report.Tally{}, named: map[string]map[string]int{}}
	us, them := 0, 0
	for i, c := range cells {
		pid := string(rune('a' + i))
		d.order = append(d.order, pid)
		d.prompts[pid] = "sha"
		d.tallies[pid] = map[string]*report.Tally{"a": {Vis: c.Hits, Rec: c.Hits, N: c.N, Sel: c.N}}
		d.named[pid] = map[string]int{"acme": c.Hits, "rival": rival[i]}
		us, them = us+c.Hits, them+rival[i]
	}
	d.Board = []report.Voice{{Name: "Acme", Answers: us, IsBrand: true}, {Name: "Rival", Answers: them}}
	return d
}

// Day over day, a small wobble is within noise, and so is a rank move it causes; a big move isn't.
func TestCompareNoise(t *testing.T) {
	prev := dayOf("2026-10-01", 2, []Hits{{3, 6}, {2, 6}, {4, 6}}, []int{3, 3, 4})
	d := dayOf("2026-10-02", 1, []Hits{{4, 6}, {3, 6}, {4, 6}}, []int{3, 3, 4})
	compare(&d, &prev)
	if c := d.Compare; c == nil || !c.WithinNoise || !c.RecommendedWithinNoise || !near(c.Visible, 2.0/18) || c.VisibleMargin == 0 {
		t.Fatalf("compare: %+v", d.Compare)
	}
	if d.DeltaRank != 1 || !d.RankWithinNoise {
		t.Errorf("passing Rival by two answers is within noise: delta %d, noise %v", d.DeltaRank, d.RankWithinNoise)
	}

	prev = dayOf("2026-10-01", 2, []Hits{{0, 6}, {0, 6}, {1, 6}}, []int{5, 6, 5})
	d = dayOf("2026-10-02", 1, []Hits{{6, 6}, {6, 6}, {5, 6}}, []int{2, 1, 2})
	compare(&d, &prev)
	if c := d.Compare; c.WithinNoise || c.Visible <= 0 {
		t.Errorf("0/6 to 6/6 isn't noise: %+v", c)
	}
	if d.DeltaRank != 1 || d.RankWithinNoise {
		t.Errorf("a clear pass is a real rank move: delta %d, noise %v", d.DeltaRank, d.RankWithinNoise)
	}
}
