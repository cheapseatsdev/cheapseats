package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/app"
)

// Chart geometry, in viewBox units.
const (
	trendW, trendH                = 640.0, 190.0
	padL, padR, padTop, padBottom = 40.0, 56.0, 14.0, 26.0
	plotW, plotH                  = trendW - padL - padR, trendH - padTop - padBottom
)

// A metric is a rate a trend can draw: its rolling value and band, and the day's own value and
// fixed-set interval.
type metric struct {
	key, label string
	rolling    func(app.Day) float64
	band       func(app.Day) app.Interval
	day        func(app.Day) float64
	today      func(app.Day) app.Interval
}

var trendMetrics = []metric{
	{"named", "named", func(d app.Day) float64 { return d.Rolling.Visible }, func(d app.Day) app.Interval { return app.Interval{Lo: d.RollingLo, Hi: d.RollingHi} },
		func(d app.Day) float64 { return d.Mean.Visible }, func(d app.Day) app.Interval { return d.Today.Visible }},
	{"recommended", "recommended", func(d app.Day) float64 { return d.Rolling.Recommended }, func(d app.Day) app.Interval { return d.Bands.Recommended },
		func(d app.Day) float64 { return d.Mean.Recommended }, func(d app.Day) app.Interval { return d.Today.Recommended }},
	{"top_pick", "top pick", func(d app.Day) float64 { return d.Rolling.TopPick }, func(d app.Day) app.Interval { return d.Bands.TopPick },
		func(d app.Day) float64 { return d.Mean.TopPick }, func(d app.Day) app.Interval { return d.Today.TopPick }},
}

// trendMetric is the metric named key, or named.
func trendMetric(key string) metric {
	for _, m := range trendMetrics {
		if m.key == key {
			return m
		}
	}
	return trendMetrics[0]
}

// trendPoint is what the hover layer shows for one day; x and y are in viewBox units.
type trendPoint struct {
	X       float64  `json:"x"`
	Y       float64  `json:"y"`
	Date    string   `json:"date"`
	Label   string   `json:"label"` // what the value is: "named, 7-day"
	Rolling string   `json:"rolling"`
	Band    string   `json:"band"`  // across prompts: questions like these
	Day     string   `json:"day"`   // the day's own rate
	Today   string   `json:"today"` // its fixed-set interval: this set, today
	Rank    string   `json:"rank"`
	Notes   []string `json:"notes,omitempty"`
}

