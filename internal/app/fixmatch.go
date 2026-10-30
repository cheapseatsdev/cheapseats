package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// Fix de-duplication across sets (docs/optional-providers.md). Quick wins list the latest fix
// list of every set, so three sets that each want a pricing page show three cards. After a run's
// fix list is built, with [typesafe] configured, each pair of fixes from different sets that
// could be the same page is decided once: equal targets by rule, the rest by one Jev question
// with both fixes in state. The decisions are stored (fix_matches) with the probability, and
// QuickWins reads them back at today's threshold to fold the matched cards into one, so no page
// ever calls Jev and switching it off leaves the list as it was.

func init() {
	RegisterPass(Pass{
		Name: "fix-matches", Command: "match-fixes", Order: 110, Provider: config.TypeSafeProvider,
		Usage: "ask Jev which sets' fixes ask for the same page ([typesafe])",
		On:    (*config.Config).FixMatchesOn,
		Run:   (*App).MatchFixes,
		Print: printFixMatches,
	})
}

// AlsoIn is another set whose latest fix list asks for the same page as a quick win. RunID and
// Rank are what mark_fix takes for that set's fix.
type AlsoIn struct {
	SetID   string `json:"set_id"`
	SetName string `json:"set_name"`
	RunID   string `json:"run_id"`
	Rank    int    `json:"rank"`
	Title   string `json:"title"`
	// The stored probability that this fix and the card's are one page: 1 for equal targets,
	// Jev's for the rest, and 0 when the two met only through a third fix.
	Confidence float64 `json:"confidence,omitempty"`
}

// InSet reports whether a quick win came from the set, or is also asked for by it.
func (q QuickWin) InSet(id string) bool {
	if q.SetID == id {
		return true
	}
	for _, x := range q.AlsoIn {
		if x.SetID == id {
			return true
		}
	}
	return false
}

// FixSignature is what identifies a fix across rebuilt lists: its type, target page (TargetKey)
// and title, lower-cased. Two lists that propose the same work in the same words have the same
// signature, so a decision about a pair outlives the runs it was made from.
func FixSignature(f analysis.Fix) string {
	return strings.ToLower(strings.TrimSpace(string(f.Type))) + "|" + TargetKey(f.Target) + "|" + strings.ToLower(strings.Join(strings.Fields(f.Title), " "))
}

// FixMatchKey is the fix_matches key for two signatures, in either order.
func FixMatchKey(a, b string) string {
	if b < a {
		a, b = b, a
	}
	sum := sha256.Sum256([]byte(a + "\n" + b))
	return hex.EncodeToString(sum[:])
}

// latestFix is one fix from the latest fix list of a set, as quick wins read them.
type latestFix struct {
	set   sets.Set
	runID string
	fix   analysis.Fix
}

func (a *App) latestFixes(ctx context.Context) []latestFix {
	var out []latestFix
	for _, s := range a.Sets() {
		var id string
		if err := a.DB.QueryRowContext(ctx, `SELECT id FROM runs WHERE set_id=? AND fixes IS NOT NULL AND status='done' ORDER BY created_at DESC LIMIT 1`, s.ID).Scan(&id); err != nil {
			continue
		}
		fixes, _, _ := a.Fixes(ctx, id)
		for _, f := range fixes {
			f = analysis.Triage(a.Cfg, s, f)
			if f.Verification.Status != "verified" {
				continue
			}
			out = append(out, latestFix{set: s, runID: id, fix: f})
		}
	}
	return out
}

// fixSite is the brand's site a fix lands on, "" when it's off them.
func (a *App) fixSite(f analysis.Fix) string {
	if f.Site != "" {
		return f.Site
	}
	if s, ok := a.Cfg.Brand.SiteOf(f.Target); ok {
		return s.URL
	}
	return ""
}

