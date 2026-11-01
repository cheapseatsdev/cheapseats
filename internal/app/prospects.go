package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/redact"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// Outreach prospects (docs/optional-providers.md). The outreach list is the hosts the answers
// already cited without the brand: reactive. After a run, with [exa] configured, the listicles
// and comparisons the answers leaned on most are each put to Exa's find-similar, and the pages
// it returns that no answer cited or retrieved are stored in prospects: pages like the ones
// that shaped the answers, that the run didn't know about. With [typesafe] on, Jev says how
// likely each is a roundup or comparison in the brand's category, and the citation fetcher
// (plain HTTP, robots-respecting, once a day per page) reads whether it names the brand or a
// competitor. Pages read the table and call nothing.

func init() {
	RegisterPass(Pass{
		Name: "prospects", Command: "prospect", Order: 50, Provider: config.ExaProvider,
		Usage: "pages like the listicles the answers leaned on, as outreach ([exa])",
		On:    (*config.Config).ProspectsOn,
		Run:   (*App).FindProspects,
		Print: func(a *App, ctx context.Context, id string, n int) {
			fmt.Printf("stored %d new prospects\n", n)
			res, err := a.Runs.Results(ctx, id)
			if err != nil {
				fmt.Println("cheapseats:", err)
				return
			}
			for _, p := range a.Prospects(ctx, id, res) {
				fmt.Printf("  %s\n    %s\n", p.URL, p.Why())
			}
		},
	})
}

// Prospect is a page a neutral index finds similar to a cited listicle or comparison, and what
// the pass learned about it.
type Prospect struct {
	URL   string `json:"url"`
	Host  string `json:"host"`
	Title string `json:"title,omitempty"`
	// The cited page it was found from, its title when a search in the run gave one, and how many
	// of the run's answers cited it.
	Seed      string `json:"seed"`
	SeedTitle string `json:"seed_title,omitempty"`
	SeedCited int    `json:"seed_cited"`
	// Jev's probability that it is a roundup, comparison or list of products in the category;
	// nil when [typesafe] was off when the pass ran.
	InCategory *float64 `json:"in_category,omitempty"`
	// Whether the page was read, and if so whom it names.
	Read        bool     `json:"read"`
	NamesBrand  bool     `json:"names_brand"`
	Competitors []string `json:"competitors"`
	// It names competitors but not the brand: the page to get into, like report.Outreach.
	Outreach bool    `json:"outreach"`
	CostUSD  float64 `json:"cost_usd"`
	FoundAt  string  `json:"found_at"`
}

// Category is InCategory for templates, 0 when Jev wasn't asked.
func (p Prospect) Category() float64 {
	if p.InCategory == nil {
		return 0
	}
	return *p.InCategory
}

// Why is the one-line reason the page is listed, for MCP and the CLI.
func (p Prospect) Why() string {
	seed := p.SeedTitle
	if seed == "" {
		seed = p.Seed
	}
	parts := []string{"similar to " + seed}
	switch {
	case !p.Read:
		parts = append(parts, "not read")
	case p.NamesBrand && len(p.Competitors) > 0:
		parts = append(parts, "names the brand and "+strings.Join(p.Competitors, ", "))
	case p.NamesBrand:
		parts = append(parts, "names the brand")
	case len(p.Competitors) > 0:
		parts = append(parts, "names "+strings.Join(p.Competitors, ", ")+" and not the brand")
	default:
		parts = append(parts, "names nobody")
	}
	if p.InCategory != nil {
		parts = append(parts, fmt.Sprintf("in category %.2f", *p.InCategory))
	}
	return strings.Join(parts, "; ")
}

