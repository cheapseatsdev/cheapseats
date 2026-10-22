package server

import (
	"strings"
	"testing"

	"github.com/metcalfc/popcorn/internal/app"
)

func TestTrend(t *testing.T) {
	day := func(date string, v float64) app.Day {
		d := app.Day{Date: date, RunID: "r-" + date}
		d.Mean.Visible, d.Rolling.Visible, d.RollingLo, d.RollingHi = v, v, v-.1, v+.1
		return d
	}
	one := trend(app.Ranking{Days: []app.Day{day("2026-10-01", .5)}}, "")
	if !strings.Contains(string(one), "trend appears from the second") {
		t.Fatalf("one day: %s", one)
	}
	// Newest first, as Rankings returns them: a prompt change on the 3rd, a new method on the 4th.
	d3, d4 := day("2026-10-03", .4), day("2026-10-04", .6)
	d3.Change = &app.Change{Added: []string{"p"}, Models: []app.ModelChange{{Answerer: "a", From: "</script>", To: "m2"}}}
	d4.NewMethod = true
	d3.Compare = &app.Compare{Common: 3, Of: 3, Visible: .1, VisibleMargin: .2, WithinNoise: true}
	marks := []app.AttemptMark{{Date: "2026-10-02", State: "proposed", Title: "Write <the> guide", URL: "https://github.com/x/y/pull/1"}, {Date: "2025-01-01", State: "proposed", Title: "too early", URL: "https://x"}}
	rk := app.Ranking{Track: "search", Days: []app.Day{d4, d3, day("2026-10-02", .3), day("2026-10-01", .2)}, Attempts: marks}
	got := string(trend(rk, ""))
	for _, want := range []string{
		`aria-label="Named in search answers, 7-day average, 20% on 2026-10-01 to 60% on 2026-10-04, over 4 days"`,
		`class="mark"`, `class="mark method"`, `"notes":["+1 prompt"`,
		`named +10 pts ±20 vs the day before, within noise`,
		`<a href="https://github.com/x/y/pull/1"><path class="fixmark proposed"`, `fix proposed: Write \u003cthe\u003e guide`, `<title>fix proposed: Write &lt;the&gt; guide`,
		`"label":"named, 7-day"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "too early") {
		t.Error("an attempt before the chart's first day is drawn")
	}
	// Recommended draws its own band and labels itself.
	d3.Bands.Recommended = app.Interval{Lo: .05, Hi: .15}
	rec := string(trend(app.Ranking{Track: "search", Days: []app.Day{d4, d3, day("2026-10-02", .3), day("2026-10-01", .2)}}, "recommended"))
	if !strings.Contains(rec, `aria-label="Recommended in search answers, 7-day average`) || !strings.Contains(rec, `"label":"recommended, 7-day"`) || !strings.Contains(rec, `"band":"5%–15%"`) {
		t.Errorf("recommended trend:\n%s", rec)
	}
	// The line breaks at the new method: one segment of three days, the last day alone (no line).
	if n := strings.Count(got, `<polyline class="line"`); n != 1 {
		t.Errorf("%d line segments, want 1", n)
	}
	if strings.Contains(got, "</script>\"") || strings.Count(got, "</script>") != 1 {
		t.Errorf("chart data must not be able to close its script tag:\n%s", got)
	}
}
