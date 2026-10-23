package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// Page types and source classes for the defaults (docs/optional-providers.md). report.PageType
// returns other, and Classifier.Class editorial, for any page or host the rule lists don't know.
// After a run, with [typesafe] configured, each such page with a title and each such host is put
// to Jev once, ever: which kind of page is this, which kind of site is that? The decisions are
// stored (page_judgments) with Jev's probability for the option it chose, and PageTypes and
// SourceClasses read them back for every report at today's threshold, so no page calls Jev and
// switching it off leaves the type and class tables as they were.

const (
	pageTypeKind     = "page_type"
	sourceClassKind  = "source_class"
	hostPagesInState = 3
)

// jevClasses are the classes Jev chooses among for a host: the third-party ones. Own and
// competitor come from the config and are decided before any list is consulted.
var jevClasses = []string{report.UGC, report.Review, report.Reference, report.Institutional, report.Editorial}

func init() {
	RegisterPass(Pass{
		Name: "page-types", Command: "classify", Order: 40, Provider: config.TypeSafeProvider,
		Usage: "type pages and class hosts the rule lists call other or editorial ([typesafe])",
		On:    (*config.Config).PageTypesOn,
		Run:   (*App).ClassifyPages,
		Print: func(a *App, ctx context.Context, id string, n int) {
			fmt.Printf("asked Jev about %d pages and hosts\n", n)
			js, err := a.RunPageJudgments(ctx, id)
			if err != nil {
				fmt.Println("cheapseats:", err)
			}
			for _, j := range js {
				fmt.Printf("  %-12s %-56s -> %-14s %.2f%s\n", j.Kind, j.Key, j.Choice, j.Confidence, map[bool]string{true: "", false: "  (below the threshold)"}[j.Applies])
			}
		},
	})
}

// judgmentCache is the read side of page_judgments, rebuilt after the pass writes or the process
// starts: Jev's page types, and the [sources] overrides with Jev's host classes merged under them.
type judgmentCache struct {
	mu      sync.Mutex
	valid   bool
	types   map[string]string
	sources map[string][]string
}

// PageTypes is the page types Jev decided, a normalized URL to a type, at or above the configured
// confidence: report.Options.PageTypes.
func (a *App) PageTypes() map[string]string {
	a.loadJudgments()
	return a.judgments.types
}

// SourceClasses is the overrides map a report's Classifier takes: the config's [sources], plus
// the hosts Jev classed at or above the configured confidence. A host the config places anywhere
// is left to the config, so [sources] wins over Jev, and Jev wins over the built-in default.
func (a *App) SourceClasses() map[string][]string {
	a.loadJudgments()
	return a.judgments.sources
}

func (a *App) loadJudgments() {
	a.judgments.mu.Lock()
	defer a.judgments.mu.Unlock()
	if a.judgments.valid {
		return
	}
	types := map[string]string{}
	sources := map[string][]string{}
	for cl, hosts := range a.Cfg.Sources {
		sources[cl] = append([]string(nil), hosts...)
	}
	var rows *sql.Rows
	err := sql.ErrConnDone
	if a.DB != nil {
		rows, err = a.DB.Query(`SELECT kind, key, choice, confidence FROM page_judgments`)
	}
	if err == nil {
		min := a.Cfg.MinConfidence()
		for rows.Next() {
			var kind, key, choice string
			var conf float64
			if rows.Scan(&kind, &key, &choice, &conf) != nil || conf < min {
				continue
			}
			switch kind {
			case pageTypeKind:
				if slices.Contains(report.PageTypeOrder, choice) {
					types[key] = choice
				}
			case sourceClassKind:
				if slices.Contains(jevClasses, choice) && !inSources(a.Cfg.Sources, key) {
					sources[choice] = append(sources[choice], key)
				}
			}
		}
		rows.Close()
	} else if a.DB != nil {
		slog.Error("page judgments", "err", err)
	}
	a.judgments.types, a.judgments.sources, a.judgments.valid = types, sources, true
}

func (a *App) invalidateJudgments() {
	a.judgments.mu.Lock()
	a.judgments.valid = false
	a.judgments.mu.Unlock()
}

// inSources reports whether any [sources] entry covers the host, whatever class it places it in.
func inSources(sources map[string][]string, host string) bool {
	for _, hosts := range sources {
		for _, h := range hosts {
			if config.MatchSite(h, "https://"+host) >= 0 {
				return true
			}
		}
	}
	return false
}

