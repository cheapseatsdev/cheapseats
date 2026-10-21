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
	one := trend(app.Ranking{Days: []app.Day{day("2026-10-01", .5)}})
	if !strings.Contains(string(one), "trend appears from the second") {
		t.Fatalf("one day: %s", one)
	}
	// Newest first, as Rankings returns them: a prompt change on the 3rd, a new method on the 4th.
	d3, d4 := day("2026-10-03", .4), day("2026-10-04", .6)
	d3.Change = &app.Change{Added: []string{"p"}, Models: []app.ModelChange{{Answerer: "a", From: "</script>", To: "m2"}}}
	d4.NewMethod = true
	got := string(trend(app.Ranking{Track: "search", Days: []app.Day{d4, d3, day("2026-10-02", .3), day("2026-10-01", .2)}}))
	for _, want := range []string{
		`aria-label="Named in search answers, 7-day average, 20% on 2026-10-01 to 60% on 2026-10-04, over 4 days"`,
		`class="mark"`, `class="mark method"`, `"notes":["+1 prompt"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	// The line breaks at the new method: one segment of three days, the last day alone (no line).
	if n := strings.Count(got, `<polyline class="line"`); n != 1 {
		t.Errorf("%d line segments, want 1", n)
	}
	if strings.Contains(got, "</script>\"") || strings.Count(got, "</script>") != 1 {
		t.Errorf("chart data must not be able to close its script tag:\n%s", got)
	}
}
