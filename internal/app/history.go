package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
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
	Intent             []string      `json:"intent,omitempty"`   // prompt ids that became or stopped being info
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
	return c != nil && len(c.Added)+len(c.Removed)+len(c.Reworded)+len(c.Intent) > 0
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
	if len(c.Intent) > 0 {
		out = append(out, count(len(c.Intent), "prompt's intent changed", "prompts' intent changed"))
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
	return len(c.Added)+len(c.Removed)+len(c.Reworded)+len(c.Intent)+len(c.CompetitorsAdded)+len(c.CompetitorsRemoved)+
		len(c.AnswerersAdded)+len(c.AnswerersRemoved)+len(c.Models)+len(c.Samples) == 0
}

// Compare is the day against the one before over only what both asked: the same prompts, worded
// the same, of the same answerers. When nothing changed it covers every prompt.
//
// Each change is paired prompt by prompt (PairedDiff) and carries its 95% margin; a change whose
// interval covers 0 is within noise: a day-to-day wobble of that size is what chance alone gives.
type Compare struct {
	Common      int     `json:"common"` // prompts in the comparison
	Of          int     `json:"of"`     // prompts asked this day
	Recommended float64 `json:"recommended"`
	Visible     float64 `json:"visible"`
	// 95% margins of the two changes, and whether each interval covers 0.
	VisibleMargin          float64 `json:"visible_margin"`
	RecommendedMargin      float64 `json:"recommended_margin"`
	WithinNoise            bool    `json:"within_noise"` // the named change
	RecommendedWithinNoise bool    `json:"recommended_within_noise"`
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
	Rolling   Mean         `json:"rolling"` // Mean averaged over this and earlier days within WindowDays, same method
	Window    int          `json:"window"`  // days in Rolling
	// The rolling window asked for, in days: 7 (the default), 14 or 28.
	WindowDays int `json:"window_days"`
	// A 95% interval around Rolling.Visible, from how much prompts vary: answers to one prompt
	// aren't independent, so prompts, not answers, are the units. Bands has it for each rate
	// (Named is the same as these two).
	RollingLo float64 `json:"rolling_lo"`
	RollingHi float64 `json:"rolling_hi"`
	Bands     Bands   `json:"bands"`
	// 95% intervals around Mean taking this day's prompts as the whole set: only the noise of
	// sampling answers, so narrower. Bands answers "questions like these"; Today, "this set, today".
	Today   Bands          `json:"today"`
	Answers int            `json:"answers"`
	Prompts int            `json:"prompts"`
	Board   []report.Voice `json:"board"`
	// Places moved up since the day before; only when that day asked the same prompts of the
	// same answerers, since otherwise a rank change says more about the questions than the brand.
	DeltaRank int `json:"delta_rank"`
	// The move could be chance: for every product that changed places with the brand, the
	// paired change in the gap between them covers 0.
	RankWithinNoise bool     `json:"rank_within_noise,omitempty"`
	Change          *Change  `json:"change,omitempty"`
	Compare         *Compare `json:"compare,omitempty"`
}

// Bands is a 95% interval for each of a day's rates.
type Bands struct {
	TopPick     Interval `json:"top_pick"`
	Recommended Interval `json:"recommended"`
	Visible     Interval `json:"visible"`
}

type Ranking struct {
	SetID   string `json:"set_id"`
	SetName string `json:"set_name"`
	Tier    string `json:"tier"`
	Track   string `json:"track"`
	Days    []Day  `json:"days"` // newest first
	// Work submitted for the set's fixes, for marking the trend: when each was proposed, and
	// accepted.
	Attempts []AttemptMark `json:"attempts,omitempty"`
}

