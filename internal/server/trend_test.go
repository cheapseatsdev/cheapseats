package server

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/app"
)

func TestTrend(t *testing.T) {
	day := func(date string, v float64) app.Day {
		d := app.Day{Date: date, RunID: "r-" + date, Window: 1}
		d.Mean.Visible, d.Rolling.Visible, d.RollingLo, d.RollingHi = v, v, v-.1, v+.1
		return d
	}
	one := trend(app.Ranking{Days: []app.Day{day("2026-10-01", .5)}}, "")
	if !strings.Contains(string(one), "trend appears from the second") {
		t.Fatalf("one day: %s", one)
	}
	// Newest first, as Rankings returns them: a prompt change on the 3rd, a new method on the 4th.
	d3, d4 := day("2026-10-03", .4), day("2026-10-04", .6)
	d3.Window = 3
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
		`"label":"named, 7-day window · 3 measured days"`,
		`"label":"named, 7-day window · 1 measured day"`,
		`Latest average: 1 measured day in the 7-day window.`,
		`Line restarts on 2026-10-04: answers were asked or graded differently.`,
		`<title>2026-10-04: answers were asked or graded differently; line restarts</title>`,
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
	if !strings.Contains(rec, `aria-label="Recommended in search answers, 7-day average`) || !strings.Contains(rec, `"label":"recommended, 7-day window · 3 measured days"`) || !strings.Contains(rec, `"band":"5%–15%"`) {
		t.Errorf("recommended trend:\n%s", rec)
	}
	// The line breaks at the new method: one segment of three days, the last day alone.
	if n := strings.Count(got, `<polyline class="line"`); n != 1 {
		t.Errorf("%d line segments, want 1", n)
	}
	if strings.Contains(got, "</script>\"") || strings.Count(got, "</script>") != 1 {
		t.Errorf("chart data must not be able to close its script tag:\n%s", got)
	}
}

func TestTrendSingletonMethodSegments(t *testing.T) {
	day := func(date string, rate, lo, hi float64, restart bool) app.Day {
		return app.Day{Date: date, NewMethod: restart, Window: 1, WindowDays: 7,
			Mean: app.Mean{Visible: rate}, Rolling: app.Mean{Visible: rate}, RollingLo: lo, RollingHi: hi}
	}
	// Singleton methods at the beginning, middle and end, including consecutive resets.
	days := []app.Day{
		day("2026-10-05", .8, .7, .9, true),
		day("2026-10-04", .6, .45, .75, true),
		day("2026-10-03", .4, .3, .5, false),
		day("2026-10-02", .2, .1, .3, true),
		day("2026-10-01", 0, 0, 0, false),
	}
	days[2].Window = 2
	got := string(trend(app.Ranking{Track: "search", Days: days}, "named"))
	var svg struct {
		Polylines []struct {
			Points string `xml:"points,attr"`
		} `xml:"polyline"`
		Paths []struct {
			Class string `xml:"class,attr"`
			D     string `xml:"d,attr"`
		} `xml:"path"`
		Circles []struct {
			Class string `xml:"class,attr"`
			X     string `xml:"cx,attr"`
			Y     string `xml:"cy,attr"`
		} `xml:"circle"`
	}
	start, end := strings.Index(got, "<svg "), strings.Index(got, "</svg>")
	if start < 0 || end < 0 {
		t.Fatalf("missing SVG: %s", got)
	}
	if err := xml.Unmarshal([]byte(got[start:end+len("</svg>")]), &svg); err != nil {
		t.Fatal(err)
	}
	if len(svg.Polylines) != 1 || svg.Polylines[0].Points != "176.0,134.0 312.0,104.0" {
		t.Errorf("only October 2 and 3 share a method; got lines %+v", svg.Polylines)
	}
	var intervals []string
	for _, p := range svg.Paths {
		if p.Class == "line interval" {
			intervals = append(intervals, p.D)
		}
	}
	// Each isolated day's interval stays at its own x coordinate, including a zero-width band.
	for i, want := range []string{"M40.0 164.0 V164.0", "M448.0 51.5 V96.5", "M584.0 29.0 V59.0"} {
		if len(intervals) <= i || !strings.Contains(intervals[i], want) {
			t.Errorf("singleton interval %d: want %q in %v", i, want, intervals)
		}
	}
	if len(intervals) != 3 {
		t.Errorf("%d singleton intervals, want 3", len(intervals))
	}
	var points []string
	for _, p := range svg.Circles {
		if p.Class == "end" {
			points = append(points, p.X+","+p.Y)
		}
	}
	if strings.Join(points, " ") != "40.0,164.0 448.0,74.0 584.0,44.0" {
		t.Errorf("singleton rolling points must remain visible, with no duplicate final point: %v", points)
	}
	if !strings.Contains(got, "Line restarts on 2026-10-02, 2026-10-04, 2026-10-05: answers were asked or graded differently.") {
		t.Errorf("missing visible explanations for consecutive method resets: %s", got)
	}
}

func TestTrendCoverageCountsMeasuredDays(t *testing.T) {
	// Gaps are calendar spacing, not missing zero values. Three observed days contribute
	// to the latest average, although the chart spans five calendar days.
	days := []app.Day{
		{Date: "2026-10-05", Window: 3, WindowDays: 7},
		{Date: "2026-10-04", Window: 2, WindowDays: 7},
		{Date: "2026-10-01", Window: 1, WindowDays: 7},
	}
	got := string(trend(app.Ranking{Track: "recall", Days: days}, "named"))
	for _, want := range []string{
		`<polyline class="line" points="40.0,164.0 448.0,164.0 584.0,164.0"/>`,
		`"label":"named, 7-day window · 3 measured days"`,
		`Latest average: 3 measured days in the 7-day window.`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "Line restarts") {
		t.Error("unmeasured days alone must not claim the method changed")
	}
}