// trend draws one set's rate (named, recommended or top pick, by metric key) on one track: the
// rolling average as a line with its 95% band across prompts, each day's own rate as a faint dot,
// a hairline where the set changed, and a small marker under the days work on a fix was proposed
// or accepted. A change in how answers are asked or graded breaks the line. One series, so no
// legend: the heading names it. The rankings table below it is its table view.
func trend(r app.Ranking, key string) template.HTML {
	m := trendMetric(key)
	days := make([]app.Day, len(r.Days))
	for i, d := range r.Days { // oldest first
		days[len(r.Days)-1-i] = d
	}
	if len(days) < 2 {
		return template.HTML(`<p class="muted small">One day so far; the trend appears from the second.</p>`)
	}
	window := days[len(days)-1].WindowDays
	if window == 0 {
		window = app.Windows[0]
	}
	label := fmt.Sprintf("%s, %d-day", m.label, window)
	parse := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }
	first, last := parse(days[0].Date), parse(days[len(days)-1].Date)
	span := last.Sub(first).Hours() / 24
	if span < 1 {
		span = 1
	}
	xAt := func(date string) float64 { return padL + plotW*parse(date).Sub(first).Hours()/24/span }
	x := func(d app.Day) float64 { return xAt(d.Date) }
	y := func(v float64) float64 { return padTop + plotH*(1-v) }

	var b strings.Builder
	var pts []trendPoint
	fmt.Fprintf(&b, `<div class="trend-wrap"><svg class="trend" viewBox="0 0 %.0f %.0f" role="img" tabindex="0" aria-label="%s">`,
		trendW, trendH, template.HTMLEscapeString(fmt.Sprintf("%s in %s answers, %d-day average, %s on %s to %s on %s, over %d days",
			strings.ToUpper(m.label[:1])+m.label[1:], r.Track, window, pctS(m.rolling(days[0])), days[0].Date, pctS(m.rolling(days[len(days)-1])), days[len(days)-1].Date, len(days))))
	// Recessive grid: 0, 50 and 100%.
	for _, v := range []float64{0, .5, 1} {
		fmt.Fprintf(&b, `<line class="grid" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/><text class="tick" x="%.1f" y="%.1f">%s</text>`,
			padL, padL+plotW, y(v), y(v), padL-6, y(v)+4, pctS(v))
	}
	// Segments: a new method starts a new one.
	var segs [][]app.Day
	for i, d := range days {
		if i == 0 || d.NewMethod {
			segs = append(segs, nil)
		}
		segs[len(segs)-1] = append(segs[len(segs)-1], d)
	}
	for i, d := range days {
		if i > 0 && (d.Change != nil || d.NewMethod) {
			fmt.Fprintf(&b, `<line class="mark%s" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/>`, map[bool]string{true: " method"}[d.NewMethod], x(d), x(d), padTop, padTop+plotH)
		}
	}
	for _, seg := range segs {
		var top, bottom, line []string
		for _, d := range seg {
			bd := m.band(d)
			top = append(top, fmt.Sprintf("%.1f,%.1f", x(d), y(bd.Hi)))
			bottom = append([]string{fmt.Sprintf("%.1f,%.1f", x(d), y(bd.Lo))}, bottom...)
			line = append(line, fmt.Sprintf("%.1f,%.1f", x(d), y(m.rolling(d))))
		}
		if len(seg) > 1 {
			fmt.Fprintf(&b, `<polygon class="band" points="%s"/><polyline class="line" points="%s"/>`, strings.Join(append(top, bottom...), " "), strings.Join(line, " "))
		}
	}
	for _, d := range days {
		fmt.Fprintf(&b, `<circle class="day" cx="%.1f" cy="%.1f" r="4"/>`, x(d), y(m.day(d)))
	}
	end := days[len(days)-1]
	fmt.Fprintf(&b, `<circle class="end" cx="%.1f" cy="%.1f" r="4"/><text class="endlabel" x="%.1f" y="%.1f">%s</text>`,
		x(end), y(m.rolling(end)), x(end)+8, y(m.rolling(end))+4, pctS(m.rolling(end)))
	fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="%.1f" text-anchor="start">%s</text><text class="tick" x="%.1f" y="%.1f" text-anchor="end">%s</text>`,
		padL, trendH-6, days[0].Date[5:], padL+plotW, trendH-6, end.Date[5:])
	// Fix attempts: a small triangle on the baseline, linking to the commit or pull request.
	notes := map[string][]string{}
	for _, at := range r.Attempts {
		if at.Date < days[0].Date || at.Date > end.Date {
			continue
		}
		what := fmt.Sprintf("fix %s: %s", at.State, at.Title)
		notes[at.Date] = append(notes[at.Date], what)
		fmt.Fprintf(&b, `<a href="%s"><path class="fixmark %s" d="M%.1f %.1f l-4 7 h8 z"><title>%s</title></path></a>`,
			template.HTMLEscapeString(at.URL), template.HTMLEscapeString(at.State), xAt(at.Date), padTop+plotH+1, template.HTMLEscapeString(what+" ("+at.URL+")"))
	}
	fmt.Fprintf(&b, `<line class="cross" x1="0" x2="0" y1="%.1f" y2="%.1f" visibility="hidden"/>`, padTop, padTop+plotH)
	for _, d := range days {
		rank := "not named"
		if d.Rank > 0 {
			rank = fmt.Sprintf("#%d", d.Rank)
		}
		bd, td := m.band(d), m.today(d)
		p := trendPoint{X: x(d), Y: y(m.rolling(d)), Date: d.Date, Label: label, Rolling: pctS(m.rolling(d)),
			Band: pctS(bd.Lo) + "–" + pctS(bd.Hi), Day: pctS(m.day(d)), Today: pctS(td.Lo) + "–" + pctS(td.Hi), Rank: rank, Notes: d.Change.Notes()}
		if d.NewMethod {
			p.Notes = append(p.Notes, "asked or graded differently: a new trend")
		}
		if c := d.Compare; c != nil && m.key == "named" {
			p.Notes = append(p.Notes, changeNote("named", c.Visible, c.VisibleMargin, c.WithinNoise))
		} else if c != nil && m.key == "recommended" {
			p.Notes = append(p.Notes, changeNote("recommended", c.Recommended, c.RecommendedMargin, c.RecommendedWithinNoise))
		}
		p.Notes = append(p.Notes, notes[d.Date]...)
		pts = append(pts, p)
	}
	js, _ := json.Marshal(pts)
	b.WriteString(`</svg><div class="trend-tip" hidden></div><script type="application/json" class="trend-data">`)
	b.WriteString(strings.ReplaceAll(string(js), "</", `<\/`))
	b.WriteString(`</script></div>`)
	return template.HTML(b.String())
}

// changeNote is a day-over-day change with its margin: "named +4 pts ±9 vs the day before,
// within noise".
func changeNote(what string, d, margin float64, noise bool) string {
	s := fmt.Sprintf("%s %+.0f pts ±%.0f vs the day before", what, 100*d, 100*margin)
	if noise {
		s += ", within noise"
	}
	return s
}

func pctS(v float64) string { return fmt.Sprintf("%.0f%%", 100*v) }