// AttemptMark is a day an attempt was proposed or accepted.
type AttemptMark struct {
	Date  string `json:"date"`
	State string `json:"state"` // proposed or accepted
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Windows are the rolling windows trends offer, in days; the first is the default.
var Windows = []int{7, 14, 28}

// Window is days if it's one of Windows, else the default.
func Window(days int) int {
	for _, w := range Windows {
		if w == days {
			return w
		}
	}
	return Windows[0]
}

// dayData is what a day's comparisons are worked out from.
type dayData struct {
	Day
	snap    run.Snapshot
	prompts map[string]string                   // prompt id -> wording hash
	texts   map[string]string                   // prompt id -> text
	info    map[string]bool                     // prompt ids asked as info prompts
	order   []string                            // prompt ids in the run's order
	models  map[string]string                   // answerer -> model
	samples map[string]int                      // answerer -> samples per prompt
	tallies map[string]map[string]*report.Tally // prompt id -> answerer -> tally
	planned map[string]map[string]*report.Tally // the same, without adaptive sampling's extras
	named   map[string]map[string]int           // prompt id -> product (lowercase) -> answers naming it
	shares  map[string]float64                  // host -> its share of the day's citations
}

// series is each track's days for one set, newest first, with changes and comparisons filled in.
// Rolling means and bands are over window days.
func (a *App) series(ctx context.Context, s sets.Set, lastDays, window int) (map[string][]dayData, error) {
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
		r.Intents(res)
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
			d := &days[i]
			d.Rolling, d.Window = rolling(days, i, window)
			d.WindowDays = window
			d.Bands = Bands{
				TopPick:     band(days, i, d.Window, d.Rolling.TopPick, topPick),
				Recommended: band(days, i, d.Window, d.Rolling.Recommended, recommended),
				Visible:     band(days, i, d.Window, d.Rolling.Visible, named),
			}
			d.RollingLo, d.RollingHi = d.Bands.Visible.Lo, d.Bands.Visible.Hi
			d.Today = Bands{TopPick: FixedSet(d.cells(nil, nil, topPick)), Recommended: FixedSet(d.cells(nil, nil, recommended)), Visible: FixedSet(d.cells(nil, nil, named))}
		}
	}
	return out, nil
}

