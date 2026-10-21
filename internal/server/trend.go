package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/metcalfc/popcorn/internal/app"
)

// Chart geometry, in viewBox units.
const (
	trendW, trendH                = 640.0, 190.0
	padL, padR, padTop, padBottom = 40.0, 56.0, 14.0, 26.0
	plotW, plotH                  = trendW - padL - padR, trendH - padTop - padBottom
)

// trendPoint is what the hover layer shows for one day; x and y are in viewBox units.
type trendPoint struct {
	X       float64  `json:"x"`
	Y       float64  `json:"y"`
	Date    string   `json:"date"`
	Rolling string   `json:"rolling"`
	Band    string   `json:"band"`
	Day     string   `json:"day"`
	Rank    string   `json:"rank"`
	Notes   []string `json:"notes,omitempty"`
}

// trend draws one set's named rate on one track: the 7-day average as a line with its 95% band,
// each day's own rate as a faint dot, and a hairline where the set changed. A change in how
// answers are asked or graded breaks the line. One series, so no legend: the heading names it.
// The rankings table below it is its table view.
func trend(r app.Ranking) template.HTML {
	days := make([]app.Day, len(r.Days))
	for i, d := range r.Days { // oldest first
		days[len(r.Days)-1-i] = d
	}
	if len(days) < 2 {
		return template.HTML(`<p class="muted small">One day so far; the trend appears from the second.</p>`)
	}
	parse := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }
	first, last := parse(days[0].Date), parse(days[len(days)-1].Date)
	span := last.Sub(first).Hours() / 24
	if span < 1 {
		span = 1
	}
	x := func(d app.Day) float64 { return padL + plotW*parse(d.Date).Sub(first).Hours()/24/span }
	y := func(v float64) float64 { return padTop + plotH*(1-v) }

	var b strings.Builder
	var pts []trendPoint
	fmt.Fprintf(&b, `<div class="trend-wrap"><svg class="trend" viewBox="0 0 %.0f %.0f" role="img" tabindex="0" aria-label="%s">`,
		trendW, trendH, template.HTMLEscapeString(fmt.Sprintf("Named in %s answers, 7-day average, %s on %s to %s on %s, over %d days",
			r.Track, pctS(days[0].Rolling.Visible), days[0].Date, pctS(days[len(days)-1].Rolling.Visible), days[len(days)-1].Date, len(days))))
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
			top = append(top, fmt.Sprintf("%.1f,%.1f", x(d), y(d.RollingHi)))
			bottom = append([]string{fmt.Sprintf("%.1f,%.1f", x(d), y(d.RollingLo))}, bottom...)
			line = append(line, fmt.Sprintf("%.1f,%.1f", x(d), y(d.Rolling.Visible)))
		}
		if len(seg) > 1 {
			fmt.Fprintf(&b, `<polygon class="band" points="%s"/><polyline class="line" points="%s"/>`, strings.Join(append(top, bottom...), " "), strings.Join(line, " "))
		}
	}
	for _, d := range days {
		fmt.Fprintf(&b, `<circle class="day" cx="%.1f" cy="%.1f" r="4"/>`, x(d), y(d.Mean.Visible))
	}
	end := days[len(days)-1]
	fmt.Fprintf(&b, `<circle class="end" cx="%.1f" cy="%.1f" r="4"/><text class="endlabel" x="%.1f" y="%.1f">%s</text>`,
		x(end), y(end.Rolling.Visible), x(end)+8, y(end.Rolling.Visible)+4, pctS(end.Rolling.Visible))
	fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="%.1f" text-anchor="start">%s</text><text class="tick" x="%.1f" y="%.1f" text-anchor="end">%s</text>`,
		padL, trendH-6, days[0].Date[5:], padL+plotW, trendH-6, end.Date[5:])
	fmt.Fprintf(&b, `<line class="cross" x1="0" x2="0" y1="%.1f" y2="%.1f" visibility="hidden"/>`, padTop, padTop+plotH)
	for _, d := range days {
		rank := "not named"
		if d.Rank > 0 {
			rank = fmt.Sprintf("#%d", d.Rank)
		}
		p := trendPoint{X: x(d), Y: y(d.Rolling.Visible), Date: d.Date, Rolling: pctS(d.Rolling.Visible),
			Band: pctS(d.RollingLo) + "–" + pctS(d.RollingHi), Day: pctS(d.Mean.Visible), Rank: rank, Notes: d.Change.Notes()}
		if d.NewMethod {
			p.Notes = append(p.Notes, "asked or graded differently: a new trend")
		}
		pts = append(pts, p)
	}
	js, _ := json.Marshal(pts)
	b.WriteString(`</svg><div class="trend-tip" hidden></div><script type="application/json" class="trend-data">`)
	b.WriteString(strings.ReplaceAll(string(js), "</", `<\/`))
	b.WriteString(`</script></div>`)
	return template.HTML(b.String())
}

func pctS(v float64) string { return fmt.Sprintf("%.0f%%", 100*v) }
