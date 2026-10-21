// Package app wires the pieces together: runs, fix lists, rankings and the schedule, behind one
// value the CLI, web UI and API all use.
package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/metcalfc/popcorn/internal/analysis"
	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/report"
	"github.com/metcalfc/popcorn/internal/run"
	"github.com/metcalfc/popcorn/internal/sets"
	"github.com/metcalfc/popcorn/internal/store"
)

type App struct {
	analyses sync.Map
	Cfg      *config.Config
	DB       *store.DB
	Runs     *run.Engine
	// The prompt sets, read once with the config: a deploy is one consistent version of both.
	sets []sets.Set
}

// New reads the prompt sets beside the config and fails if any of them is broken, so a bad
// deploy stops at startup rather than running without part of its config.
func New(cfg *config.Config, db *store.DB) (*App, error) {
	all, err := sets.LoadAll(sets.Dir(cfg.Dir))
	if err != nil {
		return nil, fmt.Errorf("prompt sets: %w", err)
	}
	a := &App{Cfg: cfg, DB: db, Runs: run.New(cfg, db), sets: all}
	if err := a.Backfill(context.Background()); err != nil {
		slog.Error("backfilling prompt ids", "err", err)
	}
	a.Runs.OnFinished = func(ctx context.Context, id string) {
		if err := a.BuildFixes(ctx, id); err != nil {
			slog.Error("fix list", "run", id, "err", err)
		}
	}
	return a, nil
}

func (a *App) Sets() []sets.Set { return a.sets }

func (a *App) Set(id string) (sets.Set, bool) {
	for _, s := range a.sets {
		if s.ID == id {
			return s, true
		}
	}
	return sets.Set{}, false
}

// StartRun creates and starts a run over a set.
func (a *App) StartRun(ctx context.Context, setID, trigger string) (*run.Run, error) {
	s, ok := a.Set(setID)
	if !ok {
		return nil, fmt.Errorf("no prompt set %q", setID)
	}
	r, err := a.Runs.Create(ctx, s, trigger)
	if err != nil {
		return nil, err
	}
	a.Runs.Start(r.ID)
	return r, nil
}

// BuildFixes (re)builds a run's fix list.
func (a *App) BuildFixes(ctx context.Context, id string) error {
	if _, busy := a.analyses.LoadOrStore(id, true); busy {
		return fmt.Errorf("analysis already active")
	}
	defer a.analyses.Delete(id)
	ctx = llm.WithOperation(llm.WithAccounting(ctx, a.Runs.Budget), "analysis/"+id)
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return err
	}
	results, err := a.Runs.Results(ctx, id)
	if err != nil {
		return err
	}
	if r.Status != "done" {
		return fmt.Errorf("the fix list needs a finished run")
	}
	for _, x := range results {
		if x.Status != "done" || x.Grade == nil {
			return fmt.Errorf("analysis requires all results graded")
		}
	}
	// The brand as the run saw it, with today's facts: a fact added since can still shape the list.
	cfg := *a.Cfg
	cfg.Brand = r.Snapshot.Brand
	cfg.Brand.Facts = a.Cfg.Brand.Facts
	an, cost, err := analysis.Build(ctx, &cfg, r.Snapshot.Set, results)
	if err != nil {
		a.DB.ExecContext(ctx, `UPDATE runs SET fixes_error=?, analysis_cost_usd=analysis_cost_usd+? WHERE id=?`, err.Error(), cost, id)
		return err
	}
	_, err = a.DB.ExecContext(ctx, `UPDATE runs SET fixes=?, insights=?, fixes_at=?, fixes_error=NULL, analysis_cost_usd=analysis_cost_usd+? WHERE id=?`, mustJSON(an.Fixes), an.Insights, store.Now(), cost, id)
	return err
}

func (a *App) Fixes(ctx context.Context, id string) ([]analysis.Fix, *string, error) {
	var fx, fe sql.NullString
	if err := a.DB.QueryRowContext(ctx, `SELECT fixes, fixes_error FROM runs WHERE id=?`, id).Scan(&fx, &fe); err != nil {
		return nil, nil, err
	}
	var errp *string
	if fe.Valid {
		errp = &fe.String
	}
	if !fx.Valid {
		return nil, errp, nil
	}
	return analysis.Decode(fx.String), errp, nil
}

