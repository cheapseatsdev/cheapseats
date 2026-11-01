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

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

type App struct {
	analyses sync.Map
	aliases  aliasCache
	// Page types and host classes Jev decided (classify.go), and the objection and praise
	// clusters (cluster.go).
	judgments judgmentCache
	phrases   phraseCache
	Cfg       *config.Config
	DB        *store.DB
	Runs      *run.Engine
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
	a := &App{Cfg: cfg, DB: db, Runs: run.New(cfg, db), sets: sets.Share(all, cfg.Competitors)}
	if err := a.Backfill(context.Background()); err != nil {
		slog.Error("backfilling prompt ids", "err", err)
	}
	a.Runs.OnFinished = func(ctx context.Context, id string) {
		// The optional providers' passes first, so the fix list and insights see folded names and
		// the coverage of each prompt; then the passes that read the fix list.
		a.runPasses(ctx, id, true)
		if err := a.BuildFixes(ctx, id); err != nil {
			slog.Error("fix list", "run", id, "err", err)
		}
		a.runPasses(ctx, id, false)
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
	if r.Status != run.Done {
		return fmt.Errorf("the fix list needs a finished run")
	}
	for _, x := range results {
		if !x.Graded() {
			return fmt.Errorf("analysis requires all results graded")
		}
	}
	// Advice is about today's product and chosen market. Historical answers and
	// grades remain evidence; rebuilding advice never changes how they were scored.
	cfg := *a.Cfg
	reader := audit.CiteOptions{DB: a.DB.DB, Reader: a.ContentsReader()}
	ctx = analysis.WithVerifier(ctx, func(ctx context.Context, u string) (string, error) {
		return audit.ReadCurrent(ctx, u, reader)
	})
	ctx = analysis.WithSiteFindings(ctx, a.siteFindings(ctx)) // the latest site audit, as evidence
	sum := a.Summary(r, results)
	ctx = analysis.WithOpportunities(ctx, sum.Opportunities)
	ctx = analysis.WithObjections(ctx, sum.Objections) // with the pages behind each
	ctx = analysis.WithMisstatements(ctx, a.misstatementLines(ctx, id, results))
	an, cost, err := analysis.Build(ctx, &cfg, r.Snapshot.Set, results, a.triedFor(ctx, r.SetID), a.dismissedFor(ctx))
	return a.recordAnalysis(ctx, id, an, cost, err)
}

// recordAnalysis stores what Build returned for run id: the analysis, its error, or both.
func (a *App) recordAnalysis(ctx context.Context, id string, an analysis.Analysis, cost float64, err error) error {
	var verr *analysis.VerificationError
	if err != nil && !errors.As(err, &verr) {
		a.DB.ExecContext(ctx, `UPDATE runs SET fixes_error=?, analysis_cost_usd=analysis_cost_usd+? WHERE id=?`, err.Error(), cost, id)
		return err
	}
	// A failed verification still leaves investigations worth keeping. They're stored with the
	// error, so the run page and the watch script's fixes_error check see that it failed.
	var failed sql.NullString
	if verr != nil {
		slog.Error("analysis verification", "run", id, "err", err)
		failed = sql.NullString{String: err.Error(), Valid: true}
	}
	_, err = a.DB.ExecContext(ctx, `UPDATE runs SET fixes=?, insights=?, fixes_at=?, fixes_error=?, analysis_cost_usd=analysis_cost_usd+? WHERE id=?`, analysis.Encode(an), an.Insights, store.Now(), failed, cost, id)
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
	Speed float64 `json:"speed"` // prompts it could flip per working day
	// Expected gain: Speed with each prompt weighted by how much it has left to win, 1 - p, p its
	// recommended rate (named, for an info prompt) pooled over the set's runs of the last
	// quickWinDays asked and graded as the fix's run was. A prompt already recommended in most
	// answers adds little; one with no data counts in full.
	Gain    float64 `json:"gain"`
	SetID   string  `json:"set_id"`
	SetName string  `json:"set_name"`
	Tier    string  `json:"tier"`
	RunID   string  `json:"run_id"`
	// Prompt ids reworded since the run, and how many of the fix's prompts the set no longer asks.
	Reworded []string `json:"reworded,omitempty"`
	Dropped  int      `json:"dropped,omitempty"`
	// Open, proposed, accepted or dismissed (FixAttempts.Status): from Attempts, the work submitted for this
	// fix, or from work on the same page the fix list didn't know about. Earlier is what was tried
	// for the same target before, so a retry shows what didn't work.
	Status   string    `json:"status"`
	Attempts []Attempt `json:"attempts,omitempty"`
	Earlier  []Attempt `json:"earlier_attempts,omitempty"`
	// Other sets whose latest fix list asks for the same page (fix_matches, at typesafe.min_confidence):
	// their cards are folded into this one. Filled by groupQuickWins.
	AlsoIn []AlsoIn `json:"also_in,omitempty"`
}

// QuickWins pulls the fix lists from the latest finished run of every set, core sets first, then
// fastest expected ranking gain (Gain: prompts flipped per working day, each weighted by what it
// has left to win), then reach, then the analyst's own rank. A fix none of whose prompts the set
// still asks is left out.
func (a *App) QuickWins(ctx context.Context) ([]QuickWin, error) {
	all, err := a.quickWins(ctx)
	if err != nil {
		return nil, err
	}
	out := []QuickWin{}
	for _, q := range all {
		if q.Verification.Status == "verified" {
			out = append(out, q)
		}
	}
	return a.groupQuickWins(ctx, out), nil
}

// quickWins retains earlier suggestions and their work history for Next steps.
// Only QuickWins exposes actions that passed verification.
func (a *App) quickWins(ctx context.Context) ([]QuickWin, error) {
	var out []QuickWin
	tried := a.FixAttempts(ctx)
	for _, s := range a.Sets() {
		var id string
		var fixesAt sql.NullString
		err := a.DB.QueryRowContext(ctx, `SELECT id, fixes_at FROM runs WHERE set_id=? AND fixes IS NOT NULL AND status='done' ORDER BY created_at DESC LIMIT 1`, s.ID).Scan(&id, &fixesAt)
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
		left := a.leftToWin(ctx, s, r.Method)
		fixes, _, _ := a.Fixes(ctx, id)
		for _, f := range fixes {
			q := QuickWin{SetID: s.ID, SetName: s.Name, Tier: string(s.Tier), RunID: id, Attempts: tried.On(id, f.Rank), Earlier: tried.Earlier(id, f)}
			q.Attempts, q.Earlier = readOnlyWorkHistory(q.Attempts), readOnlyWorkHistory(q.Earlier)
			q.Status = tried.Status(id, f, fixesAt.String)
			var now []int
			for _, n := range f.Prompts {
				if n < 1 || n > len(then.Prompts) {
					continue
				}
				old := then.Prompts[n-1]
				if m, ok := number[old.ID]; ok {
					now = append(now, m)
					if cur, _ := s.Prompt(old.ID); cur.SHA() != old.SHA() || cur.Info() != old.Info() {
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
			if len(q.Reworded) > 0 {
				f.Verification.Status = "unverified"
				f.Verification.Summary = "A target question changed after this action was verified; review the new buyer need before implementation."
			}
			q.Fix = analysis.Triage(a.Cfg, s, f)
			q.Speed = q.Fix.Speed()
			w := 0.0
			for _, n := range now {
				w += left(s.Prompts[n-1])
			}
			q.Gain = q.Speed * w / float64(len(now))
			out = append(out, q)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if (x.Tier == "core") != (y.Tier == "core") {
			return x.Tier == "core"
		}
		if x.Gain != y.Gain {
			return x.Gain > y.Gain
		}
		if len(x.Prompts) != len(y.Prompts) {
			return len(x.Prompts) > len(y.Prompts)
		}
		return x.Rank < y.Rank
	})
	return out, nil
}

// quickWinDays is how far back quick wins pool a prompt's answers to weigh it.
const quickWinDays = 14

// leftToWin is, for each of the set's prompts as worded today, 1 - its recommended rate (named,
// for an info prompt) over the finished runs of the last quickWinDays with the given method; 1
// for a prompt without graded answers there.
func (a *App) leftToWin(ctx context.Context, s sets.Set, method string) func(sets.Prompt) float64 {
	type counts struct{ n, named, rec int }
	got := map[[2]string]counts{}
	since := time.Now().UTC().AddDate(0, 0, -quickWinDays).Format(time.RFC3339)
	rows, err := a.DB.QueryContext(ctx, `SELECT x.prompt_id, x.prompt_sha, COUNT(*), SUM(x.verdict<>'absent'), SUM(x.verdict='recommended')
		FROM results x JOIN runs r ON r.id = x.run_id
		WHERE r.set_id=? AND r.status='done' AND r.method=? AND r.created_at>=? AND x.status='done' AND x.grade IS NOT NULL AND x.verdict IS NOT NULL
		GROUP BY x.prompt_id, x.prompt_sha`, s.ID, method, since)
	if err == nil {
		for rows.Next() {
			var id, sha string
			var c counts
			if rows.Scan(&id, &sha, &c.n, &c.named, &c.rec) == nil {
				got[[2]string{id, sha}] = c
			}
		}
		rows.Close()
	}
	return func(p sets.Prompt) float64 {
		c := got[[2]string{p.ID, p.SHA()}]
		if c.n == 0 {
			return 1
		}
		if p.Info() {
			return 1 - float64(c.named)/float64(c.n)
		}
		return 1 - float64(c.rec)/float64(c.n)
	}
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
	r.Intents(results)
	// Sources and PageTypes are the config's [sources] with Jev's host classes merged under them,
	// and Jev's page types (classify.go); Objections and Praise fold wordings Jev clustered
	// (cluster.go). With no decisions stored they are the config as it is.
	return report.BuildWith(a.RunBrand(r), results, report.Options{Competitors: a.RunCompetitors(r), Sources: a.SourceClasses(), Aliases: a.Aliases(), PageTypes: a.PageTypes(), Objections: a.PhraseClusters(objectionKind), Praise: a.PhraseClusters(praiseKind)})
}

// RunCompetitors are the competitors a run's report sorts products and sources by: the ones the
// run was graded with, and any the config has added since, with their domains as configured
// today, since, like the brand's sites, domains only decide whose pages are whose.
func (a *App) RunCompetitors(r *run.Run) []sets.Competitor {
	today := a.Cfg.Competitors
	if st, ok := a.Set(r.SetID); ok {
		today = st.Competitors // the shared ones folded in
	}
	key := func(c sets.Competitor) string { return strings.ToLower(strings.TrimSpace(c.Name)) }
	domains := map[string][]string{}
	for _, c := range today {
		domains[key(c)] = c.Domains
	}
	var out []sets.Competitor
	seen := map[string]bool{}
	for _, c := range r.Snapshot.Set.Competitors {
		c.Domains = domains[key(c)]
		seen[key(c)] = true
		out = append(out, c)
	}
	for _, c := range today {
		if !seen[key(c)] {
			out = append(out, c)
		}
	}
	return out
}

// QuickWinFilter narrows quick wins; empty fields match everything. Site is one of the brand's
// sites by url or kind ("docs"), or "elsewhere" for fixes off its sites (outreach, product). Status
// is open, proposed, accepted or dismissed, and Priority urgent, high, medium or low. Dismissed
// fixes are left out unless Status asks for them.
type QuickWinFilter struct{ Gap, Set, Site, Status, Priority string }

func (a *App) FilterQuickWins(qw []QuickWin, f QuickWinFilter) []QuickWin {
	out := []QuickWin{}
	for _, q := range qw {
		if f.Gap != "" && string(q.Gap) != f.Gap || f.Set != "" && !q.InSet(f.Set) ||
			f.Status != "" && q.Status != f.Status || f.Status == "" && q.Status == string(Dismissed) ||
			f.Priority != "" && q.Priority != f.Priority {
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
		if a.Runs.Status(ctx, id) != run.Queued || a.Runs.IsActive(id) {
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
		var status run.Status
		a.DB.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=?`, r.ID).Scan(&status)
		if status == run.OverBudget {
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
