package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/scaffold"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// Competitor discovery (docs/optional-providers.md). Competitors are configured by hand, and
// the prompt review's gaps only know names the answers already used. On demand, never after a
// run, Exa is asked what resembles each configured competitor's home page and the brand's own,
// the results are gathered by host and cross-referenced with the share-of-voice board, and, with
// [typesafe] on, Jev says whether each host is a product in the category. What comes out is a
// list of suggestions for [[competitors]] in competitor_suggestions, printed as TOML to paste.
// Nothing here edits the config, and the API and MCP read the table; only the CLI and the
// suggest_competitors tool run the discovery.

// discoverOp is the ledger name: spend events are provider-attempt/discover.
const discoverOp = "discover"

// Suggestion is one host worth considering as a competitor.
type Suggestion struct {
	Host string `json:"host"`
	// A cleaned name from the page title, or the host's label.
	Name string `json:"name"`
	// The best page Exa returned on the host (the shortest path, usually the home page).
	URL string `json:"url"`
	// The configured names (competitors, or the brand) whose home pages it was similar to.
	Seeds []string `json:"seeds"`
	// Answers naming it on the latest run of every set, when it isn't configured; 0 otherwise.
	OnBoard int `json:"on_board"`
	// Jev's probability that it's a product in the category; nil when [typesafe] is off.
	InCategory *float64 `json:"in_category,omitempty"`
	FoundAt    string   `json:"found_at"`
	// The reason, in a sentence: "similar to 2 competitors' sites, named in 7 answers".
	Why string `json:"why"`
}

// Discovered is what one run of the discovery did.
type Discovered struct {
	Seeds   int     `json:"seeds"`    // home pages asked about
	Hosts   int     `json:"hosts"`    // distinct hosts found
	Asked   int     `json:"asked"`    // hosts put to Jev
	CostUSD float64 `json:"cost_usd"` // Exa and Jev together
}

// seed is a home page to find similar pages to: a configured competitor's, or the brand's.
type seed struct {
	label string // the competitor's name, or the brand's
	url   string
	brand bool
}

// DiscoverCompetitors asks Exa what resembles each configured competitor's first domain and the
// brand's first site, gathers the results by host, marks the hosts the share-of-voice board
// already names, and, with [typesafe] on and callable, asks Jev once per new host whether it's a
// product in the category. The suggestions are stored; a host already judged isn't put to Jev
// again, and a dismissed host stays dismissed. Hosts Exa no longer returns are dropped unless
// dismissed.
func (a *App) DiscoverCompetitors(ctx context.Context) (Discovered, error) {
	var d Discovered
	if a.Cfg.Exa == nil {
		return d, fmt.Errorf("no [exa] in the config")
	}
	if m := config.Missing(config.ExaProvider); m != "" {
		return d, fmt.Errorf("exa: %s", m)
	}
	seeds, exclude := a.discoverSeeds()
	if len(seeds) == 0 {
		return d, fmt.Errorf("the brand has no sites and no competitor has domains: nothing to find similar pages to")
	}
	configured := a.configuredHosts()
	client := a.Exa()
	type found struct {
		title, url string
		seeds      []string
	}
	byHost := map[string]*found{}
	var hosts []string
	for _, s := range seeds {
		if ctx.Err() != nil {
			return d, ctx.Err()
		}
		resp, err := client.FindSimilar(ctx, discoverOp, s.url, exclude, a.Cfg.DiscoverResults())
		if err != nil {
			return d, fmt.Errorf("find similar to %s: %w", s.url, err)
		}
		d.Seeds++
		d.CostUSD += resp.CostUSD
		for _, r := range resp.Results {
			h := hostOf(r.URL)
			if h == "" || onConfiguredHost(h, configured) {
				continue
			}
			f, ok := byHost[h]
			if !ok {
				f = &found{title: scaffold.Clean(r.Title), url: r.URL}
				byHost[h] = f
				hosts = append(hosts, h)
			} else if shorterPath(r.URL, f.url) {
				f.url = r.URL
				if t := scaffold.Clean(r.Title); t != "" {
					f.title = t
				}
			}
			if !contains(f.seeds, s.label) {
				f.seeds = append(f.seeds, s.label)
			}
		}
	}
	d.Hosts = len(hosts)
	board := a.boardNames(ctx)

	// What's already judged, so Jev is asked once per host ever.
	judged := map[string]bool{}
	rows, err := a.DB.QueryContext(ctx, `SELECT host FROM competitor_suggestions WHERE in_category IS NOT NULL`)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil {
			judged[h] = true
		}
	}
	rows.Close()
	askJev := a.Cfg.TypeSafe != nil && config.Missing(config.TypeSafeProvider) == ""
	category := firstSentence(a.Cfg.Brand.About)
	now := store.Now()
	for _, h := range hosts {
		f := byHost[h]
		label := hostLabel(h)
		name := scaffold.TitleName(f.title, label)
		if name == "" {
			name = titleWords(label)
		}
		onBoard := 0
		for _, b := range board {
			if sameName(name, b.name) || sameName(label, b.name) || sameName(h, b.name) {
				onBoard += b.answers
			}
		}
		var inCategory sql.NullFloat64
		if askJev && !judged[h] {
			p, cost, err := a.judgeHost(ctx, f.url, f.title, category)
			if err != nil {
				return d, err
			}
			inCategory = sql.NullFloat64{Float64: p, Valid: true}
			d.Asked++
			d.CostUSD += cost
		}
		js, _ := json.Marshal(f.seeds)
		if _, err := a.DB.ExecContext(ctx, `INSERT INTO competitor_suggestions(host, name, url, seeds, on_board, in_category, dismissed, found_at) VALUES (?,?,?,?,?,?,0,?)
			ON CONFLICT(host) DO UPDATE SET name=excluded.name, url=excluded.url, seeds=excluded.seeds, on_board=excluded.on_board,
			in_category=COALESCE(excluded.in_category, competitor_suggestions.in_category), found_at=excluded.found_at`,
			h, name, f.url, string(js), onBoard, inCategory, now); err != nil {
			return d, err
		}
	}
	// A host Exa stopped returning is no longer a suggestion; a dismissed one stays dismissed in
	// case it comes back.
	if _, err := a.DB.ExecContext(ctx, `DELETE FROM competitor_suggestions WHERE dismissed=0 AND found_at != ?`, now); err != nil {
		return d, err
	}
	slog.Info("discover competitors", "seeds", d.Seeds, "hosts", d.Hosts, "asked", d.Asked, "cost_usd", fmt.Sprintf("%.4f", d.CostUSD))
	return d, nil
}

