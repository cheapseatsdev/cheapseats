package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// The neutral retrieval baseline (docs/optional-providers.md). When a competitor dominates a
// run, the run alone can't say whether the web agrees (a content problem) or the assistants'
// own queries find them (a query problem). After a run, with [exa] configured, each prompt is
// searched once on Exa with no domain filter, and the pages a neutral index puts first are
// stored per run and prompt in retrieval_baseline, the shape of coverage. Report pages read the
// stored results, set them against what the run's answers retrieved and cited, and never search.

func init() {
	RegisterPass(Pass{
		Name: "baseline", Command: "baseline", Order: 25, Provider: config.ExaProvider,
		Usage: "search the web for each prompt, no domain filter ([exa])",
		On:    (*config.Config).BaselineOn,
		Run:   (*App).CheckBaseline,
		Print: func(a *App, ctx context.Context, id string, n int) {
			fmt.Printf("searched %d prompts\n", n)
			res, err := a.Runs.Results(ctx, id)
			if err != nil {
				fmt.Println("cheapseats:", err)
				return
			}
			b := a.Baseline(ctx, id, res)
			if b == nil {
				return
			}
			fmt.Printf("  %-40s %-14s %7s %10s  %s\n", "host", "kind", "neutral", "assistants", "reading")
			for _, h := range b.Hosts {
				kind := h.Class
				if h.Competitor != "" {
					kind = h.Competitor
				}
				fmt.Printf("  %-40s %-14s %7d %10d  %s\n", h.Host, kind, h.Neutral, h.Assistants, h.Reading)
			}
		},
	})
}

// CheckBaseline searches each of the run's prompts on Exa with no domain filter and stores what
// it returns. Prompts already searched for this run are skipped, so re-running it costs nothing.
// It returns how many prompts were searched.
func (a *App) CheckBaseline(ctx context.Context, id string) (int, error) {
	if m := config.Missing(config.ExaProvider); m != "" {
		return 0, fmt.Errorf("exa: %s", m)
	}
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	done := map[int]bool{}
	rows, err := a.DB.QueryContext(ctx, `SELECT prompt_idx FROM retrieval_baseline WHERE run_id=?`, id)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var i int
		if rows.Scan(&i) == nil {
			done[i] = true
		}
	}
	rows.Close()
	client := a.Exa()
	n := 0
	total := 0.0
	for i, p := range r.Snapshot.Set.Prompts {
		if done[i] || ctx.Err() != nil {
			continue
		}
		resp, err := client.Search(ctx, "baseline/"+id, p.Text, nil, a.Cfg.BaselineResults())
		if err != nil {
			return n, err
		}
		pages := resp.Results
		if pages == nil {
			pages = []exa.Result{}
		}
		js, _ := json.Marshal(pages)
		if _, err := a.DB.ExecContext(ctx, `INSERT OR REPLACE INTO retrieval_baseline(run_id, prompt_idx, pages, cost_usd, checked_at) VALUES (?,?,?,?,?)`, id, i, string(js), resp.CostUSD, store.Now()); err != nil {
			return n, err
		}
		n++
		total += resp.CostUSD
	}
	if n > 0 {
		slog.Info("baseline", "run", id, "prompts", n, "cost_usd", fmt.Sprintf("%.4f", total))
	}
	return n, nil
}

// BaselinePage is a page a neutral index puts in the top results for a prompt: whose it is
// (its source class, and the competitor when it's one of theirs), and whether the run's answers
// to that prompt cited it, only retrieved it, or never saw it.
type BaselinePage struct {
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	Status     string `json:"status"` // cited | retrieved | unseen
	Class      string `json:"class"`
	Competitor string `json:"competitor,omitempty"`
}

// BaselineRow is a prompt's neutral top pages (none when the index found nothing).
type BaselineRow struct {
	Pages   []BaselinePage `json:"pages"`
	CostUSD float64        `json:"cost_usd"`
}

// The readings of a host's row: what its two counts say about why it shows up.
const (
	// ReadingContent: a neutral index ranks the host for at least two prompts, at least as often
	// as the assistants find it. The web agrees with the assistants, or goes further: a content
	// problem, if the host is a competitor's.
	ReadingContent = "content"
	// ReadingQuery: the assistants retrieve or cite the host for two or more prompts and a neutral
	// search never ranks it. Their queries find it, not the web: a query problem.
	ReadingQuery = "query"
	// ReadingBoth: neither pattern is clear.
	ReadingBoth = "both"
)

// BaselineHost is one row of the per-host table: over the prompts the baseline covers, how many a
// neutral index ranks the host for (Neutral) and how many any answer retrieved or cited it for
// (Assistants), and the reading of the two. A competitor's configured domains are one row, named
// for the competitor, and the brand's own sites are one row.
type BaselineHost struct {
	Host       string `json:"host"`
	Class      string `json:"class"`
	Competitor string `json:"competitor,omitempty"`
	Neutral    int    `json:"neutral"`
	Assistants int    `json:"assistants"`
	Reading    string `json:"reading"` // content | query | both
}

// Baseline is a run's stored neutral retrieval: the pages per prompt index, and the host table.
type Baseline struct {
	Prompts map[int]*BaselineRow `json:"prompts"`
	Hosts   []BaselineHost       `json:"hosts"`
	// How many prompts the baseline covers: the counts in Hosts are out of this many.
	PromptCount int `json:"prompt_count"`
}

