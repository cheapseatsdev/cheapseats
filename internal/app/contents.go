package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// Reading a page through Exa (docs/optional-providers.md). The site audit and the citation check
// read pages with plain HTTP, as a crawler that runs no JavaScript does. A page that is empty
// without JavaScript, or a bot challenge served instead of the page, then reads as nothing: a
// "needs JavaScript" finding about a page whose content is there once rendered, or a cited page
// that names nobody. With [exa] configured and contents_fallback on, such a page is read once more
// through Exa's contents endpoint, which renders it, and the text (clipped as the citation check
// clips a page, audit.ClipText) is kept in exa_contents for a day, so neither check pays for a
// page twice in a day. The result carries via: "exa". This is not a pass: it runs inside the two
// checks, which run on demand and never from a page.

// contentsTTL is how long a page's text read through Exa serves both checks.
const contentsTTL = 24 * time.Hour

// ContentsReader is the audit.Reader the site audit and the citation check fall back to, or nil
// when the fallback is off ([exa] absent or contents_fallback = false), Exa isn't callable here
// (no key and no proxy), or [pricing.exa-contents] has no page_per_1k: no price, no call, and the
// checks read what plain HTTP got, as they did without it.
func (a *App) ContentsReader() audit.Reader {
	if !a.Cfg.ContentsFallbackOn() {
		return nil
	}
	if p, ok := a.Cfg.Pricing[exa.ContentsModel]; !ok || p.PagePer1K <= 0 {
		slog.Info("exa contents fallback off", "reason", "no [pricing.exa-contents] page_per_1k")
		return nil
	}
	if m := config.Missing(config.ExaProvider); m != "" {
		slog.Info("exa contents fallback off", "reason", m)
		return nil
	}
	client := a.Exa()
	a.DB.Exec(`DELETE FROM exa_contents WHERE fetched_at < ?`, time.Now().UTC().Add(-contentsTTL).Format(time.RFC3339))
	return func(ctx context.Context, u string) (text, title string, ok bool) {
		since := time.Now().UTC().Add(-contentsTTL).Format(time.RFC3339)
		if err := a.DB.QueryRowContext(ctx, `SELECT text, title FROM exa_contents WHERE url=? AND fetched_at>?`, u, since).Scan(&text, &title); err == nil {
			return text, title, text != ""
		}
		resp, err := client.Contents(ctx, "contents", []string{u})
		if err != nil {
			slog.Warn("exa contents", "url", u, "err", err)
			return "", "", false
		}
		var page *exa.Page
		for i := range resp.Results {
			if strings.TrimSpace(resp.Results[i].Text) != "" {
				page = &resp.Results[i]
				break
			}
		}
		if page == nil {
			return "", "", false // Exa couldn't read it either; nothing kept, so a later check may try again
		}
		text = audit.ClipText(strings.Join(strings.Fields(page.Text), " "))
		if _, err := a.DB.ExecContext(ctx, `INSERT OR REPLACE INTO exa_contents (url, title, text, cost_usd, fetched_at) VALUES (?,?,?,?,?)`, u, page.Title, text, resp.CostUSD, store.Now()); err != nil {
			slog.Warn("exa contents: keeping the text", "url", u, "err", err)
		}
		slog.Info("exa contents", "url", u, "chars", len(text), "cost_usd", fmt.Sprintf("%.4f", resp.CostUSD))
		return text, page.Title, true
	}
}
