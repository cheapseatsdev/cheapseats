package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// Did the fix land, and did a cited page flip: two readings of pages the citation check's fetcher
// already reads, with nothing stored beyond its page_fetches day cache and its checks.

// LandingURL is the page a fix on a brand site asks to change: its target, with https:// when
// it was written without a scheme. Empty for a fix not on one of the brand's sites.
func LandingURL(f analysis.Fix) string {
	t := strings.TrimSpace(f.Target)
	if f.Site == "" || t == "" || strings.ContainsAny(t, " \t\n") {
		return ""
	}
	if !strings.Contains(t, "://") {
		t = "https://" + strings.TrimPrefix(t, "//")
	}
	return t
}

// landingTerms is the brand's names and the fix's claim terms, as a landing is read for.
func (a *App) landingTerms(f analysis.Fix) (brand, terms []string) {
	return append([]string{a.Cfg.Brand.Name}, a.Cfg.Brand.Aliases...), audit.ClaimTerms(f.Claim)
}

// Landing is what a fix's target page on a brand site said the last time it was read, from the
// day cache, or nil when it hasn't been read (or the fix isn't on a brand site). When today's
// cache has no reading it starts one in the background, so showing a fix fetches its page at most
// once a day and never waits on it. Evidence for "done when"; it counts toward no outcome.
func (a *App) Landing(ctx context.Context, f analysis.Fix) *audit.Landing {
	u := LandingURL(f)
	if u == "" {
		return nil
	}
	brand, terms := a.landingTerms(f)
	l, fresh := audit.CachedLanding(ctx, a.DB.DB, u, time.Now().UTC().Format("2006-01-02"), brand, terms)
	if !fresh {
		key := a.checkKey("land/" + u)
		if _, busy := checking.LoadOrStore(key, true); !busy {
			go func() {
				defer checking.Delete(key)
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				a.ReadLanding(ctx, f)
			}()
		}
	}
	return l
}

// ReadLanding reads a fix's target page now (through today's cache), as Landing does in the
// background; the zero Landing for a fix not on a brand site.
func (a *App) ReadLanding(ctx context.Context, f analysis.Fix) audit.Landing {
	u := LandingURL(f)
	if u == "" {
		return audit.Landing{}
	}
	brand, terms := a.landingTerms(f)
	return audit.CheckLanding(ctx, u, brand, terms, audit.CiteOptions{DB: a.DB.DB})
}

// CitationFlips is each page of a run's citation check whose reading changed since the check of
// the newest earlier run of the same set that has one: none when either run has no check.
func (a *App) CitationFlips(ctx context.Context, r *run.Run) []audit.PageFlip {
	cur, err := a.CitationCheck(ctx, r.ID)
	if err != nil || cur == nil {
		return nil
	}
	var js string
	if err := a.DB.QueryRowContext(ctx, `SELECT c.result FROM citation_checks c JOIN runs r ON r.id = c.run_id
		WHERE r.set_id = ? AND r.created_at < ? ORDER BY r.created_at DESC LIMIT 1`, r.SetID, r.CreatedAt).Scan(&js); err != nil {
		return nil
	}
	var prev audit.CitationCheck
	if err := json.Unmarshal([]byte(js), &prev); err != nil {
		slog.Error("previous citation check", "run", r.ID, "err", err)
		return nil
	}
	return audit.Flips(prev, *cur)
}
