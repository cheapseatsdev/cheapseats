package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// Folding product names (docs/optional-providers.md). The grader writes product names as the
// answers do, so one product can sit on the share-of-voice board twice ("Scrunch", "Scrunch AI").
// After a run, with [typesafe] configured, each new name is put to Jev once: which known product,
// if any, is this another name for? The decisions are stored (name_decisions) with Jev's
// probability for the match, and Aliases reads them back for every report at today's threshold,
// so no page ever calls Jev and switching it off leaves the board as it was.

func init() {
	RegisterPass(Pass{
		Name: "fold-names", Command: "fold", Order: 10, Provider: config.TypeSafeProvider,
		Usage: "put the run's new product names to Jev ([typesafe])",
		On:    (*config.Config).FoldNamesOn,
		Run:   (*App).FoldNames,
		Print: func(a *App, ctx context.Context, id string, n int) {
			fmt.Printf("asked Jev about %d new names\n", n)
			decisions, err := a.NameDecisions(ctx)
			if err != nil {
				fmt.Println("cheapseats:", err)
			}
			for _, d := range decisions {
				if d.Candidate != "" {
					fmt.Printf("  %-32s -> %-24s %.2f%s\n", d.Variant, d.Display, d.Confidence, map[bool]string{true: "", false: "  (below the threshold)"}[d.Folds])
				}
			}
		},
	})
}

// aliasCache is the folding map, rebuilt after a decision is written or the process starts.
type aliasCache struct {
	mu    sync.Mutex
	valid bool
	m     map[string]string
}

// Aliases is the board's folding map: a lower-cased name the grader wrote, to the name it is
// displayed as. It holds the configured competitors' aliases and the stored Jev decisions at or
// above the configured confidence.
func (a *App) Aliases() map[string]string {
	a.aliases.mu.Lock()
	defer a.aliases.mu.Unlock()
	if a.aliases.valid {
		return a.aliases.m
	}
	m := map[string]string{}
	for _, c := range a.Cfg.Competitors {
		for _, al := range c.Aliases {
			if k := strings.ToLower(strings.TrimSpace(al)); k != "" && !strings.EqualFold(al, c.Name) {
				m[k] = c.Name
			}
		}
	}
	var rows *sql.Rows
	err := sql.ErrConnDone
	if a.DB != nil {
		rows, err = a.DB.Query(`SELECT variant, display, confidence FROM name_decisions WHERE candidate != '' ORDER BY variant, confidence DESC`)
	}
	if err == nil {
		min := a.Cfg.FoldConfidence()
		seen := map[string]bool{}
		for rows.Next() {
			var v, d string
			var conf float64
			if rows.Scan(&v, &d, &conf) != nil {
				continue
			}
			if seen[v] || conf < min || strings.EqualFold(v, d) {
				continue
			}
			seen[v] = true
			if _, fixed := m[v]; !fixed {
				m[v] = d
			}
		}
		rows.Close()
	} else if a.DB != nil {
		slog.Error("aliases", "err", err)
	}
	// One hop: a variant folded onto a name that is itself folded.
	for v, d := range m {
		if d2, ok := m[strings.ToLower(d)]; ok && !strings.EqualFold(d2, v) {
			m[v] = d2
		}
	}
	a.aliases.m, a.aliases.valid = m, true
	return m
}

func (a *App) invalidateAliases() {
	a.aliases.mu.Lock()
	a.aliases.valid = false
	a.aliases.mu.Unlock()
}

// TypeSafe is the Jev client, budgeted and priced through the config.
func (a *App) TypeSafe() *typesafe.Client {
	return &typesafe.Client{Model: a.Cfg.TypeSafeModel(), Budget: a.Runs.Budget, Cfg: a.Cfg}
}

// candidate is a known product a new name might be another name for.
type candidate struct {
	key, display, context string
}