func newDay(r *run.Run, brand config.Brand, date string, results []run.Result) dayData {
	sum := report.Build(brand, results)
	d := dayData{
		Day:     Day{Date: date, RunID: r.ID, Method: r.Method, ConfigRev: r.ConfigRev, Rank: sum.BrandRank(), Rates: sum.Overall, Answers: sum.Done, Board: sum.Voice},
		snap:    r.Snapshot,
		prompts: map[string]string{}, texts: map[string]string{}, info: map[string]bool{}, models: map[string]string{}, samples: map[string]int{},
		tallies: map[string]map[string]*report.Tally{}, planned: map[string]map[string]*report.Tally{}, named: map[string]map[string]int{},
	}
	if len(sum.Voice) > 0 {
		d.Leader = sum.Voice[0].Name
	}
	d.shares = map[string]float64{}
	for _, h := range sum.Hosts {
		if h.Cited > 0 {
			d.shares[h.Host] = h.CitationShare
		}
	}
	name := brand.Name
	for _, x := range results {
		x.PromptID, x.PromptSHA = promptKey(x)
		if _, ok := d.prompts[x.PromptID]; !ok {
			d.order = append(d.order, x.PromptID)
		}
		d.prompts[x.PromptID] = x.PromptSHA
		d.texts[x.PromptID] = x.Prompt
		if x.Info {
			d.info[x.PromptID] = true
		}
		d.models[x.Answerer] = x.Model
		// Planned samples only: adaptive extras vary run to run and aren't a change of samples.
		if x.Sample+1 > d.samples[x.Answerer] && !r.Snapshot.Sampling.IsExtra(x.PromptID, x.Answerer, x.Track, x.Sample) {
			d.samples[x.Answerer] = x.Sample + 1
		}
		if !x.Graded() {
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
		t.Add(x.Grade, name, x.Info)
		if !r.Snapshot.Sampling.IsExtra(x.PromptID, x.Answerer, x.Track, x.Sample) {
			if d.planned[x.PromptID] == nil {
				d.planned[x.PromptID] = map[string]*report.Tally{}
			}
			pt := d.planned[x.PromptID][x.Answerer]
			if pt == nil {
				pt = &report.Tally{}
				d.planned[x.PromptID][x.Answerer] = pt
			}
			pt.Add(x.Grade, name, x.Info)
		}
		// Products named, counted as the share-of-voice board counts them: the brand by its
		// verdict, everyone else by the grader's list.
		if d.named[x.PromptID] == nil {
			d.named[x.PromptID] = map[string]int{}
		}
		seen := map[string]bool{}
		for _, n := range x.Grade.Named {
			if k := strings.ToLower(n); !seen[k] && !isBrand(brand, n) {
				seen[k] = true
				d.named[x.PromptID][k]++
			}
		}
		if x.Grade.Verdict != grade.Absent {
			d.named[x.PromptID][strings.ToLower(name)]++
		}
	}
	d.Prompts = len(d.prompts)
	d.Mean = d.mean(nil, nil)
	return d
}

// isBrand is whether a product the grader named is the brand, by its name or an alias, as the
// share-of-voice board decides it.
func isBrand(b config.Brand, n string) bool {
	if strings.EqualFold(n, b.Name) {
		return true
	}
	for _, a := range b.Aliases {
		if strings.EqualFold(n, a) {
			return true
		}
	}
	return false
}

// mean is the per-prompt mean over the given prompts and answerers (nil: all of them). Info
// prompts count toward named only, so recommended and top pick average over the prompts that ask
// for a choice.
func (d *dayData) mean(prompts, answerers map[string]bool) Mean {
	var m Mean
	n, sel := 0, 0
	for pid, byAnswerer := range d.tallies {
		if prompts != nil && !prompts[pid] {
			continue
		}
		var t report.Tally
		for ans, x := range byAnswerer {
			if answerers != nil && !answerers[ans] {
				continue
			}
			t = t.Plus(*x)
		}
		if t.N == 0 {
			continue
		}
		m.Visible += float64(t.Vis) / float64(t.N)
		n++
		if t.Sel > 0 {
			m.TopPick += float64(t.Top) / float64(t.Sel)
			m.Recommended += float64(t.Rec) / float64(t.Sel)
			sel++
		}
	}
	if n > 0 {
		m.Visible /= float64(n)
	}
	if sel > 0 {
		m.TopPick, m.Recommended = m.TopPick/float64(sel), m.Recommended/float64(sel)
	}
	return m
}

// A metric is one rate's hits and denominator in a tally. Recommended and top pick are out of the
// answers to prompts that ask for a choice.
type metric func(report.Tally) Hits

func named(t report.Tally) Hits       { return Hits{t.Vis, t.N} }
func recommended(t report.Tally) Hits { return Hits{t.Rec, t.Sel} }
func topPick(t report.Tally) Hits     { return Hits{t.Top, t.Sel} }

// cells is m for each of the day's prompts in run order (nil prompts or answerers: all of them),
// each over the given answerers' answers.
func (d *dayData) cells(prompts, answerers map[string]bool, m metric) []Hits {
	return cellsIn(d, d.order, prompts, answerers, m)
}

// tally is one prompt's answers over the given answerers (nil: all of them).
func (d *dayData) tally(pid string, answerers map[string]bool) report.Tally {
	var t report.Tally
	for ans, x := range d.tallies[pid] {
		if answerers == nil || answerers[ans] {
			t = t.Plus(*x)
		}
	}
	return t
}

// paired is the change in m from prev to d over the prompts in same, of the answerers in common.
func paired(d, prev *dayData, same, common map[string]bool, m metric) Diff {
	return PairedDiff(prev.cells(same, common, m), cellsIn(d, prev.order, same, common, m))
}

// cellsIn is d's cells in another day's prompt order, so they pair with that day's.
func cellsIn(d *dayData, order []string, prompts, answerers map[string]bool, m metric) []Hits {
	var out []Hits
	for _, pid := range order {
		if prompts == nil || prompts[pid] {
			out = append(out, m(d.tally(pid, answerers)))
		}
	}
	return out
}

// rankNoise is whether a rank move from prev to d could be chance. The rank is the brand's place
// on the board, so it moves when another product crosses it; for each product that did, the gap
// between them (brand share minus product share of a prompt's answers) is paired prompt by prompt
// like any rate, its variance the sum of both shares' (their covariance is left out). The move is
// within noise unless at least one crossing is significant in the direction it went.
func rankNoise(d, prev *dayData) bool {
	key := ""
	count := func(b []report.Voice) (map[string]int, int) {
		m, mine := map[string]int{}, 0
		for _, v := range b {
			m[strings.ToLower(v.Name)] = v.Answers
			if v.IsBrand {
				mine, key = v.Answers, strings.ToLower(v.Name)
			}
		}
		return m, mine
	}
	now, mine := count(d.Board)
	then, mineThen := count(prev.Board)
	products := map[string]bool{}
	for p := range now {
		products[p] = true
	}
	for p := range then {
		products[p] = true
	}
	for p := range products {
		if p == key {
			continue
		}
		above, aboveThen := now[p] > mine, then[p] > mineThen
		if above == aboveThen {
			continue
		}
		var ss []stratum
		for _, pid := range prev.order {
			n, np := d.tally(pid, nil).N, prev.tally(pid, nil).N
			if n == 0 || np == 0 {
				continue
			}
			us, them := Hits{d.named[pid][key], n}, Hits{d.named[pid][p], n}
			usThen, themThen := Hits{prev.named[pid][key], np}, Hits{prev.named[pid][p], np}
			ss = append(ss, stratum{(us.rate() - them.rate()) - (usThen.rate() - themThen.rate()),
				us.variance() + them.variance() + usThen.variance() + themThen.variance()})
		}
		want := 1 // the brand passed p
		if above {
			want = -1
		}
		if combine(ss).Sign() == want {
			return false
		}
	}
	return true
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
		if _, ok := prev.prompts[id]; ok && prev.info[id] != d.info[id] {
			c.Intent = append(c.Intent, id)
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
		if prev.prompts[id] == sha && prev.info[id] == d.info[id] {
			same[id] = true
		}
	}
	vis, rec := paired(d, prev, same, common, named), paired(d, prev, same, common, recommended)
	d.Compare = &Compare{Common: len(same), Of: len(d.prompts), Recommended: rec.D, Visible: vis.D,
		VisibleMargin: vis.Margin, RecommendedMargin: rec.Margin, WithinNoise: vis.WithinNoise(), RecommendedWithinNoise: rec.WithinNoise()}
	if !c.PromptsChanged() && len(c.AnswerersAdded)+len(c.AnswerersRemoved) == 0 && d.Rank > 0 && prev.Rank > 0 {
		d.DeltaRank = prev.Rank - d.Rank
		d.RankWithinNoise = d.DeltaRank != 0 && rankNoise(d, prev)
	}
}

// rolling averages days[i] with the days before it within window days, up to a change of method.
func rolling(days []dayData, i, window int) (Mean, int) {
	end, err := time.Parse("2006-01-02", days[i].Date)
	if err != nil {
		return days[i].Mean, 1
	}
	var m Mean
	n := 0
	for j := i; j < len(days); j++ {
		t, _ := time.Parse("2006-01-02", days[j].Date)
		if end.Sub(t) >= time.Duration(window)*24*time.Hour {
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

// band is a 95% interval for rate m over days[i] and the n-1 days before it, from how much
// prompt-days vary (AcrossPrompts).
func band(days []dayData, i, n int, mean float64, m metric) Interval {
	var xs []float64
	for j := i; j < i+n && j < len(days); j++ {
		for _, c := range days[j].cells(nil, nil, m) { // in order, so the sums come out the same every time
			if c.N > 0 {
				xs = append(xs, c.rate())
			}
		}
	}
	return AcrossPrompts(xs, mean)
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
// answers are asked or graded starts over (NewMethod). Rolling means and bands are over window
// days (Window).
func (a *App) Rankings(ctx context.Context, lastDays, window int, only string) ([]Ranking, error) {
	window = Window(window)
	var out []Ranking
	for _, s := range a.Sets() {
		if only != "" && s.ID != only {
			continue
		}
		byTrack, err := a.series(ctx, s, lastDays, window)
		if err != nil {
			return nil, err
		}
		marks := a.attemptMarks(ctx, s.ID)
		for _, track := range []string{"search", "recall"} {
			rk := Ranking{SetID: s.ID, SetName: s.Name, Tier: string(s.Tier), Track: track, Days: []Day{}, Attempts: marks}
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

// attemptMarks is the set's attempts as days on its trend: the day each was proposed and, once
// accepted, the day it was (its last update). Withdrawn and dismissed ones are left out: nothing
// shipped. Read straight from the table: a chart doesn't need their outcomes.
func (a *App) attemptMarks(ctx context.Context, setID string) []AttemptMark {
	rows, err := a.DB.QueryContext(ctx, `SELECT fix, url, state, created_at, updated_at FROM fix_attempts WHERE set_id=? AND state IN (?,?) ORDER BY created_at, id`, setID, Proposed, Accepted)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AttemptMark
	for rows.Next() {
		var fix, link, created, updated string
		var state AttemptState
		if rows.Scan(&fix, &link, &state, &created, &updated) != nil {
			continue
		}
		var f analysis.Fix
		json.Unmarshal([]byte(fix), &f)
		out = append(out, AttemptMark{Date: created[:min(10, len(created))], State: string(Proposed), Title: f.Title, URL: link})
		if state == Accepted {
			out = append(out, AttemptMark{Date: updated[:min(10, len(updated))], State: string(Accepted), Title: f.Title, URL: link})
		}
	}
	return out
}

// Cell is one prompt on one day: how its answers went, and whether it was worded differently
// from the day before.
type Cell struct {
	Asked       bool `json:"asked"`
	N           int  `json:"n"`
	Recommended int  `json:"recommended"`
	Visible     int  `json:"visible"`
	Reworded    bool `json:"reworded,omitempty"`
	Info        bool `json:"info,omitempty"`   // asked as an info prompt: recommended doesn't apply
	Intent      bool `json:"intent,omitempty"` // its intent changed since the day before
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
	// The prompt's answers pooled over the days in the window, back from the newest, as long as
	// they were asked and graded the same way and the prompt worded as on the newest day it was
	// asked; with Wilson 95% intervals. Recommended is empty for an info prompt.
	Named       report.Rate `json:"named"`
	Recommended report.Rate `json:"recommended"`
	PooledDays  int         `json:"pooled_days"`
}

type PromptHistory struct {
	Track string       `json:"track"`
	Days  []HistoryDay `json:"days"`
	Rows  []PromptLine `json:"rows"`
	// The window Named and Recommended are pooled over, in days.
	Window int `json:"window"`
}

// History is each prompt's results day by day on one track: the set's prompts today in order,
// then prompts it has since dropped. Each prompt's rates are pooled over window days (Window).
func (a *App) History(ctx context.Context, s sets.Set, track string, lastDays, window int) (PromptHistory, error) {
	window = Window(window)
	h := PromptHistory{Track: track, Days: []HistoryDay{}, Rows: []PromptLine{}, Window: window}
	byTrack, err := a.series(ctx, s, lastDays, window)
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
				c.Asked, c.Info = true, d.info[id]
				for _, t := range d.tallies[id] {
					c.N, c.Recommended, c.Visible = c.N+t.N, c.Recommended+t.Rec, c.Visible+t.Vis
				}
				if i+1 < len(days) {
					if before, ok := days[i+1].prompts[id]; ok && before != sha {
						c.Reworded = true
					}
					if _, ok := days[i+1].prompts[id]; ok && days[i+1].info[id] != c.Info {
						c.Intent = true
					}
				}
			}
			l.Cells = append(l.Cells, c)
		}
		l.Named, l.Recommended, l.PooledDays = pooled(days, id, window)
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

// pooled is prompt id's named and recommended rates over the days (newest first) within window
// days of the newest one that asked it, stopping at a change of method or of the prompt's wording
// or intent, and how many days that pooled.
func pooled(days []dayData, id string, window int) (report.Rate, report.Rate, int) {
	var t report.Tally
	var end time.Time
	sha, info, n := "", false, 0
	for j := range days {
		d := &days[j]
		s, ok := d.prompts[id]
		if !ok {
			if d.NewMethod && n > 0 {
				break
			}
			continue
		}
		at, _ := time.Parse("2006-01-02", d.Date)
		if n == 0 {
			end, sha, info = at, s, d.info[id]
		} else if s != sha || d.info[id] != info || end.Sub(at) >= time.Duration(window)*24*time.Hour {
			break
		}
		t = t.Plus(d.tally(id, nil))
		n++
		if d.NewMethod {
			break // the day before this one was measured differently
		}
	}
	r := t.Rates()
	if info {
		r.Recommended = report.Rate{}
	}
	return r.Visible, r.Recommended, n
}

// SampleAdvice says whether a set's extra samples per prompt are paying for themselves, from the
// split of its per-prompt named rates' variance (SplitVariance) over recent runs.
type SampleAdvice struct {
	VarianceSplit
	Samples int    `json:"samples"` // the most samples any answerer took per prompt
	Text    string `json:"text,omitempty"`
}

// Advice thresholds: enough history to estimate the split, and the widening (VarianceSplit) below
// which the extra samples are buying little.
const (
	adviceDays     = 28
	adviceMinRuns  = 4
	adviceMinCells = 6
	adviceLittle   = 1.25
)

// SampleAdvice estimates, for a set that asks some answerer more than one sample per prompt, how
// much of the run-to-run variance of each prompt's named rate is sampling within a run (which
// more samples reduce) and how much drift between runs (which they don't), over the same-method
// days of the last four weeks. Cells are prompt x answerer x track, each with the same samples on
// every day it counts. Text is empty without enough history.
func (a *App) SampleAdvice(ctx context.Context, s sets.Set) SampleAdvice {
	var out SampleAdvice
	byTrack, err := a.series(ctx, s, adviceDays, Windows[0])
	if err != nil {
		return out
	}
	var cells [][]Hits
	for _, track := range []string{"search", "recall"} {
		days := byTrack[track]
		if len(days) == 0 {
			continue
		}
		byCell := map[[3]string][]Hits{}
		var order [][3]string
		for j := range days {
			d := &days[j]
			for _, pid := range d.order {
				// Planned samples only: extras would leave a cell with a different count each day.
				for ans, t := range d.planned[pid] {
					if d.samples[ans] < 2 || t.N != d.samples[ans] {
						continue // one sample, or not every sample graded
					}
					k := [3]string{pid, ans, d.prompts[pid]}
					if xs := byCell[k]; len(xs) > 0 && xs[0].N != t.N {
						continue
					}
					if byCell[k] == nil {
						order = append(order, k)
					}
					byCell[k] = append(byCell[k], Hits{t.Vis, t.N})
					out.Samples = max(out.Samples, t.N)
				}
			}
			if d.NewMethod {
				break
			}
		}
		for _, k := range order {
			cells = append(cells, byCell[k])
		}
	}
	out.VarianceSplit = SplitVariance(cells)
	// No variance at all (the brand always or never named) says nothing about samples either way.
	if out.Runs < adviceMinRuns || out.Cells < adviceMinCells || out.Within+out.Between == 0 {
		return out
	}
	w := out.Widening()
	if w < adviceLittle {
		out.Text = fmt.Sprintf("Extra samples are buying little precision: between-run drift dominates (%.0f%% of the variance at %d samples); samples=1 would give about the same band (%.2f× as wide).",
			100*out.Between/(out.Within+out.Between), out.Samples, w)
	} else {
		out.Text = fmt.Sprintf("The samples are paying off: sampling within a run is %.0f%% of the variance at %d samples; samples=1 would make the band about %.1f× as wide.",
			100*out.Within/(out.Within+out.Between), out.Samples, w)
	}
	return out
}
