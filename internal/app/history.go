package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/report"
	"github.com/metcalfc/popcorn/internal/run"
	"github.com/metcalfc/popcorn/internal/sets"
)

// Backfill fills in what runs from before prompt ids lack: each run's method (recomputed for
// every run, so it always follows the current definition), and each result's
// prompt id and wording hash, matched by text against today's sets (the same set first, then
// any). It only writes a match: a result whose prompt no set asks today is left for a later start,
// so upgrading before converting the config loses nothing, and until then it's tracked under
// legacyID. Rows that are filled in are untouched, so it's cheap to call on every start.
func (a *App) Backfill(ctx context.Context) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Every run's method, from its snapshot, wherever the stored one differs: a run from before
	// methods, or one stored under an older definition of run.Method.
	rows, err := tx.QueryContext(ctx, `SELECT id, snapshot, method FROM runs`)
	if err != nil {
		return err
	}
	methods := map[string]string{}
	for rows.Next() {
		var id, js, stored string
		var snap run.Snapshot
		if rows.Scan(&id, &js, &stored) == nil && json.Unmarshal([]byte(js), &snap) == nil {
			if m := run.Method(snap); m != stored {
				methods[id] = m
			}
		}
	}
	rows.Close()
	for id, m := range methods {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET method=? WHERE id=?`, m, id); err != nil {
			return err
		}
	}

	bySet := map[string]map[string]string{}
	anySet := map[string]string{}
	for _, s := range a.Sets() {
		bySet[s.ID] = map[string]string{}
		for _, p := range s.Prompts {
			bySet[s.ID][p.SHA()] = p.ID
			if _, ok := anySet[p.SHA()]; !ok {
				anySet[p.SHA()] = p.ID
			}
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT x.id, x.run_id, x.prompt_idx, r.set_id, x.prompt FROM results x JOIN runs r ON r.id = x.run_id WHERE x.prompt_id = ''`)
	if err != nil {
		return err
	}
	type fill struct {
		id       int64
		run      string
		idx      int
		pid, sha string
	}
	var fills []fill
	for rows.Next() {
		var f fill
		var set, text string
		if err := rows.Scan(&f.id, &f.run, &f.idx, &set, &text); err != nil {
			rows.Close()
			return err
		}
		f.sha = sets.Prompt{Text: text}.SHA()
		f.pid = bySet[set][f.sha]
		if f.pid == "" {
			f.pid = anySet[f.sha]
		}
		if f.pid != "" {
			fills = append(fills, f)
		}
	}
	rows.Close()
	ids := map[string]map[int]string{} // run -> prompt index -> id, for the snapshots
	for _, f := range fills {
		if _, err := tx.ExecContext(ctx, `UPDATE results SET prompt_id=?, prompt_sha=? WHERE id=?`, f.pid, f.sha, f.id); err != nil {
			return err
		}
		if ids[f.run] == nil {
			ids[f.run] = map[int]string{}
		}
		ids[f.run][f.idx] = f.pid
	}
	// The run's own copy of the set gets the ids too, so its fix list resolves the same way.
	for id, byIdx := range ids {
		var js string
		var snap run.Snapshot
		if tx.QueryRowContext(ctx, `SELECT snapshot FROM runs WHERE id=?`, id).Scan(&js) != nil || json.Unmarshal([]byte(js), &snap) != nil {
			continue
		}
		for i := range snap.Set.Prompts {
			if snap.Set.Prompts[i].ID == "" && byIdx[i] != "" {
				snap.Set.Prompts[i].ID = byIdx[i]
			}
		}
		b, _ := json.Marshal(snap)
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET snapshot=? WHERE id=?`, string(b), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// legacyID stands in for the id of a prompt from before ids that no set asks today. It's never
// stored, so the real id replaces it once the config has the prompt again.
func legacyID(text string) string { return "legacy-" + sets.Prompt{Text: text}.SHA()[:8] }

// promptKey is a result's prompt id and wording hash, standing in for ones not backfilled yet.
func promptKey(x run.Result) (string, string) {
	if x.PromptID != "" {
		return x.PromptID, x.PromptSHA
	}
	return legacyID(x.Prompt), sets.Prompt{Text: x.Prompt}.SHA()
}

// Mean is a day's rates with every prompt counting once, however many answers it got, so a day
// with an extra sample or answerer isn't weighted differently.
type Mean struct {
	TopPick     float64 `json:"top_pick"`
	Recommended float64 `json:"recommended"`
	Visible     float64 `json:"visible"`
}

// Change is what differs from the day before in the same series. None of it breaks the trend;
// it's there so a step in the line can be read against what caused it.
type Change struct {
	Added              []string      `json:"added,omitempty"`    // prompt ids
	Removed            []string      `json:"removed,omitempty"`  // prompt ids
	Reworded           []string      `json:"reworded,omitempty"` // prompt ids whose text changed
	CompetitorsAdded   []string      `json:"competitors_added,omitempty"`
	CompetitorsRemoved []string      `json:"competitors_removed,omitempty"`
	AnswerersAdded     []string      `json:"answerers_added,omitempty"`
	AnswerersRemoved   []string      `json:"answerers_removed,omitempty"`
	Models             []ModelChange `json:"models,omitempty"`
	Samples            []string      `json:"samples,omitempty"` // answerers whose sample count changed
}

type ModelChange struct {
	Answerer string `json:"answerer"`
	From     string `json:"from"`
	To       string `json:"to"`
}

// PromptsChanged reports whether the day asked different questions than the day before.
func (c *Change) PromptsChanged() bool {
	return c != nil && len(c.Added)+len(c.Removed)+len(c.Reworded) > 0
}

// Notes is the change in a few words per kind, for the rankings table.
func (c *Change) Notes() []string {
	if c == nil {
		return nil
	}
	var out []string
	count := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	if len(c.Added) > 0 {
		out = append(out, "+"+count(len(c.Added), "prompt", "prompts"))
	}
	if len(c.Removed) > 0 {
		out = append(out, "−"+count(len(c.Removed), "prompt", "prompts"))
	}
	if len(c.Reworded) > 0 {
		out = append(out, count(len(c.Reworded), "prompt reworded", "prompts reworded"))
	}
	for _, m := range c.Models {
		out = append(out, fmt.Sprintf("%s: %s → %s", m.Answerer, m.From, m.To))
	}
	if len(c.AnswerersAdded) > 0 {
		out = append(out, "answerers +"+strings.Join(c.AnswerersAdded, ", "))
	}
	if len(c.AnswerersRemoved) > 0 {
		out = append(out, "answerers −"+strings.Join(c.AnswerersRemoved, ", "))
	}
	if len(c.Samples) > 0 {
		out = append(out, "samples changed: "+strings.Join(c.Samples, ", "))
	}
	if n := len(c.CompetitorsAdded) + len(c.CompetitorsRemoved); n > 0 {
		out = append(out, count(n, "competitor change", "competitor changes"))
	}
	return out
}

func (c *Change) empty() bool {
	return len(c.Added)+len(c.Removed)+len(c.Reworded)+len(c.CompetitorsAdded)+len(c.CompetitorsRemoved)+
		len(c.AnswerersAdded)+len(c.AnswerersRemoved)+len(c.Models)+len(c.Samples) == 0
}

// Compare is the day against the one before over only what both asked: the same prompts, worded
// the same, of the same answerers. When nothing changed it covers every prompt.
type Compare struct {
	Common      int     `json:"common"` // prompts in the comparison
	Of          int     `json:"of"`     // prompts asked this day
	Recommended float64 `json:"recommended"`
	Visible     float64 `json:"visible"`
}

// Day is one set's standing on one UTC day, from its newest finished run that day.
type Day struct {
	Date   string `json:"date"`
	RunID  string `json:"run_id"`
	Method string `json:"method"`
	// The config commit the day's run used, so a change can be traced to the commit that made it.
	ConfigRev string `json:"config_rev,omitempty"`
	// Asked or graded differently from the day before, so not compared with it.
	NewMethod bool         `json:"new_method,omitempty"`
	Rank      int          `json:"rank"` // 0: never named
	Leader    string       `json:"leader"`
	Rates     report.Rates `json:"rates"` // over every answer, as on the run page
	Mean      Mean         `json:"mean"`
	Rolling   Mean         `json:"rolling"` // Mean averaged over this and earlier days within 7 days, same method
	Window    int          `json:"window"`  // days in Rolling
	// A 95% interval around Rolling.Visible, from how much prompts vary: answers to one prompt
	// aren't independent, so prompts, not answers, are the units.
	RollingLo float64        `json:"rolling_lo"`
	RollingHi float64        `json:"rolling_hi"`
	Answers   int            `json:"answers"`
	Prompts   int            `json:"prompts"`
	Board     []report.Voice `json:"board"`
	// Places moved up since the day before; only when that day asked the same prompts of the
	// same answerers, since otherwise a rank change says more about the questions than the brand.
	DeltaRank int      `json:"delta_rank"`
	Change    *Change  `json:"change,omitempty"`
	Compare   *Compare `json:"compare,omitempty"`
}

type Ranking struct {
	SetID   string `json:"set_id"`
	SetName string `json:"set_name"`
	Tier    string `json:"tier"`
	Track   string `json:"track"`
	Days    []Day  `json:"days"` // newest first
}

// dayData is what a day's comparisons are worked out from.
type dayData struct {
	Day
	snap    run.Snapshot
	prompts map[string]string                   // prompt id -> wording hash
	texts   map[string]string                   // prompt id -> text
	order   []string                            // prompt ids in the run's order
	models  map[string]string                   // answerer -> model
	samples map[string]int                      // answerer -> samples per prompt
	tallies map[string]map[string]*report.Tally // prompt id -> answerer -> tally
}

// series is each track's days for one set, newest first, with changes and comparisons filled in.
func (a *App) series(ctx context.Context, s sets.Set, lastDays int) (map[string][]dayData, error) {
	since := time.Now().UTC().AddDate(0, 0, -lastDays).Format("2006-01-02")
	rows, err := a.DB.QueryContext(ctx, `SELECT id, substr(created_at,1,10) FROM runs WHERE set_id=? AND status='done' AND profile!='' AND created_at>=? ORDER BY created_at DESC`, s.ID, since)
	if err != nil {
		return nil, err
	}
	type pick struct{ id, day string }
	var picks []pick
	seen := map[string]bool{}
	for rows.Next() {
		var p pick
		rows.Scan(&p.id, &p.day)
		if !seen[p.day] {
			seen[p.day] = true
			picks = append(picks, p)
		}
	}
	rows.Close()
	out := map[string][]dayData{}
	for _, p := range picks {
		r, err := a.Runs.Get(ctx, p.id)
		if err != nil {
			return nil, err
		}
		res, err := a.Runs.Results(ctx, p.id)
		if err != nil {
			return nil, err
		}
		for _, track := range []string{"search", "recall"} {
			var slice []run.Result
			for _, x := range res {
				if string(x.Track) == track {
					slice = append(slice, x)
				}
			}
			if len(slice) == 0 {
				continue
			}
			out[track] = append(out[track], newDay(r, a.RunBrand(r), p.day, slice))
		}
	}
	for track := range out {
		days := out[track]
		for i := range days {
			if i+1 < len(days) {
				compare(&days[i], &days[i+1])
			}
		}
		for i := range days {
			days[i].Rolling, days[i].Window = rolling(days, i)
			days[i].RollingLo, days[i].RollingHi = band(days, i, days[i].Window, days[i].Rolling.Visible)
		}
	}
	return out, nil
}

func newDay(r *run.Run, brand config.Brand, date string, results []run.Result) dayData {
	sum := report.Build(brand, results)
	d := dayData{
		Day:     Day{Date: date, RunID: r.ID, Method: r.Method, ConfigRev: r.ConfigRev, Rank: sum.BrandRank(), Rates: sum.Overall, Answers: sum.Done, Board: sum.Voice},
		snap:    r.Snapshot,
		prompts: map[string]string{}, texts: map[string]string{}, models: map[string]string{}, samples: map[string]int{},
		tallies: map[string]map[string]*report.Tally{},
	}
	if len(sum.Voice) > 0 {
		d.Leader = sum.Voice[0].Name
	}
	name := brand.Name
	for _, x := range results {
		x.PromptID, x.PromptSHA = promptKey(x)
		if _, ok := d.prompts[x.PromptID]; !ok {
			d.order = append(d.order, x.PromptID)
		}
		d.prompts[x.PromptID] = x.PromptSHA
		d.texts[x.PromptID] = x.Prompt
		d.models[x.Answerer] = x.Model
		if x.Sample+1 > d.samples[x.Answerer] {
			d.samples[x.Answerer] = x.Sample + 1
		}
		if x.Status != "done" || x.Grade == nil {
			continue
		}
		if d.tallies[x.PromptID] == nil {
			d.tallies[x.PromptID] = map[string]*report.Tally{}
		}
		t := d.tallies[x.PromptID][x.Answerer]
		if t == nil {
			t = &report.Tally{}
			d.tallies[x.PromptID][x.Answerer] = t
		}
		t.Add(x.Grade, name)
	}
	d.Prompts = len(d.prompts)
	d.Mean = d.mean(nil, nil)
	return d
}

// mean is the per-prompt mean over the given prompts and answerers (nil: all of them).
func (d *dayData) mean(prompts, answerers map[string]bool) Mean {
	var m Mean
	n := 0
	for pid, byAnswerer := range d.tallies {
		if prompts != nil && !prompts[pid] {
			continue
		}
		var t report.Tally
		for ans, x := range byAnswerer {
			if answerers != nil && !answerers[ans] {
				continue
			}
			t.Top, t.Rec, t.Vis, t.N = t.Top+x.Top, t.Rec+x.Rec, t.Vis+x.Vis, t.N+x.N
		}
		if t.N == 0 {
			continue
		}
		m.TopPick += float64(t.Top) / float64(t.N)
		m.Recommended += float64(t.Rec) / float64(t.N)
		m.Visible += float64(t.Vis) / float64(t.N)
		n++
	}
	if n > 0 {
		m.TopPick, m.Recommended, m.Visible = m.TopPick/float64(n), m.Recommended/float64(n), m.Visible/float64(n)
	}
	return m
}

// compare fills in d against prev, the day before it.
func compare(d, prev *dayData) {
	if d.Method != prev.Method {
		d.NewMethod = true
		return
	}
	c := &Change{}
	for _, id := range d.order {
		sha, ok := prev.prompts[id]
		switch {
		case !ok:
			c.Added = append(c.Added, id)
		case sha != d.prompts[id]:
			c.Reworded = append(c.Reworded, id)
		}
	}
	for _, id := range prev.order {
		if _, ok := d.prompts[id]; !ok {
			c.Removed = append(c.Removed, id)
		}
	}
	c.CompetitorsAdded, c.CompetitorsRemoved = diff(competitors(d.snap), competitors(prev.snap))
	var now, before []string
	for a := range d.models {
		now = append(now, a)
	}
	for a := range prev.models {
		before = append(before, a)
	}
	c.AnswerersAdded, c.AnswerersRemoved = diff(now, before)
	common := map[string]bool{}
	sort.Strings(now)
	for _, a := range now {
		if m, ok := prev.models[a]; ok {
			common[a] = true
			if m != d.models[a] {
				c.Models = append(c.Models, ModelChange{a, m, d.models[a]})
			}
			if prev.samples[a] != d.samples[a] {
				c.Samples = append(c.Samples, a)
			}
		}
	}
	if !c.empty() {
		d.Change = c
	}
	same := map[string]bool{}
	for id, sha := range d.prompts {
		if prev.prompts[id] == sha {
			same[id] = true
		}
	}
	x, y := d.mean(same, common), prev.mean(same, common)
	d.Compare = &Compare{Common: len(same), Of: len(d.prompts), Recommended: x.Recommended - y.Recommended, Visible: x.Visible - y.Visible}
	if !c.PromptsChanged() && len(c.AnswerersAdded)+len(c.AnswerersRemoved) == 0 && d.Rank > 0 && prev.Rank > 0 {
		d.DeltaRank = prev.Rank - d.Rank
	}
}

// rolling averages days[i] with the days before it within a week, up to a change of method.
func rolling(days []dayData, i int) (Mean, int) {
	end, err := time.Parse("2006-01-02", days[i].Date)
	if err != nil {
		return days[i].Mean, 1
	}
	var m Mean
	n := 0
	for j := i; j < len(days); j++ {
		t, _ := time.Parse("2006-01-02", days[j].Date)
		if end.Sub(t) >= 7*24*time.Hour {
			break
		}
		m.TopPick += days[j].Mean.TopPick
		m.Recommended += days[j].Mean.Recommended
		m.Visible += days[j].Mean.Visible
		n++
		if days[j].NewMethod {
			break // the day before this one was measured differently
		}
	}
	return Mean{m.TopPick / float64(n), m.Recommended / float64(n), m.Visible / float64(n)}, n
}

// band is a 95% interval for the visible rate over days[i] and the window-1 days before it: the
// mean ± 1.96 standard errors over every prompt-day's rate.
func band(days []dayData, i, window int, mean float64) (float64, float64) {
	var xs []float64
	for j := i; j < i+window && j < len(days); j++ {
		for _, byAnswerer := range days[j].tallies {
			var vis, n int
			for _, t := range byAnswerer {
				vis, n = vis+t.Vis, n+t.N
			}
			if n > 0 {
				xs = append(xs, float64(vis)/float64(n))
			}
		}
	}
	if len(xs) < 2 {
		return mean, mean
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
	return math.Max(0, mean-1.96*se), math.Min(1, mean+1.96*se)
}

func competitors(s run.Snapshot) []string {
	var out []string
	for _, c := range s.Set.Competitors {
		out = append(out, c.Name)
	}
	return out
}

// diff is what's in now but not before, and the other way round, each sorted.
func diff(now, before []string) (added, removed []string) {
	in := func(xs []string, x string) bool {
		for _, y := range xs {
			if y == x {
				return true
			}
		}
		return false
	}
	for _, x := range now {
		if !in(before, x) {
			added = append(added, x)
		}
	}
	for _, x := range before {
		if !in(now, x) {
			removed = append(removed, x)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// Rankings is each set's day-by-day series per track, across prompt changes: a day records what
// changed since the one before and compares with it over what both asked. A change in how
// answers are asked or graded starts over (NewMethod).
func (a *App) Rankings(ctx context.Context, lastDays int, only string) ([]Ranking, error) {
	var out []Ranking
	for _, s := range a.Sets() {
		if only != "" && s.ID != only {
			continue
		}
		byTrack, err := a.series(ctx, s, lastDays)
		if err != nil {
			return nil, err
		}
		for _, track := range []string{"search", "recall"} {
			rk := Ranking{SetID: s.ID, SetName: s.Name, Tier: string(s.Tier), Track: track, Days: []Day{}}
			for _, d := range byTrack[track] {
				rk.Days = append(rk.Days, d.Day)
			}
			if len(rk.Days) > 0 {
				out = append(out, rk)
			}
		}
	}
	return out, nil
}

// Cell is one prompt on one day: how its answers went, and whether it was worded differently
// from the day before.
type Cell struct {
	Asked       bool `json:"asked"`
	N           int  `json:"n"`
	Recommended int  `json:"recommended"`
	Visible     int  `json:"visible"`
	Reworded    bool `json:"reworded,omitempty"`
}

type HistoryDay struct {
	Date      string `json:"date"`
	RunID     string `json:"run_id"`
	NewMethod bool   `json:"new_method,omitempty"`
}

type PromptLine struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Current bool   `json:"current"` // still in the set
	Cells   []Cell `json:"cells"`   // one per day, newest first
}

type PromptHistory struct {
	Track string       `json:"track"`
	Days  []HistoryDay `json:"days"`
	Rows  []PromptLine `json:"rows"`
}

// History is each prompt's results day by day on one track: the set's prompts today in order,
// then prompts it has since dropped.
func (a *App) History(ctx context.Context, s sets.Set, track string, lastDays int) (PromptHistory, error) {
	h := PromptHistory{Track: track, Days: []HistoryDay{}, Rows: []PromptLine{}}
	byTrack, err := a.series(ctx, s, lastDays)
	if err != nil {
		return h, err
	}
	days := byTrack[track]
	for _, d := range days {
		h.Days = append(h.Days, HistoryDay{Date: d.Date, RunID: d.RunID, NewMethod: d.NewMethod})
	}
	line := func(id, text string, current bool) PromptLine {
		l := PromptLine{ID: id, Text: text, Current: current}
		for i, d := range days {
			c := Cell{}
			if sha, ok := d.prompts[id]; ok {
				c.Asked = true
				for _, t := range d.tallies[id] {
					c.N, c.Recommended, c.Visible = c.N+t.N, c.Recommended+t.Rec, c.Visible+t.Vis
				}
				if i+1 < len(days) {
					if before, ok := days[i+1].prompts[id]; ok && before != sha {
						c.Reworded = true
					}
				}
			}
			l.Cells = append(l.Cells, c)
		}
		return l
	}
	current := map[string]bool{}
	for _, p := range s.Prompts {
		current[p.ID] = true
		h.Rows = append(h.Rows, line(p.ID, p.Text, true))
	}
	for _, d := range days {
		for _, id := range d.order {
			if !current[id] {
				current[id] = true
				h.Rows = append(h.Rows, line(id, d.texts[id], false))
			}
		}
	}
	return h, nil
}