// FoldNames puts the run's new product names to Jev and stores its decisions. It returns how
// many names were asked about. Names already decided, and names with no plausible candidate,
// are skipped, so re-running it costs nothing.
func (a *App) FoldNames(ctx context.Context, id string) (int, error) {
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
	isBrand := func(n string) bool {
		if strings.EqualFold(n, brand.Name) {
			return true
		}
		for _, al := range brand.Aliases {
			if strings.EqualFold(n, al) {
				return true
			}
		}
		return false
	}
	// Known products, in the order they're offered: the brand, the competitors, then names
	// already decided to be products of their own or folded onto.
	var known []candidate
	knownKey := map[string]bool{}
	add := func(display, context string) {
		k := strings.ToLower(strings.TrimSpace(display))
		if k == "" || knownKey[k] {
			return
		}
		knownKey[k] = true
		known = append(known, candidate{key: k, display: strings.TrimSpace(display), context: context})
	}
	add(brand.Name, categoryOf(brand))
	for _, c := range a.RunCompetitors(r) {
		add(c.Name, "")
		for _, al := range c.Aliases {
			knownKey[strings.ToLower(strings.TrimSpace(al))] = true
		}
	}
	decided := map[string]bool{}
	rows, err := a.DB.QueryContext(ctx, `SELECT variant, candidate, display FROM name_decisions`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var v, c, d string
		if err := rows.Scan(&v, &c, &d); err != nil {
			rows.Close()
			return 0, err
		}
		decided[v] = true
		if c == "" {
			add(v, "")
		} else {
			add(d, "")
		}
	}
	rows.Close()

	// The run's names, each with the sentence of an answer that names it, as context, and how many
	// answers named it.
	context := map[string]string{}
	display := map[string]string{}
	count := map[string]int{}
	var order []string
	for _, x := range results {
		if x.Grade == nil || x.Answer == nil {
			continue
		}
		names := append([]string(nil), x.Grade.Named...)
		if x.Grade.TopPick != nil {
			names = append(names, *x.Grade.TopPick)
		}
		for _, n := range names {
			n = strings.TrimSpace(n)
			k := strings.ToLower(n)
			if k == "" || isBrand(n) {
				continue
			}
			if _, ok := display[k]; !ok {
				display[k] = n
				order = append(order, k)
			}
			count[k]++
			if context[k] == "" {
				context[k] = sentenceWith(x.Answer.Text, n)
			}
		}
	}
	sort.Strings(order)
	// Every name on this run's board is a candidate for the others, so two unconfigured spellings
	// of one product can meet. Between two such names, only the less established one is asked
	// about: the one named in fewer answers, or, at a tie, the shorter, so "Scrunch" is asked
	// about "Scrunch AI" and not the other way round.
	boardOnly := map[string]bool{}
	for _, k := range order {
		if !knownKey[k] {
			known = append(known, candidate{key: k, display: display[k], context: context[k]})
			boardOnly[k] = true
		}
	}
	moreEstablished := func(c, v string) bool {
		if count[c] != count[v] {
			return count[c] > count[v]
		}
		return len(c) > len(v)
	}
	type ask struct {
		variant    string
		candidates []candidate
	}
	var asks []ask
	for _, k := range order {
		if decided[k] || isKnownProduct(k, a.Cfg) {
			continue
		}
		var cs []candidate
		for _, c := range known {
			if c.key == k || !plausible(k, c.key) {
				continue
			}
			if boardOnly[c.key] && !moreEstablished(c.key, k) {
				continue
			}
			cs = append(cs, c)
		}
		if len(cs) == 0 {
			continue
		}
		if len(cs) > 8 {
			cs = cs[:8]
		}
		asks = append(asks, ask{variant: k, candidates: cs})
	}
	if len(asks) == 0 {
		return 0, nil
	}

	client := a.TypeSafe()
	asked := 0
	// One name per request. Jev's accuracy falls as the state fills with detail unrelated to the
	// question, and in production eight names in one request scored the same pairs 0.15 to 0.3
	// lower than one at a time (Otterly AI vs Otterly.AI: 0.59 against 0.88). A request is a
	// fraction of a cent, so the smaller state is worth it.
	const batch = 1
	for i := 0; i < len(asks); i += batch {
		part := asks[i:min(i+batch, len(asks))]
		state := map[string]any{"category": categoryOf(brand), "names": []any{}}
		questions := map[string]typesafe.Question{}
		var namesState []any
		for j, q := range part {
			var cands []any
			criteria := map[string]any{}
			for ci, c := range q.candidates {
				cands = append(cands, map[string]any{"name": c.display, "context": c.context})
				criteria[fmt.Sprintf("c%d", ci)] = fmt.Sprintf("the same product as `names[%d].candidates[%d].name` (%s), under another name or spelling", j, ci, c.display)
			}
			criteria["none"] = "a different product from every candidate, or not enough information to tell"
			namesState = append(namesState, map[string]any{"name": display[q.variant], "context": context[q.variant], "candidates": cands})
			questions[fmt.Sprintf("q%d", j)] = typesafe.Question{
				Type:         "choice",
				Instructions: fmt.Sprintf("`names[%d].name` is a product named in an AI assistant's answer about `category`; `names[%d].context` is the sentence that named it. Which of `names[%d].candidates`, if any, is the same product under another name? Treat a name that differs only in spacing, punctuation, capitalisation, a domain suffix like .ai or .com, a company prefix, or a suffix like AI as the same product. A product with a similar name but a different maker or purpose is a different product.", j, j, j),
				Criteria:     criteria,
			}
		}
		state["names"] = namesState
		resp, err := client.Ask(ctx, "fold-names/"+id, state, questions)
		if err != nil {
			return asked, err
		}
		now := store.Now()
		for j, q := range part {
			ans, ok := resp.Answers[fmt.Sprintf("q%d", j)]
			if !ok {
				continue
			}
			cand, disp := "", ""
			if ans.Choice != "none" {
				var ci int
				if _, err := fmt.Sscanf(ans.Choice, "c%d", &ci); err == nil && ci >= 0 && ci < len(q.candidates) {
					cand, disp = q.candidates[ci].key, q.candidates[ci].display
				}
			}
			// What's stored is Jev's probability for the option it chose: for a match, how likely
			// the two names are one product. (Its confidence statistic measures how concentrated
			// the whole distribution is and shrinks with every extra candidate, so a 0.77 on a
			// two-option question reads 0.54; it isn't the quantity to gate on.)
			p, ok := ans.Probabilities[ans.Choice]
			if !ok {
				p = ans.Confidence
			}
			if _, err := a.DB.ExecContext(ctx, `INSERT OR REPLACE INTO name_decisions(variant, candidate, display, confidence, source, decided_at) VALUES (?,?,?,?,'jev',?)`,
				q.variant, cand, disp, p, now); err != nil {
				return asked, err
			}
			asked++
		}
		slog.Info("fold names", "run", id, "asked", len(part), "model", resp.Model, "cost_usd", fmt.Sprintf("%.4f", resp.CostUSD))
	}
	a.invalidateAliases()
	return asked, nil
}