// seenPage is a page the run's answers cited or their searches returned, with the first title
// any answer gave it, as report.BuildWith counts them.
type seenPage struct {
	url, title, host string
	cited, surfaced  int
}

// runPages lists the run's pages, most cited first, then most surfaced, then by URL.
func runPages(results []run.Result) []seenPage {
	pages := map[string]*seenPage{}
	for _, r := range results {
		if r.Answer == nil {
			continue
		}
		touch := func(u, title string) *seenPage {
			u = report.NormalizeURL(u)
			p := pages[u]
			if p == nil {
				p = &seenPage{url: u, host: report.Host(u)}
				pages[u] = p
			}
			if p.title == "" {
				p.title = strings.TrimSpace(title)
			}
			return p
		}
		cited := map[string]bool{}
		for _, c := range r.Answer.Citations {
			if p := touch(c.URL, c.Title); !cited[p.url] {
				cited[p.url] = true
				p.cited++
			}
		}
		surfaced := map[string]bool{}
		for _, q := range r.Answer.Searches {
			for _, l := range q.Results {
				if p := touch(l.URL, l.Title); !surfaced[p.url] {
					surfaced[p.url] = true
					p.surfaced++
				}
			}
		}
	}
	out := make([]seenPage, 0, len(pages))
	for _, p := range pages {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if x.cited != y.cited {
			return x.cited > y.cited
		}
		if x.surfaced != y.surfaced {
			return x.surfaced > y.surfaced
		}
		return x.url < y.url
	})
	return out
}

// ClassifyPages puts the run's untyped pages and unclassed hosts to Jev and stores its decisions.
// It returns how many questions were asked. A page the rules type, a page with no title, a host
// the brand, a competitor or [sources] owns, and anything decided before, for any run, are
// skipped, so re-running it costs nothing.
func (a *App) ClassifyPages(ctx context.Context, id string) (int, error) {
	if m := config.Missing(config.TypeSafeProvider); m != "" {
		return 0, fmt.Errorf("typesafe: %s", m)
	}
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	results, err := a.Runs.Results(ctx, id)
	if err != nil {
		return 0, err
	}
	brand := a.RunBrand(r)
	category := categoryOf(brand)
	// The rule classifier: the config's [sources], the lists and the rules, with no Jev in it, so
	// a host is judged by what the rules alone say.
	rules := report.NewClassifier(brand, a.RunCompetitors(r), a.Cfg.Sources)
	decided := map[string]map[string]bool{pageTypeKind: {}, sourceClassKind: {}}
	rows, err := a.DB.QueryContext(ctx, `SELECT kind, key FROM page_judgments`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var kind, key string
		if err := rows.Scan(&kind, &key); err != nil {
			rows.Close()
			return 0, err
		}
		if decided[kind] != nil {
			decided[kind][key] = true
		}
	}
	rows.Close()

	pages := runPages(results)
	client := a.TypeSafe()
	asked, total := 0, 0.0
	store1 := func(kind, key string, ans typesafe.Answer, options []string) error {
		if !slices.Contains(options, ans.Choice) {
			return nil // not one of the options offered: nothing to store
		}
		_, err := a.DB.ExecContext(ctx, `INSERT OR REPLACE INTO page_judgments(kind, key, choice, confidence, source, decided_at) VALUES (?,?,?,?,'jev',?)`,
			kind, key, ans.Choice, typesafe.Chosen(ans), store.Now())
		return err
	}
	defer a.invalidateJudgments()

	// One Choice per page the rules call other, over every type, with the definitions as criteria.
	typeCriteria := map[string]any{}
	for _, t := range report.PageTypeOrder {
		typeCriteria[t] = report.PageTypeDefinitions[t]
	}
	pageQ := typesafe.Question{
		Type:         "choice",
		Instructions: "`url` and `title` are a web page an AI assistant cited, or its search returned, while answering a buyer's question about `category`. Which kind of page is it? Judge from the title and the URL's path, not from whose site it is; choose `other` only when none of the kinds fits or there isn't enough to tell.",
		Criteria:     typeCriteria,
	}
	for _, p := range pages {
		if p.title == "" || decided[pageTypeKind][p.url] || report.PageType(p.url, p.title) != report.OtherPage {
			continue
		}
		if ctx.Err() != nil {
			return asked, ctx.Err()
		}
		state := map[string]any{"url": p.url, "title": p.title, "category": category}
		resp, err := client.Ask(ctx, "page-types/"+id, state, map[string]typesafe.Question{"type": pageQ})
		if err != nil {
			return asked, err
		}
		asked++
		total += resp.CostUSD
		if err := store1(pageTypeKind, p.url, resp.Answers["type"], report.PageTypeOrder); err != nil {
			return asked, err
		}
	}

	// One Choice per host the rules call editorial that the config doesn't place, over the
	// third-party classes, with up to three of its pages in the state.
	classCriteria := map[string]any{}
	for _, c := range jevClasses {
		classCriteria[c] = report.ClassDefinitions[c]
	}
	hostQ := typesafe.Question{
		Type:         "choice",
		Instructions: "`host` is a website; `pages` are pages on it that AI assistants cited, or their searches returned, while answering buyers' questions about `category`. Which kind of site is it? Judge from the host name and the pages' titles and paths; `editorial` is the answer for a publisher, blog or vendor that is none of the other kinds.",
		Criteria:     classCriteria,
	}
	hostPages := map[string][]seenPage{}
	hostCited := map[string]int{}
	var hosts []string
	for _, p := range pages {
		if p.host == "" {
			continue
		}
		if _, ok := hostPages[p.host]; !ok {
			hosts = append(hosts, p.host)
		}
		hostPages[p.host] = append(hostPages[p.host], p)
		hostCited[p.host] += p.cited
	}
	sort.SliceStable(hosts, func(i, j int) bool { return hostCited[hosts[i]] > hostCited[hosts[j]] })
	for _, h := range hosts {
		if decided[sourceClassKind][h] || inSources(a.Cfg.Sources, h) {
			continue
		}
		if cl, _ := rules.Class("https://" + h); cl != report.Editorial {
			continue
		}
		if ctx.Err() != nil {
			return asked, ctx.Err()
		}
		var ps []any
		for _, p := range hostPages[h][:min(hostPagesInState, len(hostPages[h]))] {
			ps = append(ps, map[string]any{"url": p.url, "title": p.title})
		}
		state := map[string]any{"host": h, "pages": ps, "category": category}
		resp, err := client.Ask(ctx, "page-types/"+id, state, map[string]typesafe.Question{"class": hostQ})
		if err != nil {
			return asked, err
		}
		asked++
		total += resp.CostUSD
		if err := store1(sourceClassKind, h, resp.Answers["class"], jevClasses); err != nil {
			return asked, err
		}
	}
	if asked > 0 {
		slog.Info("page types", "run", id, "asked", asked, "model", client.Model, "cost_usd", fmt.Sprintf("%.4f", total))
	}
	return asked, nil
}

