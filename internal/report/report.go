// Package report turns a run's graded results into rates, share of voice and the pages that
// shaped the answers.
package report

import (
	"math"
	"sort"
	"strings"

	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/grade"
	"github.com/metcalfc/popcorn/internal/run"
)

// Rate is a proportion with a 95% Wilson interval, so a small sample reads as uncertain.
type Rate struct {
	Hits int     `json:"hits"`
	N    int     `json:"n"`
	P    float64 `json:"p"`
	Lo   float64 `json:"lo"`
	Hi   float64 `json:"hi"`
}

func NewRate(hits, n int) Rate {
	r := Rate{Hits: hits, N: n}
	if n == 0 {
		return r
	}
	const z = 1.96
	p := float64(hits) / float64(n)
	nf := float64(n)
	den := 1 + z*z/nf
	mid := (p + z*z/(2*nf)) / den
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / den
	r.P, r.Lo, r.Hi = p, math.Max(0, mid-half), math.Min(1, mid+half)
	return r
}

type Rates struct {
	TopPick     Rate `json:"top_pick"`
	Recommended Rate `json:"recommended"`
	Visible     Rate `json:"visible"` // recommended or mentioned
}

type Slice struct {
	Answerer string       `json:"answerer"`
	Track    answer.Track `json:"track"`
	Rates
	Errors int `json:"errors"`
}

type Voice struct {
	Name    string  `json:"name"`
	Answers int     `json:"answers"` // answers that named it
	Share   float64 `json:"share"`   // of all answers graded
	TopPick int     `json:"top_pick"`
	IsBrand bool    `json:"is_brand"`
}

type Page struct {
	URL        string `json:"url"`
	Host       string `json:"host"`
	FirstParty bool   `json:"first_party"`
	// The brand's site it's on, and that site's kind, when FirstParty.
	Site     string `json:"site,omitempty"`
	SiteKind string `json:"site_kind,omitempty"`
	Cited    int    `json:"cited"`    // answers that linked it
	Surfaced int    `json:"surfaced"` // answers whose searches returned it
	Prompts  []int  `json:"prompts"`
}

// SiteUse is how much answers drew on one of the brand's sites, so a site the answers never
// reach shows up as zeros rather than not at all.
type SiteUse struct {
	URL      string `json:"url"`
	Kind     string `json:"kind"`
	Cited    int    `json:"cited"`    // answers that linked a page on it
	Surfaced int    `json:"surfaced"` // answers whose searches returned a page on it
	Pages    int    `json:"pages"`    // distinct pages of it seen
	Prompts  int    `json:"prompts"`  // prompts where it surfaced or was cited
}

type PromptRow struct {
	Idx       int     `json:"idx"`
	Prompt    string  `json:"prompt"`
	Rates     Rates   `json:"rates"`
	TopPicks  []Count `json:"top_picks"`
	FirstHand bool    `json:"first_party_surfaced"` // any search surfaced a first-party page
}

type Count struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

type Summary struct {
	Overall  Rates       `json:"overall"`
	Slices   []Slice     `json:"slices"`
	Voice    []Voice     `json:"voice"`
	Pages    []Page      `json:"pages"`
	Sites    []SiteUse   `json:"sites"`
	Prompts  []PromptRow `json:"prompts"`
	Done     int         `json:"done"`
	Errors   int         `json:"errors"`
	Pending  int         `json:"pending"`
	CostUSD  float64     `json:"cost_usd"`
	Searches int         `json:"searches"`
}

type tally struct{ top, rec, vis, n int }

func (t *tally) add(g *grade.Grade, brand string) {
	t.n++
	if g.IsTopPick(brand) {
		t.top++
	}
	if g.Verdict == grade.Recommended {
		t.rec++
	}
	if g.Verdict != grade.Absent {
		t.vis++
	}
}