// FindProspects finds pages like the run's most-cited third-party listicles and comparisons
// and stores the ones no answer cited or retrieved, with Jev's in-category probability when
// [typesafe] is callable and what the page names. Seeds already searched for this run are
// skipped, so re-running it costs nothing. It returns how many pages were stored.
func (a *App) FindProspects(ctx context.Context, id string) (int, error) {
	if m := config.Missing(config.ExaProvider); m != "" {
		return 0, fmt.Errorf("exa: %s", m)
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
	competitors := a.RunCompetitors(r)
	sum := a.Summary(r, results)

	// The seeds: the most-cited third-party listicles and comparisons (Pages is most cited first).
	var seeds []report.Page
	for _, p := range sum.Pages {
		if p.FirstParty || p.CompetitorSite != "" || p.Cited == 0 || (p.Type != report.Listicle && p.Type != report.Comparison) {
			continue
		}
		if len(seeds) == a.Cfg.ProspectSeeds() {
			break
		}
		seeds = append(seeds, p)
	}
	if len(seeds) == 0 {
		return 0, nil
	}
	done := map[string]bool{}
	rows, err := a.DB.QueryContext(ctx, `SELECT DISTINCT seed FROM prospects WHERE run_id=?`, id)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			done[s] = true
		}
	}
	rows.Close()

	// Never the brand's or a competitor's own site: those aren't outreach. Domains are hosts or
	// host/path prefixes, as the config has them and as Exa takes them.
	var exclude []string
	excluded := map[string]bool{}
	addDomain := func(d string) {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(d), "https://"), "http://"), "/"))
		d = strings.TrimPrefix(d, "www.")
		if d != "" && !excluded[d] {
			excluded[d] = true
			exclude = append(exclude, d)
		}
	}
	for _, s := range brand.AllSites() {
		addDomain(s.URL)
	}
	for _, c := range competitors {
		for _, d := range c.Domains {
			addDomain(d)
		}
	}
	onExcluded := func(u string) bool {
		hp := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://"))
		hp = strings.TrimPrefix(hp, "www.")
		if i := strings.IndexAny(hp, "?#"); i >= 0 {
			hp = hp[:i]
		}
		h := report.Host(u)
		for d := range excluded {
			if strings.Contains(d, "/") {
				if hp == d || strings.HasPrefix(hp, d+"/") {
					return true
				}
			} else if h == d || strings.HasSuffix(h, "."+d) {
				return true
			}
		}
		return false
	}
	// Everything the run already saw: cited, retrieved by a search, or fetched.
	seen := map[string]bool{}
	for _, x := range results {
		if x.Answer == nil {
			continue
		}
		for _, c := range x.Answer.Citations {
			seen[report.NormalizeURL(c.URL)] = true
		}
		for _, q := range x.Answer.Searches {
			for _, l := range q.Results {
				seen[report.NormalizeURL(l.URL)] = true
			}
		}
		for _, f := range x.Answer.Fetches {
			seen[report.NormalizeURL(f)] = true
		}
	}
	rows, err = a.DB.QueryContext(ctx, `SELECT url FROM prospects WHERE run_id=?`, id)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil {
			seen[report.NormalizeURL(u)] = true
		}
	}
	rows.Close()

	type found struct {
		url, title, seed string
		cost             float64
		inCategory       sql.NullFloat64
	}
	var pages []found
	client := a.Exa()
	searches, total := 0, 0.0
	for _, s := range seeds {
		if done[s.URL] || ctx.Err() != nil {
			continue
		}
		resp, err := client.FindSimilar(ctx, "prospects/"+id, s.URL, exclude, a.Cfg.ProspectResults())
		if err != nil {
			return 0, err
		}
		searches++
		total += resp.CostUSD
		cost := resp.CostUSD // on the first page kept from this seed, so the rows add up to the spend
		for _, res := range resp.Results {
			u := report.NormalizeURL(res.URL)
			if u == "" || seen[u] || onExcluded(u) {
				continue
			}
			seen[u] = true
			pages = append(pages, found{url: u, title: strings.TrimSpace(res.Title), seed: s.URL, cost: cost})
			cost = 0
		}
	}
	if len(pages) == 0 {
		if searches > 0 {
			slog.Info("prospects", "run", id, "searches", searches, "pages", 0, "cost_usd", fmt.Sprintf("%.4f", total))
		}
		return 0, nil
	}

	// Jev, one page per request: is it a roundup in the category? Skipped, leaving NULL, when
	// [typesafe] is off or not callable here; the page is listed either way.
	if a.Cfg.TypeSafe != nil && config.Missing(config.TypeSafeProvider) == "" {
		jev := a.TypeSafe()
		category := categoryOf(brand)
		for i := range pages {
			if ctx.Err() != nil {
				break
			}
			state := map[string]any{"url": pages[i].url, "title": pages[i].title, "category": category}
			resp, err := jev.Ask(ctx, "prospects/"+id, state, map[string]typesafe.Question{
				"in_category": {
					Type:         "noul",
					Instructions: "`url` and `title` are a web page. It is a roundup, comparison or list of products in `category`: a page that names several products of that kind and ranks, compares or recommends among them. One product's own site, a news story, documentation, a forum thread or a general article about the field is not.",
				},
			})
			if err != nil {
				slog.Warn("prospects: jev", "run", id, "url", redact.URL(pages[i].url), "err", err)
				break
			}
			if ans, ok := resp.Answers["in_category"]; ok {
				pages[i].inCategory = sql.NullFloat64{Float64: typesafe.Chosen(ans), Valid: true}
			}
			pages[i].cost += resp.CostUSD
			total += resp.CostUSD
		}
	}

	// Read each page through the citation fetcher: who does it name? Free, robots-respecting, and
	// cached for the day in page_fetches.
	targets := make([]audit.Target, len(pages))
	for i, p := range pages {
		targets[i] = audit.Target{URL: p.url}
	}
	check := audit.CheckCitations(ctx, targets, append([]string{brand.Name}, brand.Aliases...), competitors, audit.CiteOptions{DB: a.DB.DB})

	now := store.Now()
	n := 0
	for i, p := range pages {
		var namesBrand sql.NullInt64
		comps := []string{}
		if cp := check.Pages[i]; cp.Skipped == "" {
			namesBrand = sql.NullInt64{Int64: map[bool]int64{false: 0, true: 1}[cp.NamesBrand], Valid: true}
			comps = cp.Competitors
		}
		js, _ := json.Marshal(comps)
		if _, err := a.DB.ExecContext(ctx, `INSERT OR IGNORE INTO prospects(run_id, url, title, seed, in_category, names_brand, competitors, cost_usd, found_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, p.url, p.title, p.seed, p.inCategory, namesBrand, string(js), p.cost, now); err != nil {
			return n, err
		}
		n++
	}
	slog.Info("prospects", "run", id, "searches", searches, "pages", n, "cost_usd", fmt.Sprintf("%.4f", total))
	return n, nil
}

// Prospects is a run's stored prospects at today's threshold: a page Jev judged is listed when
// its in-category probability is at least typesafe.min_confidence, a page Jev never saw always.
// Ordered by: names competitors but not the brand first, then Jev's probability, then how many
// answers cited the seed. Nil when the run has none.
func (a *App) Prospects(ctx context.Context, id string, results []run.Result) []Prospect {
	rows, err := a.DB.QueryContext(ctx, `SELECT url, title, seed, in_category, names_brand, competitors, cost_usd, found_at FROM prospects WHERE run_id=?`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	// The seeds' titles and citation counts, from the run's answers.
	cited := map[string]int{}
	titles := map[string]string{}
	for _, x := range results {
		if x.Answer == nil {
			continue
		}
		once := map[string]bool{}
		for _, c := range x.Answer.Citations {
			u := report.NormalizeURL(c.URL)
			if !once[u] {
				once[u] = true
				cited[u]++
			}
			if c.Title != "" && titles[u] == "" {
				titles[u] = c.Title
			}
		}
		for _, q := range x.Answer.Searches {
			for _, l := range q.Results {
				if u := report.NormalizeURL(l.URL); l.Title != "" && titles[u] == "" {
					titles[u] = l.Title
				}
			}
		}
	}
	min := a.Cfg.MinConfidence()
	var out []Prospect
	for rows.Next() {
		var p Prospect
		var inCat sql.NullFloat64
		var names sql.NullInt64
		var comps string
		if rows.Scan(&p.URL, &p.Title, &p.Seed, &inCat, &names, &comps, &p.CostUSD, &p.FoundAt) != nil {
			continue
		}
		if inCat.Valid {
			if inCat.Float64 < min {
				continue
			}
			v := inCat.Float64
			p.InCategory = &v
		}
		p.Host = report.Host(p.URL)
		p.Read, p.NamesBrand = names.Valid, names.Int64 == 1
		p.Competitors = []string{}
		json.Unmarshal([]byte(comps), &p.Competitors)
		if p.Competitors == nil {
			p.Competitors = []string{}
		}
		p.Outreach = p.Read && !p.NamesBrand && len(p.Competitors) > 0
		seed := report.NormalizeURL(p.Seed)
		p.SeedTitle, p.SeedCited = titles[seed], cited[seed]
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Outreach != b.Outreach {
			return a.Outreach
		}
		if ca, cb := a.Category(), b.Category(); ca != cb {
			return ca > cb
		}
		if a.SeedCited != b.SeedCited {
			return a.SeedCited > b.SeedCited
		}
		return a.URL < b.URL
	})
	return out
}