// MatchFixes decides, for every pair of fixes from different sets' latest fix lists with the
// same type on the same site, whether they ask for the same page: by rule when their targets
// are the same page, by one Jev question when their targets or titles share a meaningful word,
// and not at all otherwise. The run id says which run's fix list triggered it; every set's
// latest list takes part. Pairs already decided are skipped, so re-running it costs nothing.
// It returns how many pairs were decided.
func (a *App) MatchFixes(ctx context.Context, id string) (int, error) {
	if m := config.Missing(config.TypeSafeProvider); m != "" {
		return 0, fmt.Errorf("typesafe: %s", m)
	}
	fixes := a.latestFixes(ctx)
	decided := map[string]bool{}
	rows, err := a.DB.QueryContext(ctx, `SELECT key FROM fix_matches`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return 0, err
		}
		decided[k] = true
	}
	rows.Close()

	type pair struct {
		x, y   latestFix
		sx, sy string
		key    string
	}
	var rule, asks []pair
	for i := range fixes {
		for j := i + 1; j < len(fixes); j++ {
			x, y := fixes[i], fixes[j]
			if x.set.ID == y.set.ID || x.fix.Type != y.fix.Type || a.fixSite(x.fix) != a.fixSite(y.fix) {
				continue
			}
			sx, sy := FixSignature(x.fix), FixSignature(y.fix)
			if sy < sx {
				x, y, sx, sy = y, x, sy, sx
			}
			key := FixMatchKey(sx, sy)
			if decided[key] {
				continue
			}
			decided[key] = true
			p := pair{x: x, y: y, sx: sx, sy: sy, key: key}
			switch {
			case TargetKey(x.fix.Target) == TargetKey(y.fix.Target):
				rule = append(rule, p)
			case shareFixToken(x.fix, y.fix):
				asks = append(asks, p)
			}
		}
	}
	insert := func(p pair, conf float64, source string) error {
		_, err := a.DB.ExecContext(ctx, `INSERT OR REPLACE INTO fix_matches(key, fix_a, fix_b, confidence, source, decided_at) VALUES (?,?,?,?,?,?)`,
			p.key, p.sx, p.sy, conf, source, store.Now())
		return err
	}
	n := 0
	for _, p := range rule {
		if err := insert(p, 1, "rule"); err != nil {
			return n, err
		}
		n++
	}
	if len(asks) == 0 {
		return n, nil
	}

	client := a.TypeSafe()
	category := categoryOf(a.Cfg.Brand)
	cost := 0.0
	// One pair per request, with nothing in the state but the two fixes and the category: Jev's
	// probabilities fall as the state fills with detail unrelated to the question.
	for _, p := range asks {
		state := map[string]any{"category": category, "a": fixState(p.x.fix), "b": fixState(p.y.fix)}
		questions := map[string]typesafe.Question{"same_page": {
			Type:         "noul",
			Instructions: "`a` and `b` are two fixes proposed for the website of a product in `category`, each from a different set of buyer questions put to AI assistants. Each names the page to create or change (`target`), what it must say (`claim`) and when it counts as done (`done_when`). Do they describe work on the same page, so that one page would satisfy both? Two targets that are one URL in different spellings are the same page; so are two pages that would say the same thing under two names. Two pages on one site about different subjects are different pages, even when they would link to each other or share a section.",
		}}
		resp, err := client.Ask(ctx, "fix-matches/"+id, state, questions)
		if err != nil {
			return n, err
		}
		ans, ok := resp.Answers["same_page"]
		if !ok {
			continue
		}
		if err := insert(p, typesafe.Chosen(ans), "jev"); err != nil {
			return n, err
		}
		n++
		cost += resp.CostUSD
	}
	slog.Info("fix matches", "run", id, "asked", len(asks), "by_rule", len(rule), "model", a.Cfg.TypeSafeModel(), "cost_usd", fmt.Sprintf("%.4f", cost))
	return n, nil
}

func fixState(f analysis.Fix) map[string]any {
	return map[string]any{"title": f.Title, "target": f.Target, "claim": f.Claim, "done_when": f.DoneWhen}
}

// fixStopTokens are the words every fix title and path has, on top of stopTokens: sharing one
// says nothing about the page.
var fixStopTokens = map[string]bool{"page": true, "pages": true, "docs": true, "doc": true, "guide": true, "blog": true, "post": true, "add": true, "write": true, "create": true, "publish": true, "new": true, "update": true, "edit": true, "fix": true, "with": true, "for": true, "and": true, "the": true, "our": true, "your": true, "how": true, "that": true, "from": true, "into": true, "about": true, "what": true, "section": true, "www": true, "html": true, "index": true, "site": true}

// fixTokens are the meaningful words of a fix's target path and title; the host is left out,
// since every fix on one site shares it.
func fixTokens(f analysis.Fix) map[string]bool {
	path := ""
	if k := TargetKey(f.Target); strings.Contains(k, "/") {
		path = k[strings.Index(k, "/")+1:]
	}
	out := map[string]bool{}
	for t := range tokens(path + " " + f.Title) {
		if !fixStopTokens[t] {
			out[t] = true
		}
	}
	return out
}

// shareFixToken is the cheap pre-filter that decides which pairs are worth a question.
func shareFixToken(x, y analysis.Fix) bool {
	tx, ty := fixTokens(x), fixTokens(y)
	for t := range tx {
		if ty[t] {
			return true
		}
	}
	return false
}