func (t tally) rates() Rates {
	return Rates{TopPick: NewRate(t.top, t.n), Recommended: NewRate(t.rec, t.n), Visible: NewRate(t.vis, t.n)}
}

func Build(brand config.Brand, results []run.Result) Summary {
	var s Summary
	var all tally
	slices := map[[2]string]*tally{}
	sliceErr := map[[2]string]int{}
	var sliceOrder [][2]string
	prompts := map[int]*tally{}
	promptText := map[int]string{}
	promptTop := map[int]map[string]int{}
	promptFirst := map[int]bool{}
	voice := map[string]*Voice{}
	pages := map[string]*Page{}
	// Every site, in config order, used or not.
	sites := map[string]*SiteUse{}
	sitePrompts := map[string]map[int]bool{}
	for _, st := range brand.AllSites() {
		if _, dup := sites[st.URL]; !dup {
			sites[st.URL] = &SiteUse{URL: st.URL, Kind: st.Kind}
			sitePrompts[st.URL] = map[int]bool{}
		}
	}
	siteUse := func(url string) *SiteUse { return sites[url] }

	for _, r := range results {
		s.CostUSD += r.CostUSD
		key := [2]string{r.Answerer, string(r.Track)}
		if _, ok := slices[key]; !ok {
			slices[key] = &tally{}
			sliceOrder = append(sliceOrder, key)
		}
		promptText[r.PromptIdx] = r.Prompt
		switch r.Status {
		case "done":
		case "error":
			s.Errors++
			sliceErr[key]++
			continue
		default:
			s.Pending++
			continue
		}
		if r.Grade == nil {
			continue
		}
		s.Done++
		all.add(r.Grade, brand.Name)
		slices[key].add(r.Grade, brand.Name)
		if prompts[r.PromptIdx] == nil {
			prompts[r.PromptIdx] = &tally{}
			promptTop[r.PromptIdx] = map[string]int{}
		}
		prompts[r.PromptIdx].add(r.Grade, brand.Name)
		if r.Grade.TopPick != nil {
			promptTop[r.PromptIdx][*r.Grade.TopPick]++
		}
		// The brand counts as named exactly when its verdict says so, so its share of voice and its
		// named rate always agree, whatever the grader's list of names says.
		names := []string{}
		for _, n := range r.Grade.Named {
			if !isBrand(brand, n) {
				names = append(names, n)
			}
		}
		if r.Grade.Verdict != grade.Absent {
			names = append(names, brand.Name)
		}
		seen := map[string]bool{}
		for _, n := range names {
			k := strings.ToLower(n)
			if seen[k] {
				continue
			}
			seen[k] = true
			v := voice[k]
			if v == nil {
				v = &Voice{Name: n, IsBrand: strings.EqualFold(n, brand.Name)}
				voice[k] = v
			}
			v.Answers++
			if r.Grade.TopPick != nil && strings.EqualFold(*r.Grade.TopPick, n) {
				v.TopPick++
			}
		}
		if r.Answer == nil {
			continue
		}
		s.Searches += len(r.Answer.Searches)
		touch := func(u string) *Page {
			u = NormalizeURL(u)
			p := pages[u]
			if p == nil {
				p = &Page{URL: u, Host: Host(u)}
				if site, ok := brand.SiteOf(u); ok {
					p.FirstParty, p.Site, p.SiteKind = true, site.URL, site.Kind
				}
				pages[u] = p
			}
			if !containsInt(p.Prompts, r.PromptIdx) {
				p.Prompts = append(p.Prompts, r.PromptIdx)
			}
			return p
		}
		cited := map[string]bool{}
		citedSite := map[string]bool{}
		for _, c := range r.Answer.Citations {
			p := touch(c.URL)
			if !cited[p.URL] {
				cited[p.URL] = true
				p.Cited++
			}
			if p.Site != "" && !citedSite[p.Site] {
				citedSite[p.Site] = true
				siteUse(p.Site).Cited++
				sitePrompts[p.Site][r.PromptIdx] = true
			}
		}
		surfaced := map[string]bool{}
		surfacedSite := map[string]bool{}
		for _, q := range r.Answer.Searches {
			for _, l := range q.Results {
				p := touch(l.URL)
				if !surfaced[p.URL] {
					surfaced[p.URL] = true
					p.Surfaced++
				}
				if p.Site != "" && !surfacedSite[p.Site] {
					surfacedSite[p.Site] = true
					siteUse(p.Site).Surfaced++
					sitePrompts[p.Site][r.PromptIdx] = true
				}
				if p.FirstParty {
					promptFirst[r.PromptIdx] = true
				}
			}
		}
	}

	s.Overall = all.rates()
	for _, k := range sliceOrder {
		s.Slices = append(s.Slices, Slice{Answerer: k[0], Track: answer.Track(k[1]), Rates: slices[k].rates(), Errors: sliceErr[k]})
	}
	for _, v := range voice {
		if s.Done > 0 {
			v.Share = float64(v.Answers) / float64(s.Done)
		}
		s.Voice = append(s.Voice, *v)
	}
	sort.Slice(s.Voice, func(i, j int) bool {
		if s.Voice[i].Answers != s.Voice[j].Answers {
			return s.Voice[i].Answers > s.Voice[j].Answers
		}
		return s.Voice[i].Name < s.Voice[j].Name
	})
	for _, p := range pages {
		s.Pages = append(s.Pages, *p)
		if p.Site != "" {
			sites[p.Site].Pages++
		}
	}
	seenSite := map[string]bool{}
	for _, st := range brand.AllSites() {
		if u := sites[st.URL]; u != nil && !seenSite[st.URL] {
			seenSite[st.URL] = true
			u.Prompts = len(sitePrompts[st.URL])
			s.Sites = append(s.Sites, *u)
		}
	}
	sort.Slice(s.Pages, func(i, j int) bool {
		a, b := s.Pages[i], s.Pages[j]
		if a.Cited != b.Cited {
			return a.Cited > b.Cited
		}
		if a.Surfaced != b.Surfaced {
			return a.Surfaced > b.Surfaced
		}
		return a.URL < b.URL
	})
	var idxs []int
	for i := range promptText {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		row := PromptRow{Idx: i, Prompt: promptText[i], FirstHand: promptFirst[i]}
		if t := prompts[i]; t != nil {
			row.Rates = t.rates()
		}
		for n, c := range promptTop[i] {
			row.TopPicks = append(row.TopPicks, Count{n, c})
		}
		sort.Slice(row.TopPicks, func(a, b int) bool { return row.TopPicks[a].N > row.TopPicks[b].N })
		s.Prompts = append(s.Prompts, row)
	}
	return s
}

// BrandRank is the brand's 1-based place on the share-of-voice board, or 0 if never named.
func (s Summary) BrandRank() int {
	count := 0
	found := false
	for _, v := range s.Voice {
		if v.IsBrand {
			count = v.Answers
			found = true
			break
		}
	}
	if !found {
		return 0
	}
	rank := 1
	for _, v := range s.Voice {
		if v.Answers > count {
			rank++
		}
	}
	return rank
}

// NormalizeURL drops the fragment and trailing slash, so one page counts once.
func NormalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.Index(u, "#"); i >= 0 {
		u = u[:i]
	}
	return strings.TrimSuffix(u, "/")
}

// Host is the URL's host without www.
func Host(u string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(h, "/?"); i >= 0 {
		h = h[:i]
	}
	return strings.TrimPrefix(h, "www.")
}

// isBrand reports whether a product name is the brand, by its name or an alias.
func isBrand(b config.Brand, name string) bool {
	if strings.EqualFold(name, b.Name) {
		return true
	}
	for _, a := range b.Aliases {
		if strings.EqualFold(name, a) {
			return true
		}
	}
	return false
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