// PageJudgment is one stored decision: a page's type or a host's class.
type PageJudgment struct {
	Kind       string  `json:"kind"` // page_type | source_class
	Key        string  `json:"key"`  // the normalized URL, or the host
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
	DecidedAt  string  `json:"decided_at"`
	// Whether the decision takes effect today, at the configured confidence.
	Applies bool `json:"applies"`
}

// PageJudgments lists every stored decision, newest first.
func (a *App) PageJudgments(ctx context.Context) ([]PageJudgment, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT kind, key, choice, confidence, source, decided_at FROM page_judgments ORDER BY decided_at DESC, kind, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	min := a.Cfg.MinConfidence()
	var out []PageJudgment
	for rows.Next() {
		var j PageJudgment
		if err := rows.Scan(&j.Kind, &j.Key, &j.Choice, &j.Confidence, &j.Source, &j.DecidedAt); err != nil {
			return nil, err
		}
		j.Applies = j.Confidence >= min
		out = append(out, j)
	}
	return out, rows.Err()
}

// RunPageJudgments is the stored decisions about a run's pages and hosts, pages first, each in
// the run's most-cited order: what cheapseats classify prints.
func (a *App) RunPageJudgments(ctx context.Context, id string) ([]PageJudgment, error) {
	results, err := a.Runs.Results(ctx, id)
	if err != nil {
		return nil, err
	}
	all, err := a.PageJudgments(ctx)
	if err != nil {
		return nil, err
	}
	by := map[[2]string]PageJudgment{}
	for _, j := range all {
		by[[2]string{j.Kind, j.Key}] = j
	}
	var out []PageJudgment
	seenHost := map[string]bool{}
	pages := runPages(results)
	for _, p := range pages {
		if j, ok := by[[2]string{pageTypeKind, p.url}]; ok {
			out = append(out, j)
		}
	}
	for _, p := range pages {
		if j, ok := by[[2]string{sourceClassKind, p.host}]; ok && !seenHost[p.host] {
			seenHost[p.host] = true
			out = append(out, j)
		}
	}
	return out, nil
}