// judgeHost puts one host to Jev: is the page a product or service that competes in the category?
// It returns the probability of yes and what the call cost.
func (a *App) judgeHost(ctx context.Context, pageURL, title, category string) (float64, float64, error) {
	state := map[string]any{"url": pageURL, "title": title, "category": category}
	q := map[string]typesafe.Question{"competes": {
		Type:         "noul",
		Instructions: "`url` is a web page titled `title`, found because it resembles the home page of a product in `category`. It is the website of a product or service that competes in `category`: something a buyer could choose instead of one of those products. A product in the category counts whatever its size. A blog, a news article, a directory or marketplace listing, an agency, a review site, or a product for a different job does not.",
	}}
	resp, err := a.TypeSafe().Ask(ctx, discoverOp, state, q)
	if err != nil {
		return 0, 0, err
	}
	ans, ok := resp.Answers["competes"]
	if !ok {
		return 0, resp.CostUSD, fmt.Errorf("typesafe: no answer for %s", pageURL)
	}
	return typesafe.Chosen(ans), resp.CostUSD, nil
}

// discoverSeeds is the home pages to start from (each configured competitor's first domain, then
// the brand's first site) and every configured host, which the results must leave out.
func (a *App) discoverSeeds() ([]seed, []string) {
	var seeds []seed
	var exclude []string
	addExclude := func(site string) {
		if h := hostOf(site); h != "" && !contains(exclude, h) {
			exclude = append(exclude, h)
		}
	}
	for _, c := range a.allCompetitors() {
		for i, dom := range c.Domains {
			if i == 0 {
				seeds = append(seeds, seed{label: c.Name, url: homePage(dom)})
			}
			addExclude(dom)
		}
	}
	for i, s := range a.Cfg.Brand.AllSites() {
		if i == 0 {
			seeds = append(seeds, seed{label: a.Cfg.Brand.Name, url: homePage(s.URL), brand: true})
		}
		addExclude(s.URL)
	}
	return seeds, exclude
}

// allCompetitors is every configured competitor: the shared ones, then each set's own, once each
// by name.
func (a *App) allCompetitors() []sets.Competitor {
	var out []sets.Competitor
	seen := map[string]bool{}
	add := func(c sets.Competitor) {
		k := strings.ToLower(strings.TrimSpace(c.Name))
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, c)
	}
	for _, c := range a.Cfg.Competitors {
		add(c)
	}
	for _, s := range a.Sets() {
		for _, c := range s.Competitors {
			add(c)
		}
	}
	return out
}