// isKnownProduct reports whether a lower-cased name is a configured competitor or alias, which
// the grader already folds and no decision is needed for.
func isKnownProduct(k string, cfg *config.Config) bool {
	for _, c := range cfg.Competitors {
		if strings.EqualFold(k, c.Name) {
			return true
		}
		for _, al := range c.Aliases {
			if strings.EqualFold(k, al) {
				return true
			}
		}
	}
	return false
}

var stopTokens = map[string]bool{"ai": true, "the": true, "app": true, "tool": true, "tools": true, "platform": true, "io": true, "com": true, "inc": true, "labs": true, "hq": true, "search": true, "seo": true}

// plausible is the cheap pre-filter that decides which pairs are worth a question: the names
// share a meaningful word, or one contains the other.
func plausible(a, b string) bool {
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return true
	}
	ta, tb := tokens(a), tokens(b)
	for t := range ta {
		if tb[t] {
			return true
		}
	}
	return false
}

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(t) >= 3 && !stopTokens[t] {
			out[t] = true
		}
	}
	return out
}

// sentenceWith is the sentence of text that names n, trimmed, or "".
func sentenceWith(text, n string) string {
	i := strings.Index(strings.ToLower(text), strings.ToLower(n))
	if i < 0 {
		return ""
	}
	start := strings.LastIndexAny(text[:i], ".!?\n") + 1
	end := strings.IndexAny(text[i:], ".!?\n")
	if end < 0 {
		end = len(text)
	} else {
		end += i + 1
	}
	s := strings.TrimSpace(text[start:end])
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// categoryOf is the category a pass puts in state: brand.category when set, else the first
// sentence of brand.about (a product description, which is close enough for context).
func categoryOf(b config.Brand) string {
	if b.Category != "" {
		return b.Category
	}
	return firstSentence(b.About)
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".!?\n"); i > 0 && i < len(s)-1 {
		return s[:i+1]
	}
	return s
}

// NameDecisions lists the stored decisions, newest first, for the UI and the CLI.
func (a *App) NameDecisions(ctx context.Context) ([]NameDecision, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT variant, candidate, display, confidence, source, decided_at FROM name_decisions ORDER BY decided_at DESC, variant`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NameDecision
	for rows.Next() {
		var d NameDecision
		var cand sql.NullString
		if err := rows.Scan(&d.Variant, &cand, &d.Display, &d.Confidence, &d.Source, &d.DecidedAt); err != nil {
			return nil, err
		}
		d.Candidate = cand.String
		d.Folds = d.Candidate != "" && d.Confidence >= a.Cfg.FoldConfidence()
		out = append(out, d)
	}
	return out, rows.Err()
}

type NameDecision struct {
	Variant    string  `json:"variant"`
	Candidate  string  `json:"candidate,omitempty"`
	Display    string  `json:"display,omitempty"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
	DecidedAt  string  `json:"decided_at"`
	// Whether the decision folds the variant today, at the configured confidence.
	Folds bool `json:"folds"`
}