// FixMatch is one stored decision about a pair of fixes, for the UI and the CLI.
type FixMatch struct {
	Key        string  `json:"key"`
	FixA       string  `json:"fix_a"`
	FixB       string  `json:"fix_b"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
	DecidedAt  string  `json:"decided_at"`
	// Whether the two fixes count as one page today, at the configured confidence.
	Matches bool `json:"matches"`
}

// FixMatches lists the stored decisions, newest first.
func (a *App) FixMatches(ctx context.Context) ([]FixMatch, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT key, fix_a, fix_b, confidence, source, decided_at FROM fix_matches ORDER BY decided_at DESC, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	min := a.Cfg.MinConfidence()
	var out []FixMatch
	for rows.Next() {
		var m FixMatch
		if err := rows.Scan(&m.Key, &m.FixA, &m.FixB, &m.Confidence, &m.Source, &m.DecidedAt); err != nil {
			return nil, err
		}
		m.Matches = m.Confidence >= min
		out = append(out, m)
	}
	return out, rows.Err()
}

// groupQuickWins folds quick wins whose fixes are decided to be one page, at today's threshold,
// into one card each: the first in the ranking is kept, and the others go under its AlsoIn. With
// no decisions stored the list is returned as it came.
func (a *App) groupQuickWins(ctx context.Context, qw []QuickWin) []QuickWin {
	if len(qw) < 2 || a.DB == nil {
		return qw
	}
	matches, err := a.FixMatches(ctx)
	if err != nil {
		slog.Error("fix matches", "err", err)
		return qw
	}
	bySig := map[string][]int{}
	for i, q := range qw {
		s := FixSignature(q.Fix)
		bySig[s] = append(bySig[s], i)
	}
	parent := make([]int, len(qw))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	conf := map[[2]int]float64{}
	joined := false
	for _, m := range matches {
		if !m.Matches {
			continue
		}
		for _, i := range bySig[m.FixA] {
			for _, j := range bySig[m.FixB] {
				if qw[i].SetID == qw[j].SetID {
					continue
				}
				ri, rj := find(i), find(j)
				if ri != rj {
					if rj < ri {
						ri, rj = rj, ri
					}
					parent[rj] = ri
				}
				conf[[2]int{i, j}], conf[[2]int{j, i}] = m.Confidence, m.Confidence
				joined = true
			}
		}
	}
	if !joined {
		return qw
	}
	kept := map[int]int{} // a group's root, to its card's index in out
	out := make([]QuickWin, 0, len(qw))
	for i, q := range qw {
		r := find(i)
		k, ok := kept[r]
		if !ok {
			kept[r] = len(out)
			out = append(out, q)
			continue
		}
		// Unions keep the smaller root, so a group's root is its first card in ranking order: the
		// one kept, and the one this card's probability is against.
		out[k].AlsoIn = append(out[k].AlsoIn, AlsoIn{SetID: q.SetID, SetName: q.SetName, RunID: q.RunID, Rank: q.Rank, Title: q.Title, Confidence: conf[[2]int{r, i}]})
	}
	return out
}

// printFixMatches is the CLI's summary: the groups quick wins now show, each with the
// probability behind it, and the pairs decided below the threshold.
func printFixMatches(a *App, ctx context.Context, id string, n int) {
	fmt.Printf("decided %d new pairs of fixes\n", n)
	qw, err := a.QuickWins(ctx)
	if err != nil {
		fmt.Println("cheapseats:", err)
		return
	}
	groups := 0
	for _, q := range qw {
		if len(q.AlsoIn) == 0 {
			continue
		}
		groups++
		fmt.Printf("  %s  [%s]  %s\n", q.Title, q.SetName, q.Target)
		for _, x := range q.AlsoIn {
			p := "(through another fix)"
			if x.Confidence > 0 {
				p = fmt.Sprintf("%.2f", x.Confidence)
			}
			fmt.Printf("    also asked for by %s: %s  %s\n", x.SetName, x.Title, p)
		}
	}
	if groups == 0 {
		fmt.Printf("no two sets' fixes ask for the same page at %.2f\n", a.Cfg.MinConfidence())
	}
	matches, err := a.FixMatches(ctx)
	if err != nil {
		fmt.Println("cheapseats:", err)
		return
	}
	first := true
	for _, m := range matches {
		if m.Matches {
			continue
		}
		if first {
			fmt.Printf("below the threshold (%.2f):\n", a.Cfg.MinConfidence())
			first = false
		}
		fmt.Printf("  %s  +  %s  %.2f\n", signatureTitle(m.FixA), signatureTitle(m.FixB), m.Confidence)
	}
}

// signatureTitle is the title part of a FixSignature.
func signatureTitle(sig string) string {
	if i := strings.LastIndex(sig, "|"); i >= 0 {
		return sig[i+1:]
	}
	return sig
}
