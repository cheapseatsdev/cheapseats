package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// The coverage check (docs/optional-providers.md). After a run, with [exa] configured, each
// prompt is searched once on Exa, a neutral web index, restricted to the brand's own sites. The
// pages it finds, stored per run and prompt, split a miss in two: no page of yours answers the
// question (a documentation gap), or one does and the assistants never read it (a retrieval
// gap). Report pages read the stored results and never search.

func init() {
	RegisterPass(Pass{
		Name: "coverage", Command: "coverage", Order: 20, Provider: config.ExaProvider,
		Usage: "search the brand's sites for each prompt ([exa])",
		On:    (*config.Config).CoverageOn,
		Run:   (*App).CheckCoverage,
		Print: func(a *App, ctx context.Context, id string, n int) { fmt.Printf("searched %d prompts\n", n) },
	})
}

// Exa is the search client, budgeted and priced through the config.
func (a *App) Exa() *exa.Client { return &exa.Client{Budget: a.Runs.Budget, Cfg: a.Cfg} }

// CheckCoverage searches each of the run's prompts over the brand's sites and stores what Exa
// finds. Prompts already checked for this run are skipped, so re-running it costs nothing. It
// returns how many prompts were searched.
func (a *App) CheckCoverage(ctx context.Context, id string) (int, error) {
	if m := config.Missing(config.ExaProvider); m != "" {
		return 0, fmt.Errorf("exa: %s", m)
	}
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	brand := a.RunBrand(r)
	var domains []string
	for _, s := range brand.AllSites() {
		domains = append(domains, strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s.URL), "https://"), "http://"), "/"))
	}
	if len(domains) == 0 {
		return 0, fmt.Errorf("the brand has no sites to search")
	}
	done := map[int]bool{}
	rows, err := a.DB.QueryContext(ctx, `SELECT prompt_idx FROM coverage WHERE run_id=?`, id)
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
		resp, err := client.Search(ctx, "coverage/"+id, p.Text, domains, a.Cfg.CoverageResults())
		if err != nil {
			return n, err
		}
		pages := resp.Results
		if pages == nil {
			pages = []exa.Result{}
		}
		js, _ := json.Marshal(pages)
		if _, err := a.DB.ExecContext(ctx, `INSERT OR REPLACE INTO coverage(run_id, prompt_idx, pages, cost_usd, checked_at) VALUES (?,?,?,?,?)`, id, i, string(js), resp.CostUSD, store.Now()); err != nil {
			return n, err
		}
		n++
		total += resp.CostUSD
	}
	if n > 0 {
		slog.Info("coverage", "run", id, "prompts", n, "cost_usd", fmt.Sprintf("%.4f", total))
	}
	return n, nil
}

// CoveragePage is one of the brand's pages a neutral index finds for a prompt, and whether the
// run's answers to that prompt cited it, only retrieved it, or never saw it.
type CoveragePage struct {
	URL    string `json:"url"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status"` // cited | retrieved | unseen
}

// CoverageRow is a prompt's coverage: checked, and the pages found (none when empty).
type CoverageRow struct {
	Pages   []CoveragePage `json:"pages"`
	CostUSD float64        `json:"cost_usd"`
}

// Coverage is a run's stored coverage by prompt index, with each page's status against the run's
// answers, or nil when the run was never checked.
func (a *App) Coverage(ctx context.Context, id string, results []run.Result) map[int]*CoverageRow {
	rows, err := a.DB.QueryContext(ctx, `SELECT prompt_idx, pages, cost_usd FROM coverage WHERE run_id=?`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[int]*CoverageRow{}
	for rows.Next() {
		var idx int
		var js string
		var cost float64
		if rows.Scan(&idx, &js, &cost) != nil {
			continue
		}
		var pages []exa.Result
		json.Unmarshal([]byte(js), &pages)
		row := &CoverageRow{Pages: []CoveragePage{}, CostUSD: cost}
		cited, seen := map[string]bool{}, map[string]bool{}
		for _, x := range results {
			if x.PromptIdx != idx || x.Answer == nil {
				continue
			}
			for _, c := range x.Answer.Citations {
				cited[report.NormalizeURL(c.URL)] = true
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
		for _, p := range pages {
			u := report.NormalizeURL(p.URL)
			st := "unseen"
			switch {
			case cited[u]:
				st = "cited"
			case seen[u]:
				st = "retrieved"
			}
			row.Pages = append(row.Pages, CoveragePage{URL: p.URL, Title: p.Title, Status: st})
		}
		out[idx] = row
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
