package app

import (
	"fmt"
	"math"
)

// The statistics every comparison here shares. A rate is measured per prompt, and prompts are the
// strata: a prompt's answers are a handful of draws from one question, so a change is tested
// prompt by prompt and averaged, each prompt counting once, as the rates themselves are.

// z is the 95% normal quantile.
const z = 1.96

// Hits is hits of n answers in one cell: a prompt in a run, or a prompt pooled over runs.
type Hits struct{ Hits, N int }

func (c Hits) Plus(x Hits) Hits { return Hits{c.Hits + x.Hits, c.N + x.N} }

// rate is the cell's own proportion.
func (c Hits) rate() float64 { return float64(c.Hits) / float64(c.N) }

// variance is the sampling variance of the cell's rate, from the smoothed proportion
// p~ = (h+0.5)/(n+1): p~(1-p~)/n. The smoothing keeps a cell of 0/6 or 6/6 from claiming no
// noise at all.
func (c Hits) variance() float64 {
	p := (float64(c.Hits) + 0.5) / (float64(c.N) + 1)
	return p * (1 - p) / float64(c.N)
}

// Diff is a change in a per-prompt mean rate with its 95% margin, in proportions (0.05 is 5
// points): D = mean over prompts of (p_after - p_before), Var(D) = sum of both cells' variances
// over prompts / K^2.
type Diff struct {
	D       float64 `json:"d"`
	Margin  float64 `json:"margin"`  // 1.96 standard errors
	Prompts int     `json:"prompts"` // prompts measured on both sides (K)
}

// Sign is +1 when the interval is above 0, -1 when below, 0 when it covers 0 (or there's nothing
// to compare).
func (d Diff) Sign() int {
	switch {
	case d.Prompts == 0:
		return 0
	case d.D-d.Margin > 0:
		return 1
	case d.D+d.Margin < 0:
		return -1
	}
	return 0
}

// WithinNoise is whether the change could be chance: its 95% interval covers 0.
func (d Diff) WithinNoise() bool { return d.Sign() == 0 }

// String is the change in points with its margin: "+12 pts ±9".
func (d Diff) String() string {
	return fmt.Sprintf("%+.0f pts ±%.0f", 100*d.D, 100*d.Margin)
}

// stratum is one prompt's change and that change's variance.
type stratum struct{ d, v float64 }

func combine(ss []stratum) Diff {
	if len(ss) == 0 {
		return Diff{}
	}
	var d, v float64
	for _, s := range ss {
		d, v = d+s.d, v+s.v
	}
	k := float64(len(ss))
	return Diff{D: d / k, Margin: z * math.Sqrt(v) / k, Prompts: len(ss)}
}

// PairedDiff is the stratified change from before[k] to after[k] over the prompts k measured on
// both sides; a prompt with no answers on either side is left out.
func PairedDiff(before, after []Hits) Diff {
	var ss []stratum
	for k := range before {
		b, a := before[k], after[k]
		if b.N == 0 || a.N == 0 {
			continue
		}
		ss = append(ss, stratum{a.rate() - b.rate(), a.variance() + b.variance()})
	}
	return combine(ss)
}

// Interval is a 95% interval for a rate.
type Interval struct {
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
}

func clamp(mean, margin float64) Interval {
	return Interval{math.Max(0, mean-margin), math.Min(1, mean+margin)}
}

// FixedSet is the interval for the per-prompt mean of cells, taking these prompts as the whole
// set: only the answers' sampling noise, mean ± 1.96·sqrt(Σ var_k)/K. It answers "this set,
// today", where the across-prompts band answers "questions like these".
func FixedSet(cells []Hits) Interval {
	var m, v float64
	k := 0
	for _, c := range cells {
		if c.N == 0 {
			continue
		}
		m, v, k = m+c.rate(), v+c.variance(), k+1
	}
	if k == 0 {
		return Interval{}
	}
	return clamp(m/float64(k), z*math.Sqrt(v)/float64(k))
}

// AcrossPrompts is the interval for mean from how much the rates xs (one per prompt, or per
// prompt-day) vary: mean ± 1.96·s/sqrt(n). Answers to one prompt aren't independent, so prompts,
// not answers, are the units.
func AcrossPrompts(xs []float64, mean float64) Interval {
	if len(xs) < 2 {
		return Interval{mean, mean}
	}
	var m, ss float64
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	for _, x := range xs {
		ss += (x - m) * (x - m)
	}
	se := math.Sqrt(ss/float64(len(xs)-1)) / math.Sqrt(float64(len(xs)))
	return clamp(mean, z*se)
}