// reading is the row's reading from its two counts.
func reading(neutral, assistants int) string {
	switch {
	case neutral >= 2 && neutral >= assistants:
		return ReadingContent
	case assistants >= 2 && neutral == 0:
		return ReadingQuery
	}
	return ReadingBoth
}

// Baseline is a run's stored neutral retrieval against its answers, or nil when the run was
// never searched. The host table spans the prompts that were searched, so both counts are out of
// the same prompts, and lists the hosts a neutral search ranked at all and those the assistants
// found for two or more prompts; a host one answer retrieved once says nothing.
func (a *App) Baseline(ctx context.Context, id string, results []run.Result) *Baseline {
	rows, err := a.DB.QueryContext(ctx, `SELECT prompt_idx, pages, cost_usd FROM retrieval_baseline WHERE run_id=?`, id)
	if err != nil {
		return nil
	}
	stored := map[int][]exa.Result{}
	costs := map[int]float64{}
	for rows.Next() {
		var idx int
		var js string
		var cost float64
		if rows.Scan(&idx, &js, &cost) != nil {
			continue
		}
		var pages []exa.Result
		json.Unmarshal([]byte(js), &pages)
		stored[idx] = pages
		costs[idx] = cost
	}
	rows.Close()
	if len(stored) == 0 {
		return nil
	}
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return nil
	}
	brand, competitors := a.RunBrand(r), a.RunCompetitors(r)
	cls := report.NewClassifier(brand, competitors, a.Cfg.Sources)
	// The row a URL counts under: the brand's sites as one, each competitor's domains as one,
	// every other host as itself. A shared row is named for its first configured site, not for
	// whichever of its pages happened to be counted first.
	type rowKey struct{ class, competitor, host string }
	firstSite := map[string]string{}
	for _, c := range competitors {
		if len(c.Domains) > 0 {
			firstSite[c.Name] = config.CleanSite(c.Domains[0])
		}
	}
	if sites := brand.AllSites(); len(sites) > 0 {
		firstSite[""] = config.CleanSite(sites[0].URL)
	}
	keyOf := func(u string) (rowKey, string, string) {
		class, comp := cls.Class(u)
		host := report.Host(report.NormalizeURL(u))
		switch class {
		case report.Own:
			return rowKey{class: class}, class, comp
		case report.Competitor:
			return rowKey{class: class, competitor: comp}, class, comp
		}
		return rowKey{class: class, host: host}, class, comp
	}
	type counts struct {
		host                string
		class, competitor   string
		neutral, assistants map[int]bool
	}
	hosts := map[rowKey]*counts{}
	tally := func(u string, idx int, neutral bool) {
		k, class, comp := keyOf(u)
		c := hosts[k]
		if c == nil {
			host := report.Host(report.NormalizeURL(u))
			if k.host == "" {
				if site, ok := firstSite[comp]; ok {
					host = site
				}
			}
			c = &counts{host: host, class: class, competitor: comp, neutral: map[int]bool{}, assistants: map[int]bool{}}
			hosts[k] = c
		}
		if neutral {
			c.neutral[idx] = true
		} else {
			c.assistants[idx] = true
		}
	}
	out := &Baseline{Prompts: map[int]*BaselineRow{}, PromptCount: len(stored)}
	for idx, pages := range stored {
		row := &BaselineRow{Pages: []BaselinePage{}, CostUSD: costs[idx]}
		cited, seen := map[string]bool{}, map[string]bool{}
		for _, x := range results {
			if x.PromptIdx != idx || x.Answer == nil {
				continue
			}
			for _, c := range x.Answer.Citations {
				cited[report.NormalizeURL(c.URL)] = true
				tally(c.URL, idx, false)
			}
			for _, q := range x.Answer.Searches {
				for _, l := range q.Results {
					seen[report.NormalizeURL(l.URL)] = true
					tally(l.URL, idx, false)
				}
			}
			for _, f := range x.Answer.Fetches {
				seen[report.NormalizeURL(f)] = true
				tally(f, idx, false)
			}
		}
		for _, p := range pages {
			u := report.NormalizeURL(p.URL)
			st := "unseen"
			switch {
			case cited[u]:
				st = "cited"
			case seen[u]:
				st = "retrieved"
			}
			class, comp := cls.Class(p.URL)
			row.Pages = append(row.Pages, BaselinePage{URL: p.URL, Title: p.Title, Status: st, Class: class, Competitor: comp})
			tally(p.URL, idx, true)
		}
		out.Prompts[idx] = row
	}
	out.Hosts = []BaselineHost{}
	for _, c := range hosts {
		n, s := len(c.neutral), len(c.assistants)
		if n == 0 && s < 2 {
			continue
		}
		out.Hosts = append(out.Hosts, BaselineHost{Host: c.host, Class: c.class, Competitor: c.competitor, Neutral: n, Assistants: s, Reading: reading(n, s)})
	}
	// The clearest rows first: by the larger of the two counts, then by neutral, then by host.
	sort.Slice(out.Hosts, func(i, j int) bool {
		a, b := out.Hosts[i], out.Hosts[j]
		if max(a.Neutral, a.Assistants) != max(b.Neutral, b.Assistants) {
			return max(a.Neutral, a.Assistants) > max(b.Neutral, b.Assistants)
		}
		if a.Neutral != b.Neutral {
			return a.Neutral > b.Neutral
		}
		return a.Host < b.Host
	})
	return out
}