// configuredHosts is every host the brand's sites and the competitors' domains name.
func (a *App) configuredHosts() []string {
	var out []string
	for _, s := range a.Cfg.Brand.AllSites() {
		if h := hostOf(s.URL); h != "" {
			out = append(out, h)
		}
	}
	for _, c := range a.allCompetitors() {
		for _, d := range c.Domains {
			if h := hostOf(d); h != "" {
				out = append(out, h)
			}
		}
	}
	return out
}

// boardName is an unconfigured product the latest runs name, and in how many answers.
type boardName struct {
	name    string
	answers int
}

// boardNames is every product on the share-of-voice board of each set's latest finished run that
// isn't the brand or a configured competitor, with the answers naming it summed over the sets.
func (a *App) boardNames(ctx context.Context) []boardName {
	count := map[string]int{}
	var order []string
	for _, s := range a.Sets() {
		var id string
		if err := a.DB.QueryRowContext(ctx, `SELECT id FROM runs WHERE set_id=? AND status='done' ORDER BY created_at DESC LIMIT 1`, s.ID).Scan(&id); err != nil {
			continue
		}
		r, err := a.Runs.Get(ctx, id)
		if err != nil {
			continue
		}
		results, err := a.Runs.Results(ctx, id)
		if err != nil {
			continue
		}
		brand := a.RunBrand(r)
		known := map[string]bool{strings.ToLower(brand.Name): true}
		for _, al := range brand.Aliases {
			known[strings.ToLower(al)] = true
		}
		for _, c := range a.RunCompetitors(r) {
			known[strings.ToLower(c.Name)] = true
			for _, al := range c.Aliases {
				known[strings.ToLower(al)] = true
			}
		}
		for _, v := range a.Summary(r, results).Voice {
			k := strings.ToLower(strings.TrimSpace(v.Name))
			if k == "" || known[k] || isKnownProduct(k, a.Cfg) {
				continue
			}
			if _, ok := count[k]; !ok {
				order = append(order, k)
			}
			count[k] += v.Answers
		}
	}
	out := make([]boardName, 0, len(order))
	for _, k := range order {
		out = append(out, boardName{name: k, answers: count[k]})
	}
	return out
}

// CompetitorSuggestions lists the suggestions worth showing: not dismissed, not since configured,
// and, where Jev judged the host, at or above the configured confidence that it's a product in
// the category. Ordered by how many seeds led there, then answers on the board, then Jev's
// probability.
func (a *App) CompetitorSuggestions(ctx context.Context) ([]Suggestion, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT host, name, url, seeds, on_board, in_category, found_at FROM competitor_suggestions WHERE dismissed=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	threshold := a.Cfg.MinConfidence()
	configured := a.configuredHosts()
	out := []Suggestion{}
	for rows.Next() {
		var s Suggestion
		var js string
		var p sql.NullFloat64
		if err := rows.Scan(&s.Host, &s.Name, &s.URL, &js, &s.OnBoard, &p, &s.FoundAt); err != nil {
			return nil, err
		}
		if p.Valid {
			if p.Float64 < threshold {
				continue
			}
			v := p.Float64
			s.InCategory = &v
		}
		if onConfiguredHost(s.Host, configured) || isKnownProduct(strings.ToLower(s.Name), a.Cfg) {
			continue
		}
		json.Unmarshal([]byte(js), &s.Seeds)
		if s.Seeds == nil {
			s.Seeds = []string{}
		}
		s.Why = a.why(s)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if len(x.Seeds) != len(y.Seeds) {
			return len(x.Seeds) > len(y.Seeds)
		}
		if x.OnBoard != y.OnBoard {
			return x.OnBoard > y.OnBoard
		}
		px, py := -1.0, -1.0
		if x.InCategory != nil {
			px = *x.InCategory
		}
		if y.InCategory != nil {
			py = *y.InCategory
		}
		if px != py {
			return px > py
		}
		return x.Host < y.Host
	})
	return out, nil
}

// why is a suggestion's reason in one clause: "similar to 2 competitors' sites and the brand's,
// named in 7 answers, in the category at 0.90".
func (a *App) why(s Suggestion) string {
	brand := false
	var rivals []string
	for _, l := range s.Seeds {
		if strings.EqualFold(l, a.Cfg.Brand.Name) {
			brand = true
		} else {
			rivals = append(rivals, l)
		}
	}
	var sim string
	switch {
	case len(rivals) == 1:
		sim = "similar to " + possessive(rivals[0]) + " site"
	case len(rivals) > 1:
		sim = fmt.Sprintf("similar to %d competitors' sites", len(rivals))
	}
	if brand {
		if sim == "" {
			sim = "similar to " + possessive(a.Cfg.Brand.Name) + " site"
		} else {
			sim += " and " + possessive(a.Cfg.Brand.Name)
		}
	}
	parts := []string{sim}
	if s.OnBoard > 0 {
		parts = append(parts, "named in "+plural(s.OnBoard, "answer"))
	}
	if s.InCategory != nil {
		parts = append(parts, fmt.Sprintf("in the category at %.2f", *s.InCategory))
	}
	return strings.Join(parts, ", ")
}