// VarianceSplit is Profound's V = S/n + D for per-prompt rates measured run after run: S is the
// variance of a single answer within a run (binomial), n the answers per cell per run, and D the
// variance between runs that more samples can't remove (drift: the model, the web, the day).
type VarianceSplit struct {
	Within  float64 `json:"within"`  // mean S/n over cells: what samples reduce
	Single  float64 `json:"single"`  // mean S over cells: the within-run variance at samples=1
	Between float64 `json:"between"` // mean D over cells
	Cells   int     `json:"cells"`
	Runs    int     `json:"runs"` // the most runs any cell was seen in
}

// SplitVariance estimates the split from cells, each a cell's counts run after run (all with the
// same n > 1). Per cell, S is the mean within-run sample variance n/(n-1)·p(1-p), and D what's left
// of the variance of the run rates after S/n, floored at 0 (method of moments). Cells seen in
// fewer than two runs are skipped.
func SplitVariance(cells [][]Hits) VarianceSplit {
	var out VarianceSplit
	for _, runs := range cells {
		if len(runs) < 2 || runs[0].N < 2 {
			continue
		}
		n := float64(runs[0].N)
		var mean, s float64
		for _, c := range runs {
			p := c.rate()
			mean += p
			s += n / (n - 1) * p * (1 - p)
		}
		r := float64(len(runs))
		mean, s = mean/r, s/r
		var obs float64
		for _, c := range runs {
			obs += (c.rate() - mean) * (c.rate() - mean)
		}
		obs /= r - 1
		out.Within += s / n
		out.Single += s
		out.Between += math.Max(0, obs-s/n)
		out.Cells++
		if len(runs) > out.Runs {
			out.Runs = len(runs)
		}
	}
	if out.Cells > 0 {
		k := float64(out.Cells)
		out.Within, out.Single, out.Between = out.Within/k, out.Single/k, out.Between/k
	}
	return out
}

// Widening is how much wider a cell's interval would be at samples=1 than now:
// sqrt((S + D) / (S/n + D)).
func (v VarianceSplit) Widening() float64 {
	if v.Within+v.Between == 0 {
		return 1
	}
	return math.Sqrt((v.Single + v.Between) / (v.Within + v.Between))
}

// Homogeneity is the χ² statistic for cells' run rates against one binomial rate per cell, summed
// over the cells, with its degrees of freedom: per cell, Σ (h_r − n·p̂)² / (n·p̂(1−p̂)) over its runs,
// with runs − 1 degrees of freedom, where p̂ is the cell's pooled rate. A cell seen in fewer than
// two runs, with fewer than two answers a run, or at 0 or 1 throughout (nothing varied) adds
// nothing.
func Homogeneity(cells [][]Hits) (chi2 float64, df int) {
	for _, runs := range cells {
		if len(runs) < 2 || runs[0].N < 2 {
			continue
		}
		var h, n float64
		for _, c := range runs {
			h, n = h+float64(c.Hits), n+float64(c.N)
		}
		p := h / n
		if p <= 0 || p >= 1 {
			continue
		}
		for _, c := range runs {
			e := float64(c.N) * p
			chi2 += (float64(c.Hits) - e) * (float64(c.Hits) - e) / (e * (1 - p))
		}
		df += len(runs) - 1
	}
	return chi2, df
}

// ChiSquareP is the upper tail of the χ² distribution with df degrees of freedom at x: the chance
// of a statistic at least x by chance. 1 with no degrees of freedom.
func ChiSquareP(x float64, df int) float64 {
	if df <= 0 || x <= 0 {
		return 1
	}
	return gammaQ(float64(df)/2, x/2)
}

// gammaQ is the regularised upper incomplete gamma function Q(a, x): a series below a+1, a
// continued fraction above (Numerical Recipes, 6.2).
func gammaQ(a, x float64) float64 {
	lg, _ := math.Lgamma(a)
	if x < a+1 {
		sum, del := 1/a, 1/a
		for n := 1; n < 500; n++ {
			del *= x / (a + float64(n))
			sum += del
			if math.Abs(del) < math.Abs(sum)*1e-14 {
				break
			}
		}
		return math.Max(0, 1-sum*math.Exp(-x+a*math.Log(x)-lg))
	}
	const tiny = 1e-300
	b := x + 1 - a
	c, d := 1/tiny, 1/b
	h := d
	for i := 1; i < 500; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2
		d = an*d + b
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = b + an/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < 1e-14 {
			break
		}
	}
	return math.Min(1, math.Exp(-x+a*math.Log(x)-lg)*h)
}
