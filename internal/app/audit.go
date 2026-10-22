package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/metcalfc/popcorn/internal/audit"
	"github.com/metcalfc/popcorn/internal/crawl"
	"github.com/metcalfc/popcorn/internal/report"
)

// checking is the site audits and citation checks in progress, by App and kind, so the web UI can
// say so and doesn't start a second.
var checking sync.Map

func (a *App) checkKey(kind string) string { return fmt.Sprintf("%p/%s", a, kind) }

// Checking reports whether a site audit ("audit") or a run's citation check ("cite/<run>") is in
// progress.
func (a *App) Checking(kind string) bool {
	_, ok := checking.Load(a.checkKey(kind))
	return ok
}

// AuditSites is the brand's sites (and domains), as the audit takes them.
func (a *App) AuditSites() []string {
	var out []string
	for _, s := range a.Cfg.Brand.AllSites() {
		out = append(out, s.URL)
	}
	return out
}

// IsBrandSite reports whether a site is one of the brand's.
func (a *App) IsBrandSite(site string) bool {
	for _, s := range a.AuditSites() {
		if strings.EqualFold(strings.TrimSuffix(s, "/"), strings.TrimSuffix(site, "/")) {
			return true
		}
	}
	return false
}

// Audit checks the sites (the brand's when none are given) and, when they're all the brand's,
// keeps the result as the latest audit. Free: plain HTTP, no model calls.
func (a *App) Audit(ctx context.Context, sites []string, o audit.Options) (audit.Result, error) {
	if len(sites) == 0 {
		sites = a.AuditSites()
	}
	if len(sites) == 0 {
		return audit.Result{}, errors.New("no sites to audit: list the brand's sites in popcorn.toml ([[brand.sites]])")
	}
	if _, busy := checking.LoadOrStore(a.checkKey("audit"), true); busy {
		return audit.Result{}, errors.New("a site audit is already running")
	}
	defer checking.Delete(a.checkKey("audit"))
	if o.Hosted == nil {
		o.Hosted = map[string]bool{}
		for _, s := range a.Cfg.Brand.AllSites() {
			if s.Kind == "code" || s.Kind == "community" {
				o.Hosted[s.URL] = true
			}
		}
	}
	res := audit.Run(ctx, sites, o)
	for _, s := range sites {
		if !a.IsBrandSite(s) {
			return res, nil // someone else's site: shown, not kept
		}
	}
	return res, audit.Save(ctx, a.DB.DB, res)
}

// LatestAudit is the newest kept audit, or nil.
func (a *App) LatestAudit(ctx context.Context) (*audit.Result, error) {
	return audit.Latest(ctx, a.DB.DB)
}

// siteFindings is the latest audit's errors and warnings on the brand's sites, if it's recent, for
// the fix list.
func (a *App) siteFindings(ctx context.Context) []string {
	r, err := audit.Latest(ctx, a.DB.DB)
	if err != nil || r == nil {
		return nil
	}
	if at, err := time.Parse(time.RFC3339, r.At); err != nil || time.Since(at) > 30*24*time.Hour {
		return nil
	}
	var out []string
	for _, f := range r.Findings {
		if f.Severity != audit.Info && a.IsBrandSite(f.Site) && len(out) < 40 {
			out = append(out, f.String())
		}
	}
	return out
}

// CitationTargets is the n third-party pages a run's answers drew on most: cited first, then
// surfaced in searches.
func CitationTargets(s report.Summary, n int) []audit.Target {
	var out []audit.Target
	for _, p := range s.Pages { // already most cited first
		if p.FirstParty || p.Cited+p.Surfaced == 0 {
			continue
		}
		if len(out) == n {
			break
		}
		out = append(out, audit.Target{URL: p.URL, Cited: p.Cited, Surfaced: p.Surfaced})
	}
	return out
}

// CheckCitations fetches the n third-party pages a run's answers drew on most (25 when n <= 0) and
// keeps which name the brand and which competitors. Free: plain HTTP, no model calls.
func (a *App) CheckCitations(ctx context.Context, runID string, n int, o audit.CiteOptions) (audit.CitationCheck, error) {
	if n <= 0 {
		n = 25
	}
	r, err := a.Runs.Get(ctx, runID)
	if err != nil {
		return audit.CitationCheck{}, err
	}
	res, err := a.Runs.Results(ctx, runID)
	if err != nil {
		return audit.CitationCheck{}, err
	}
	key := a.checkKey("cite/" + runID)
	if _, busy := checking.LoadOrStore(key, true); busy {
		return audit.CitationCheck{}, errors.New("this run's citation check is already running")
	}
	defer checking.Delete(key)
	b := a.RunBrand(r)
	o.DB = a.DB.DB
	c := audit.CheckCitations(ctx, CitationTargets(a.Summary(r, res), n), append([]string{b.Name}, b.Aliases...), r.Snapshot.Set.Competitors, o)
	c.RunID = runID
	return c, audit.SaveCheck(ctx, a.DB.DB, c)
}

// CitationCheck is a run's latest citation check, or nil.
func (a *App) CitationCheck(ctx context.Context, runID string) (*audit.CitationCheck, error) {
	return audit.LatestCheck(ctx, a.DB.DB, runID)
}

// ImportCrawl counts a log's AI crawler fetches and referred visits. Host is the site for lines
// that don't record one.
func (a *App) ImportCrawl(ctx context.Context, r io.Reader, host string) (crawl.Stats, error) {
	return crawl.Import(ctx, a.DB.DB, r, host)
}

// Crawlers is the last days of imported crawler counts, page by page, beside how often the latest
// finished run of every set cited and surfaced each of the brand's pages.
func (a *App) Crawlers(ctx context.Context, days int) (crawl.Report, error) {
	if days <= 0 {
		days = 30
	}
	cited := map[[2]string]crawl.Use{}
	for _, s := range a.Sets() {
		var id string
		err := a.DB.QueryRowContext(ctx, `SELECT id FROM runs WHERE set_id=? AND status='done' ORDER BY created_at DESC LIMIT 1`, s.ID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return crawl.Report{}, err
		}
		r, err := a.Runs.Get(ctx, id)
		if err != nil {
			continue
		}
		res, err := a.Runs.Results(ctx, id)
		if err != nil {
			continue
		}
		for _, p := range a.Summary(r, res).Pages {
			if !p.FirstParty {
				continue
			}
			h, path := crawl.Key(p.URL)
			u := cited[[2]string{h, path}]
			u.Cited += p.Cited
			u.Surfaced += p.Surfaced
			cited[[2]string{h, path}] = u
		}
	}
	return crawl.Build(ctx, a.DB.DB, days, cited)
}