// QuickWin is a fix from the latest run of some set, with the set it came from. Its prompts are
// the set's prompts today: ones removed since the run are dropped, and ones reworded are listed.
type QuickWin struct {
	analysis.Fix
	Speed   float64 `json:"speed"` // prompts it could flip per working day
	SetID   string  `json:"set_id"`
	SetName string  `json:"set_name"`
	Tier    string  `json:"tier"`
	RunID   string  `json:"run_id"`
	// Prompt ids reworded since the run, and how many of the fix's prompts the set no longer asks.
	Reworded []string `json:"reworded,omitempty"`
	Dropped  int      `json:"dropped,omitempty"`
}

// QuickWins pulls the fix lists from the latest finished run of every set, core sets first, then
// fastest ranking gain: prompts flipped per working day, then reach, then the analyst's own rank.
// A fix none of whose prompts the set still asks is left out.
func (a *App) QuickWins(ctx context.Context) ([]QuickWin, error) {
	var out []QuickWin
	for _, s := range a.Sets() {
		var id string
		err := a.DB.QueryRowContext(ctx, `SELECT id FROM runs WHERE set_id=? AND fixes IS NOT NULL AND status='done' ORDER BY created_at DESC LIMIT 1`, s.ID).Scan(&id)
		if err != nil {
			continue
		}
		r, err := a.Runs.Get(ctx, id)
		if err != nil {
			continue
		}
		then := r.Snapshot.Set
		number := map[string]int{}
		for i, p := range s.Prompts {
			number[p.ID] = i + 1
		}
		fixes, _, _ := a.Fixes(ctx, id)
		for _, f := range fixes {
			q := QuickWin{SetID: s.ID, SetName: s.Name, Tier: string(s.Tier), RunID: id}
			var now []int
			for _, n := range f.Prompts {
				if n < 1 || n > len(then.Prompts) {
					continue
				}
				old := then.Prompts[n-1]
				if m, ok := number[old.ID]; ok {
					now = append(now, m)
					if cur, _ := s.Prompt(old.ID); cur.SHA() != old.SHA() {
						q.Reworded = append(q.Reworded, old.ID)
					}
				} else {
					q.Dropped++
				}
			}
			if len(now) == 0 {
				continue
			}
			sort.Ints(now)
			f.Prompts = now
			q.Fix = analysis.Triage(a.Cfg, s, f)
			q.Speed = q.Fix.Speed()
			out = append(out, q)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if (x.Tier == "core") != (y.Tier == "core") {
			return x.Tier == "core"
		}
		if x.Speed != y.Speed {
			return x.Speed > y.Speed
		}
		if len(x.Prompts) != len(y.Prompts) {
			return len(x.Prompts) > len(y.Prompts)
		}
		return x.Rank < y.Rank
	})
	return out, nil
}

// RunBrand is the brand a past run is summarized with: who the brand is (name, aliases, about,
// audience, facts) as that run saw it, since that shaped how it was graded, and its sites as
// configured today, since sites only decide which pages count as its own and never make runs
// incomparable. Every view of a run (its page, rankings, the API, MCP and the CLI) uses it, so
// they agree about the same run.
func (a *App) RunBrand(r *run.Run) config.Brand {
	b := r.Snapshot.Brand
	b.Sites, b.Domains = a.Cfg.Brand.Sites, a.Cfg.Brand.Domains
	return b
}

// Summary is a run's report over results (all of its results, or a slice of them).
func (a *App) Summary(r *run.Run, results []run.Result) report.Summary {
	return report.Build(a.RunBrand(r), results)
}

// QuickWinFilter narrows quick wins; empty fields match everything. Site is one of the brand's
// sites by url or kind ("docs"), or "elsewhere" for fixes off its sites (outreach, product).
type QuickWinFilter struct{ Gap, Set, Site string }

func (a *App) FilterQuickWins(qw []QuickWin, f QuickWinFilter) []QuickWin {
	out := []QuickWin{}
	for _, q := range qw {
		if f.Gap != "" && string(q.Gap) != f.Gap || f.Set != "" && q.SetID != f.Set {
			continue
		}
		if f.Site != "" {
			kind := ""
			if s, ok := a.Cfg.Brand.SiteOf(q.Site); ok && q.Site != "" {
				kind = s.Kind
			}
			if !(f.Site == "elsewhere" && q.Site == "" || f.Site == q.Site && q.Site != "" || f.Site == kind && kind != "") {
				continue
			}
		}
		out = append(out, q)
	}
	return out
}