// DismissSuggestion hides a host from the suggestions, through every later refresh.
func (a *App) DismissSuggestion(ctx context.Context, host string) error {
	host = hostOf(host)
	res, err := a.DB.ExecContext(ctx, `UPDATE competitor_suggestions SET dismissed=1 WHERE host=?`, host)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no suggestion for %q", host)
	}
	return nil
}

// SuggestionsTOML is the suggestions as [[competitors]] entries to paste into cheapseats.toml,
// each with its reason as a comment. It parses as TOML; nothing writes it to the config.
func SuggestionsTOML(ss []Suggestion) string {
	var b strings.Builder
	for i, s := range ss {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[[competitors]]\nname = %s\ndomains = [%s]   # %s\n", tomlString(s.Name), tomlString(s.Host), scaffold.Clean(s.Why))
	}
	return b.String()
}

// tomlString is a TOML basic string: Clean has already dropped control characters.
func tomlString(s string) string {
	s = scaffold.Clean(s)
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// homePage is the URL of a configured site or domain's home page: https:// and the host (without
// www., as the config reads sites), or the host/path prefix as given.
func homePage(site string) string {
	s := strings.ToLower(strings.TrimSpace(site))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	return "https://" + strings.TrimSuffix(strings.TrimPrefix(s, "www."), "/")
}

// hostOf is a URL's or a site's host, lower-cased, without www. or a port; "" when there is none.
func hostOf(u string) string {
	s := strings.TrimSpace(u)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	p, err := url.Parse(s)
	if err != nil {
		return ""
	}
	h := strings.ToLower(p.Hostname())
	return strings.TrimPrefix(h, "www.")
}

// onConfiguredHost reports whether a host is one of the configured hosts or a subdomain of one.
func onConfiguredHost(h string, configured []string) bool {
	for _, c := range configured {
		if h == c || strings.HasSuffix(h, "."+c) {
			return true
		}
	}
	return false
}

// shorterPath reports whether a's path is shorter than b's: the better pick for a host's page.
func shorterPath(a, b string) bool {
	pa, erra := url.Parse(a)
	pb, errb := url.Parse(b)
	if erra != nil || errb != nil {
		return false
	}
	return len(strings.Trim(pa.Path, "/"))+len(pa.RawQuery) < len(strings.Trim(pb.Path, "/"))+len(pb.RawQuery)
}

// hostLabel is the host's name without its suffix: "acme" for acme.co.uk, "docs.acme" stays
// "acme" since the first label of a subdomain rarely names the product.
func hostLabel(h string) string {
	parts := strings.Split(h, ".")
	if len(parts) >= 3 && len(parts[len(parts)-2]) <= 3 && len(parts[len(parts)-1]) <= 3 {
		parts = parts[:len(parts)-2] // acme.co.uk
	} else if len(parts) >= 2 {
		parts = parts[:len(parts)-1]
	}
	return parts[len(parts)-1]
}

// titleWords is a label as a name: "rankprompt" stays as is but capitalised, "rank-prompt"
// becomes "Rank Prompt".
func titleWords(label string) string {
	words := strings.FieldsFunc(label, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		rs := []rune(w)
		rs[0] = unicode.ToUpper(rs[0])
		words[i] = string(rs)
	}
	return strings.Join(words, " ")
}

// nameKey is a name's letters and digits, lowercased, with a trailing "ai" dropped so "Peec" and
// "Peec AI" meet.
func nameKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	k := b.String()
	if len(k) > 4 && strings.HasSuffix(k, "ai") {
		k = strings.TrimSuffix(k, "ai")
	}
	return k
}

// sameName reports whether two names are one product by spelling alone: equal keys, or one is the
// other with a domain suffix (host "peec.ai" against the board's "peec").
func sameName(a, b string) bool {
	ka, kb := nameKey(a), nameKey(b)
	if ka == "" || kb == "" {
		return false
	}
	if ka == kb {
		return true
	}
	if strings.Contains(a, ".") && nameKey(hostLabel(hostOf(a))) == kb {
		return true
	}
	return false
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