// Resume finishes the runs a restart interrupted (ids, from Runs.Recover), one after another, then
// builds any fix list a restart cut off. A run this version would ask or grade differently can't
// be finished consistently, so it's failed with the reason instead.
func (a *App) Resume(ctx context.Context, ids []string) {
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if a.Runs.Status(ctx, id) != "queued" || a.Runs.IsActive(id) {
			continue // cancelled or retried meanwhile
		}
		r, err := a.Runs.Get(ctx, id)
		if err != nil {
			continue
		}
		if !r.Snapshot.Current(a.Cfg) {
			a.Runs.Fail(ctx, id, "interrupted by a restart, and this version asks or grades answers differently, so it can't be finished; start a new run")
			continue
		}
		slog.Info("resuming run", "run", id)
		if err := a.Runs.Execute(ctx, id); err != nil {
			slog.Error("resuming run", "run", id, "err", err)
		}
	}
	// Fix lists whose build a restart cut off: finished runs from the last week with neither a
	// list nor an error.
	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	rows, err := a.DB.QueryContext(ctx, `SELECT id FROM runs WHERE status='done' AND fixes IS NULL AND fixes_error IS NULL AND finished_at >= ? ORDER BY finished_at`, since)
	if err != nil {
		return
	}
	var todo []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		todo = append(todo, id)
	}
	rows.Close()
	for _, id := range todo {
		if ctx.Err() != nil {
			return
		}
		if err := a.BuildFixes(ctx, id); err != nil {
			slog.Error("fix list", "run", id, "err", err)
		}
	}
}

// Schedule runs every core set daily at the configured UTC hour, and each PLG set when it's been
// tiers.plg.every_days since its last run, one set after another. Returns when ctx ends.
func (a *App) Schedule(ctx context.Context) {
	if a.Cfg.Schedule.DailyHourUTC == nil {
		slog.Info("schedule off (schedule.daily_hour_utc unset)")
		return
	}
	hour := *a.Cfg.Schedule.DailyHourUTC
	slog.Info("schedule on", "hour_utc", hour)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		now := time.Now().UTC()
		if now.Hour() >= hour {
			a.runDue(ctx, now)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// everyDays is how often the schedule runs a set: tiers.core.every_days or tiers.plg.every_days.
func (a *App) everyDays(s sets.Set) int {
	if s.Tier == sets.PLG {
		return a.Cfg.Tiers.PLG.EveryDays
	}
	return a.Cfg.Tiers.Core.EveryDays
}

func (a *App) runDue(ctx context.Context, now time.Time) {
	today := now.Format("2006-01-02")
	for _, s := range a.Sets() {
		var last sql.NullString
		a.DB.QueryRowContext(ctx, `SELECT MAX(substr(created_at,1,10)) FROM runs WHERE set_id=? AND trigger='schedule'`, s.ID).Scan(&last)
		due := !last.Valid
		if last.Valid {
			lt, _ := time.Parse("2006-01-02", last.String)
			due = last.String != today && now.Sub(lt) >= time.Duration(a.everyDays(s))*24*time.Hour-time.Hour
		}
		if !due {
			continue
		}
		r, err := a.Runs.Create(ctx, s, "schedule")
		var busy *run.BusyError
		if errors.As(err, &busy) {
			continue // already running; today's schedule counts it once it's done
		}
		if err != nil {
			slog.Error("scheduled run", "set", s.ID, "err", err)
			return
		}
		slog.Info("scheduled run", "set", s.ID, "run", r.ID)
		if err := a.Runs.Execute(ctx, r.ID); err != nil {
			slog.Error("scheduled run", "set", s.ID, "err", err)
		}
		var status string
		a.DB.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=?`, r.ID).Scan(&status)
		if status == "budget" {
			return // nothing else will fit today
		}
	}
}

type CitationQuery struct {
	URLContains string // case-insensitive substring, e.g. "docs.example.com"
	LastDays    int    // only runs created in the last N days; 0 for all
	SetID       string
}

type CitedBy struct {
	RunID     string `json:"run_id"`
	RunName   string `json:"run_name"`
	CreatedAt string `json:"created_at"`
	ResultID  int64  `json:"result_id"`
	Answerer  string `json:"answerer"`
	Prompt    string `json:"prompt"`
}

type CitedPage struct {
	URL       string    `json:"url"`
	Host      string    `json:"host"`
	Citations int       `json:"citations"` // answers that linked it (twice in one answer counts once)
	Runs      int       `json:"runs"`
	CitedBy   []CitedBy `json:"cited_by"` // newest run first
}

// Citations are the denominators too: a page absent from Pages went uncited across that many answers.
type Citations struct {
	Since          *string     `json:"since"`
	RunsScanned    int         `json:"runs_scanned"`
	AnswersScanned int         `json:"answers_scanned"`
	Pages          []CitedPage `json:"pages"`
}

// Citations is every URL linked from a finished answer across runs, most cited first.
func (a *App) Citations(ctx context.Context, q CitationQuery) (Citations, error) {
	out := Citations{Pages: []CitedPage{}}
	since := ""
	if q.LastDays > 0 {
		since = time.Now().UTC().AddDate(0, 0, -q.LastDays).Format(time.RFC3339)
		out.Since = &since
	}
	rows, err := a.DB.QueryContext(ctx, `SELECT r.id, r.name, r.created_at, x.id, x.answerer, x.prompt, x.answer
		FROM results x JOIN runs r ON r.id = x.run_id
		WHERE x.status = 'done' AND x.answer IS NOT NULL AND r.created_at >= ? AND (? = '' OR r.set_id = ?)
		ORDER BY r.created_at DESC, x.id`, since, q.SetID, q.SetID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	needle := strings.ToLower(q.URLContains)
	pages := map[string]*CitedPage{}
	runsOf := map[string]map[string]bool{}
	runs := map[string]bool{}
	for rows.Next() {
		var by CitedBy
		var js string
		if err := rows.Scan(&by.RunID, &by.RunName, &by.CreatedAt, &by.ResultID, &by.Answerer, &by.Prompt, &js); err != nil {
			return out, err
		}
		runs[by.RunID] = true
		out.AnswersScanned++
		var ans answer.Answer
		if json.Unmarshal([]byte(js), &ans) != nil {
			continue
		}
		seen := map[string]bool{}
		for _, c := range ans.Citations {
			u := report.NormalizeURL(c.URL)
			if u == "" || seen[u] || (needle != "" && !strings.Contains(strings.ToLower(u), needle)) {
				continue
			}
			seen[u] = true
			p := pages[u]
			if p == nil {
				p = &CitedPage{URL: u, Host: report.Host(u)}
				pages[u] = p
				runsOf[u] = map[string]bool{}
			}
			p.Citations++
			runsOf[u][by.RunID] = true
			p.CitedBy = append(p.CitedBy, by)
		}
	}
	out.RunsScanned = len(runs)
	for u, p := range pages {
		p.Runs = len(runsOf[u])
		out.Pages = append(out.Pages, *p)
	}
	sort.Slice(out.Pages, func(i, j int) bool {
		x, y := out.Pages[i], out.Pages[j]
		if x.Citations != y.Citations {
			return x.Citations > y.Citations
		}
		if x.Runs != y.Runs {
			return x.Runs > y.Runs
		}
		return x.URL < y.URL
	})
	return out, rows.Err()
}

// Live is a cheap fingerprint of everything a page shows about runs, so the UI can refresh only
// when something changed, and poll faster while work is in progress.
type Live struct {
	Version string `json:"version"`
	Active  bool   `json:"active"` // a run or a fix list is in progress
}

func (a *App) Live(ctx context.Context) (Live, error) {
	var l Live
	var results, active int
	var finished, runs string
	err := a.DB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM results WHERE status NOT IN ('pending','running')),
		(SELECT COALESCE(MAX(finished_at), '') FROM results),
		(SELECT COUNT(*) FROM runs WHERE status IN ('queued','running')),
		(SELECT COALESCE(group_concat(id || ':' || status || ':' || COALESCE(fixes_at, '') || ':' || COALESCE(fixes_error, ''), ','), '')
			FROM (SELECT * FROM runs ORDER BY id))`).
		Scan(&results, &finished, &active, &runs)
	if err != nil {
		return l, err
	}
	analyzing := false
	a.analyses.Range(func(any, any) bool { analyzing = true; return false })
	l.Active = active > 0 || analyzing
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%t", results, finished, runs, l.Active)))
	l.Version = hex.EncodeToString(sum[:8])
	return l, nil
}
