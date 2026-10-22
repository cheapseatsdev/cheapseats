// Package report turns a run's graded results into rates, share of voice and the pages that
// shaped the answers.
package report

import (
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
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
	Name    string `json:"name"`
	Answers int    `json:"answers"` // answers that named it
	// Answers that named it, of all answers graded: each product's own rate, so the board's rates
	// don't add up to 100%.
	NamedRate float64 `json:"named_rate"`
	// Deprecated: NamedRate, under the name it had before share of voice was a share. Kept so
	// readers of the JSON keep working; use named_rate or share_of_voice.
	Share float64 `json:"share"`
	// Its mentions of all products' mentions, each product counted at most once per answer: the
	// board's shares add up to 100%.
	ShareOfVoice float64 `json:"share_of_voice"`
	TopPick      int     `json:"top_pick"`
	IsBrand      bool    `json:"is_brand"`
	// Its mean 1-based place among the products an answer names, over the answers that name it at
	// a known place (0 if none): where it comes in the grader's list of names, or for the brand
	// when the list left it out, its grade's position.
	AvgPosition float64 `json:"avg_position"`
	// Of the answers to prompts that ask for a choice: those naming it first, and in the first three.
	NamedFirst Rate `json:"named_first"`
	Top3       Rate `json:"top3"`
	// Position-weighted visibility, as SE Ranking defines it: (N-pos+1)/N in an answer naming N
	// products, 0 in one that doesn't name it, averaged over every answer graded.
	Weighted float64 `json:"position_weighted"`
}

// HeadToHead is the brand against one other product across a run's answers.
type HeadToHead struct {
	Name string `json:"name"`
	// Answers naming both, and of those, which led: the answer's top pick if it's one of the two,
	// otherwise the one named first. An answer whose order isn't known counts for neither.
	Both            int `json:"both"`
	BrandAhead      int `json:"brand_ahead"`
	CompetitorAhead int `json:"competitor_ahead"`
	BrandOnly       int `json:"brand_only"`
	CompetitorOnly  int `json:"competitor_only"`
	Neither         int `json:"neither"`
}

// Objection is one concern answers raised about the brand, folded (lowercase, punctuation
// collapsed) so the same phrase counts once, with how many answers naming the brand raised it.
type Objection struct {
	Text    string `json:"objection"`
	Answers int    `json:"answers"`
	// The first prompt (0-based) it was raised for, and its text, as an example.
	Prompt     int    `json:"example_prompt_idx"`
	PromptText string `json:"example_prompt"`
}

// SourceRates are how far a page or host reached. Retrieved and cited rates are of every answer
// graded with its text; citation share is of every citation in the run, so a run's shares add up
// to 100%; cited when surfaced is of the answers whose searches returned it.
type SourceRates struct {
	RetrievedRate     float64 `json:"retrieved_rate"`
	CitedRate         float64 `json:"cited_rate"`
	CitationShare     float64 `json:"citation_share"`
	CitedWhenSurfaced float64 `json:"cited_when_surfaced"`
}

type Page struct {
	URL        string `json:"url"`
	Host       string `json:"host"`
	FirstParty bool   `json:"first_party"`
	// The brand's site it's on, and that site's kind, when FirstParty.
	Site     string `json:"site,omitempty"`
	SiteKind string `json:"site_kind,omitempty"`
	// Its source class (see Classifier.Class), and whose site it is when that's a competitor.
	Class          string `json:"class"`
	CompetitorSite string `json:"competitor,omitempty"`
	// What kind of page it is (PageType): listicle, comparison, review, docs and so on.
	Type     string `json:"page_type"`
	Cited    int    `json:"cited"`    // answers that linked it
	Surfaced int    `json:"surfaced"` // answers whose searches returned it
	// Answers whose searches returned it and that linked it.
	SurfacedAndCited int `json:"surfaced_and_cited"`
	SourceRates
	Prompts []int `json:"prompts"`
}

// HostUse is a host's pages taken together, counting each answer once however many of its
// pages the answer drew on. A brand's or competitor's site under a path (github.com/acme) is its
// own row.
type HostUse struct {
	Host             string `json:"host"`
	Class            string `json:"class"`
	CompetitorSite   string `json:"competitor,omitempty"`
	Cited            int    `json:"cited"`
	Surfaced         int    `json:"surfaced"`
	SurfacedAndCited int    `json:"surfaced_and_cited"`
	Pages            int    `json:"pages"`
	SourceRates
	// For a host that isn't the brand's, what the answers citing it name: the brand in BrandNamed
	// of them, each competitor in Competitors. Bucket sums it up: missing (competitors but never
	// the brand), unique (the brand, no competitor), strong (the brand more often than any
	// competitor) or shared (both). Empty for the brand's own hosts and when they name no one.
	Bucket      string  `json:"bucket,omitempty"`
	BrandNamed  int     `json:"brand_named"`
	Competitors []Count `json:"competitors_named,omitempty"`
}

// ClassUse is one source class across the run: answers citing or surfacing any page of it,
// and its share of the run's citations.
type ClassUse struct {
	Class         string  `json:"class"`
	Cited         int     `json:"cited"`
	Surfaced      int     `json:"surfaced"`
	Pages         int     `json:"pages"`
	Hosts         int     `json:"hosts"`
	CitationShare float64 `json:"citation_share"`
}

// TypeUse is one page type across the run, counted as ClassUse counts a class.
type TypeUse struct {
	Type          string  `json:"page_type"`
	Cited         int     `json:"cited"`
	Surfaced      int     `json:"surfaced"`
	Pages         int     `json:"pages"`
	CitationShare float64 `json:"citation_share"`
}

// Outreach is a third-party host the answers cite while naming competitors and never the brand:
// somewhere to get the brand onto. Score is distinct competitors named times answers citing it.
type Outreach struct {
	Host        string   `json:"host"`
	Class       string   `json:"class"`
	Citations   int      `json:"citations"`
	Competitors []string `json:"competitors"`
	Score       int      `json:"score"`
	Page        string   `json:"top_page"` // its most cited page
}

// Buckets for a host, from what the answers citing it name.
const (
	Missing = "missing"
	Shared  = "shared"
	Strong  = "strong"
	Unique  = "unique"
)

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
	Info      bool    `json:"info,omitempty"`       // scored on named only (see sets.Prompt)
	// The prompt names the brand or an alias, so being named in its answers is no achievement.
	Branded bool `json:"branded,omitempty"`
}

type Count struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

type Summary struct {
	Overall Rates `json:"overall"`
	// Overall without the branded prompts: the headline, since naming the brand in a question
	// that names it proves little. The same as Overall when no prompt is branded.
	Unbranded      Rates        `json:"unbranded"`
	BrandedPrompts int          `json:"branded_prompts"`
	Slices         []Slice      `json:"slices"`
	Voice          []Voice      `json:"voice"`
	HeadToHead     []HeadToHead `json:"head_to_head"`
	Objections     []Objection  `json:"objections"`
	Pages          []Page       `json:"pages"`
	Hosts          []HostUse    `json:"hosts"`
	Classes        []ClassUse   `json:"source_classes"`
	PageTypes      []TypeUse    `json:"page_types"`
	Outreach       []Outreach   `json:"outreach"`
	// The brand's pages that searches returned and no answer cited.
	Uncited  []Page      `json:"found_not_cited"`
	Sites    []SiteUse   `json:"sites"`
	Prompts  []PromptRow `json:"prompts"`
	Done     int         `json:"done"`
	Answers  int         `json:"answers"` // answers that could reach a page (search track, or any search, fetch or citation): what source rates are of
	Errors   int         `json:"errors"`
	Pending  int         `json:"pending"`
	CostUSD  float64     `json:"cost_usd"`
	Searches int         `json:"searches"`
}

// Tally counts graded answers: how many named the brand, and of the answers to prompts that ask for
// a choice (Sel), how many recommended it or made it the top pick. Answers to info prompts count
// only toward named, since a good answer to them may recommend nothing.
type Tally struct{ Top, Rec, Vis, N, Sel int }

func (t *Tally) Add(g *grade.Grade, brand string, info bool) {
	t.N++
	if g.Verdict != grade.Absent {
		t.Vis++
	}
	if info {
		return
	}
	t.Sel++
	if g.IsTopPick(brand) {
		t.Top++
	}
	if g.Verdict == grade.Recommended {
		t.Rec++
	}
}

// Plus is both tallies together.
func (t Tally) Plus(x Tally) Tally {
	return Tally{t.Top + x.Top, t.Rec + x.Rec, t.Vis + x.Vis, t.N + x.N, t.Sel + x.Sel}
}

func (t Tally) Rates() Rates {
	return Rates{TopPick: NewRate(t.Top, t.Sel), Recommended: NewRate(t.Rec, t.Sel), Visible: NewRate(t.Vis, t.N)}
}

// Options is what a summary can use beyond the brand: the competitors, for their domains and to
// tell them from other products named, and the [sources] overrides.
type Options struct {
	Competitors []sets.Competitor
	Sources     map[string][]string
}

// Branded reports whether a prompt names the brand or one of its aliases, as a whole word.
func Branded(brand config.Brand, prompt string) bool {
	for _, n := range append([]string{brand.Name}, brand.Aliases...) {
		if sets.Names(prompt, n) {
			return true
		}
	}
	return false
}

// NormalizeObjection folds an objection for counting (grade.Fold), the same way the run's
// sentiment counts it.
func NormalizeObjection(o string) string { return grade.Fold(o) }

// answerRec is what one graded answer named, for the measures worked out after the pass.
type answerRec struct {
	brand bool
	top   string         // the top pick's key
	pos   map[string]int // product key -> its 1-based place, when known
	names []string       // keys of the products named, the brand's included when named
}

func Build(brand config.Brand, results []run.Result) Summary {
	return BuildWith(brand, results, Options{})
}

func BuildWith(brand config.Brand, results []run.Result, opt Options) Summary {
	var s Summary
	var all, unbranded Tally
	slices := map[[2]string]*Tally{}
	sliceErr := map[[2]string]int{}
	var sliceOrder [][2]string
	prompts := map[int]*Tally{}
	promptText := map[int]string{}
	promptTop := map[int]map[string]int{}
	promptFirst := map[int]bool{}
	promptInfo := map[int]bool{}
	promptBranded := map[int]bool{}
	voice := map[string]*Voice{}
	type posAcc struct {
		sum, n, first, top3 int
		weighted            float64
	}
	positions := map[string]*posAcc{}
	var recs []answerRec
	objections := map[string]*Objection{}
	brandKey := strings.ToLower(brand.Name)
	cls := NewClassifier(brand, opt.Competitors, opt.Sources)
	pages := map[string]*Page{}
	pageHost := map[string]string{}
	hosts := map[string]*HostUse{}
	hostCiters := map[string][]int{} // host -> the recs of answers citing it
	classes := map[string]*ClassUse{}
	for _, c := range ClassOrder {
		classes[c] = &ClassUse{Class: c}
	}
	types := map[string]*TypeUse{}
	for _, t := range PageTypeOrder {
		types[t] = &TypeUse{Type: t}
	}
	// Each page's title, the first any answer gave it, so its type doesn't hang on which answer
	// reached it first.
	titles := map[string]string{}
	for _, r := range results {
		if r.Answer == nil {
			continue
		}
		links := append([]answer.Link{}, r.Answer.Citations...)
		for _, q := range r.Answer.Searches {
			links = append(links, q.Results...)
		}
		for _, l := range links {
			if u := NormalizeURL(l.URL); titles[u] == "" {
				titles[u] = strings.TrimSpace(l.Title)
			}
		}
	}
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
			slices[key] = &Tally{}
			sliceOrder = append(sliceOrder, key)
		}
		if _, ok := promptText[r.PromptIdx]; !ok {
			promptBranded[r.PromptIdx] = Branded(brand, r.Prompt)
		}
		promptText[r.PromptIdx] = r.Prompt
		if r.Info {
			promptInfo[r.PromptIdx] = true
		}
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
		g := r.Grade
		s.Done++
		all.Add(g, brand.Name, r.Info)
		if !promptBranded[r.PromptIdx] {
			unbranded.Add(g, brand.Name, r.Info)
		}
		slices[key].Add(g, brand.Name, r.Info)
		if prompts[r.PromptIdx] == nil {
			prompts[r.PromptIdx] = &Tally{}
			promptTop[r.PromptIdx] = map[string]int{}
		}
		prompts[r.PromptIdx].Add(g, brand.Name, r.Info)
		if g.TopPick != nil {
			promptTop[r.PromptIdx][*g.TopPick]++
		}
		// The brand counts as named exactly when its verdict says so, so its share of voice and its
		// named rate always agree, whatever the grader's list of names says.
		named := g.Verdict != grade.Absent
		rec := answerRec{brand: named, pos: map[string]int{}}
		if g.TopPick != nil {
			rec.top = strings.ToLower(*g.TopPick)
			if isBrand(brand, *g.TopPick) {
				rec.top = brandKey
			}
		}
		display := map[string]string{}
		for _, n := range g.Named {
			k := strings.ToLower(n)
			if isBrand(brand, n) {
				if !named {
					continue
				}
				k, n = brandKey, brand.Name
			}
			if _, dup := display[k]; dup {
				continue
			}
			display[k] = n
			rec.names = append(rec.names, k)
			rec.pos[k] = len(rec.names)
		}
		// Every product's place is on one scale, the grader's list of names, so two can't both be
		// first. The grade's position stands in only when that list left the brand out.
		if _, ok := display[brandKey]; named && !ok {
			display[brandKey] = brand.Name
			rec.names = append(rec.names, brandKey)
			if g.Position != nil && *g.Position > 0 {
				rec.pos[brandKey] = *g.Position
			}
		}
		for _, k := range rec.names {
			v := voice[k]
			if v == nil {
				v = &Voice{Name: display[k], IsBrand: k == brandKey}
				voice[k] = v
				positions[k] = &posAcc{}
			}
			v.Answers++
			if rec.top == k {
				v.TopPick++
			}
			if p := rec.pos[k]; p > 0 {
				a := positions[k]
				a.sum, a.n = a.sum+p, a.n+1
				if !r.Info && p == 1 {
					a.first++
				}
				if !r.Info && p <= 3 {
					a.top3++
				}
				n := max(len(rec.names), p)
				a.weighted += float64(n-p+1) / float64(n)
			}
		}
		if named {
			seen := map[string]bool{}
			for _, o := range g.Objections {
				k := NormalizeObjection(o)
				if k == "" || seen[k] {
					continue
				}
				seen[k] = true
				x := objections[k]
				if x == nil {
					x = &Objection{Text: k, Prompt: r.PromptIdx, PromptText: r.Prompt}
					objections[k] = x
				}
				x.Answers++
			}
		}
		recs = append(recs, rec)
		recIdx := len(recs) - 1
		if r.Answer == nil {
			continue
		}
		// Source rates are of the answers that could reach a page: recall answers have no tools,
		// so counting them would only dilute every page's rate.
		if r.Track == answer.Search || len(r.Answer.Searches) > 0 || len(r.Answer.Fetches) > 0 || len(r.Answer.Citations) > 0 {
			s.Answers++
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
				var hk string
				p.Class, p.CompetitorSite, hk = cls.classify(u)
				p.Type = PageType(u, titles[u])
				pages[u] = p
				pageHost[u] = hk
				classes[p.Class].Pages++
				types[p.Type].Pages++
				h := hosts[hk]
				if h == nil {
					h = &HostUse{Host: hk, Class: p.Class, CompetitorSite: p.CompetitorSite}
					if p.Class != Own && p.Class != Competitor {
						h.Class, _, _ = cls.classify("https://" + hk) // the host's class, not its first page's
					}
					hosts[hk] = h
				}
				h.Pages++
			}
			if !containsInt(p.Prompts, r.PromptIdx) {
				p.Prompts = append(p.Prompts, r.PromptIdx)
			}
			return p
		}
		cited := map[string]bool{}
		citedSite := map[string]bool{}
		citedHost := map[string]bool{}
		citedClass := map[string]bool{}
		citedType := map[string]bool{}
		for _, c := range r.Answer.Citations {
			p := touch(c.URL)
			if !cited[p.URL] {
				cited[p.URL] = true
				p.Cited++
			}
			if hk := pageHost[p.URL]; !citedHost[hk] {
				citedHost[hk] = true
				hosts[hk].Cited++
				hostCiters[hk] = append(hostCiters[hk], recIdx)
			}
			if !citedClass[p.Class] {
				citedClass[p.Class] = true
				classes[p.Class].Cited++
			}
			if !citedType[p.Type] {
				citedType[p.Type] = true
				types[p.Type].Cited++
			}
			if p.Site != "" && !citedSite[p.Site] {
				citedSite[p.Site] = true
				siteUse(p.Site).Cited++
				sitePrompts[p.Site][r.PromptIdx] = true
			}
		}
		surfaced := map[string]bool{}
		surfacedSite := map[string]bool{}
		surfacedHost := map[string]bool{}
		surfacedClass := map[string]bool{}
		surfacedType := map[string]bool{}
		for _, q := range r.Answer.Searches {
			for _, l := range q.Results {
				p := touch(l.URL)
				if !surfaced[p.URL] {
					surfaced[p.URL] = true
					p.Surfaced++
					if cited[p.URL] {
						p.SurfacedAndCited++
					}
				}
				if hk := pageHost[p.URL]; !surfacedHost[hk] {
					surfacedHost[hk] = true
					hosts[hk].Surfaced++
					if citedHost[hk] {
						hosts[hk].SurfacedAndCited++
					}
				}
				if !surfacedClass[p.Class] {
					surfacedClass[p.Class] = true
					classes[p.Class].Surfaced++
				}
				if !surfacedType[p.Type] {
					surfacedType[p.Type] = true
					types[p.Type].Surfaced++
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

	s.Overall = all.Rates()
	s.Unbranded = unbranded.Rates()
	for _, b := range promptBranded {
		if b {
			s.BrandedPrompts++
		}
	}
	for _, k := range sliceOrder {
		s.Slices = append(s.Slices, Slice{Answerer: k[0], Track: answer.Track(k[1]), Rates: slices[k].Rates(), Errors: sliceErr[k]})
	}
	mentions := 0
	for _, v := range voice {
		mentions += v.Answers
	}
	for k, v := range voice {
		if s.Done > 0 {
			v.NamedRate = float64(v.Answers) / float64(s.Done)
			v.Share = v.NamedRate
			v.ShareOfVoice = float64(v.Answers) / float64(mentions)
		}
		a := positions[k]
		if a.n > 0 {
			v.AvgPosition = float64(a.sum) / float64(a.n)
		}
		v.NamedFirst, v.Top3 = NewRate(a.first, all.Sel), NewRate(a.top3, all.Sel)
		if s.Done > 0 {
			v.Weighted = a.weighted / float64(s.Done)
		}
		s.Voice = append(s.Voice, *v)
	}
	sort.Slice(s.Voice, func(i, j int) bool {
		if s.Voice[i].Answers != s.Voice[j].Answers {
			return s.Voice[i].Answers > s.Voice[j].Answers
		}
		return s.Voice[i].Name < s.Voice[j].Name
	})
	s.HeadToHead = headToHeads(s.Voice, recs, brandKey)
	for _, o := range objections {
		s.Objections = append(s.Objections, *o)
	}
	sort.Slice(s.Objections, func(i, j int) bool {
		a, b := s.Objections[i], s.Objections[j]
		if a.Answers != b.Answers {
			return a.Answers > b.Answers
		}
		return a.Text < b.Text
	})

	citations, hostCitations := 0, 0
	for _, p := range pages {
		citations += p.Cited
	}
	for _, h := range hosts {
		hostCitations += h.Cited
	}
	reach := func(cited, surfaced, both, total int) SourceRates {
		var r SourceRates
		if s.Answers > 0 {
			r.RetrievedRate = float64(surfaced) / float64(s.Answers)
			r.CitedRate = float64(cited) / float64(s.Answers)
		}
		if total > 0 {
			r.CitationShare = float64(cited) / float64(total)
		}
		if surfaced > 0 {
			r.CitedWhenSurfaced = float64(both) / float64(surfaced)
		}
		return r
	}
	for _, p := range pages {
		p.SourceRates = reach(p.Cited, p.Surfaced, p.SurfacedAndCited, citations)
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
	for _, p := range s.Pages {
		if p.FirstParty && p.Surfaced > 0 && p.Cited == 0 {
			s.Uncited = append(s.Uncited, p)
		}
	}

	competitor := competitorNames(opt.Competitors, brandKey)
	for k, h := range hosts {
		h.SourceRates = reach(h.Cited, h.Surfaced, h.SurfacedAndCited, hostCitations)
		classes[h.Class].Hosts++
		if h.Class != Own {
			bucket(h, hostCiters[k], recs, voice, competitor)
		}
		s.Hosts = append(s.Hosts, *h)
	}
	sort.Slice(s.Hosts, func(i, j int) bool {
		a, b := s.Hosts[i], s.Hosts[j]
		if a.Cited != b.Cited {
			return a.Cited > b.Cited
		}
		if a.Surfaced != b.Surfaced {
			return a.Surfaced > b.Surfaced
		}
		return a.Host < b.Host
	})
	for _, c := range ClassOrder {
		u := classes[c]
		if citations > 0 {
			n := 0
			for _, p := range pages {
				if p.Class == c {
					n += p.Cited
				}
			}
			u.CitationShare = float64(n) / float64(citations)
		}
		s.Classes = append(s.Classes, *u)
	}
	for _, t := range PageTypeOrder {
		u := types[t]
		if citations > 0 {
			n := 0
			for _, p := range pages {
				if p.Type == t {
					n += p.Cited
				}
			}
			u.CitationShare = float64(n) / float64(citations)
		}
		s.PageTypes = append(s.PageTypes, *u)
	}
	for _, h := range s.Hosts {
		if h.Bucket != Missing || h.Class == Competitor {
			continue
		}
		o := Outreach{Host: h.Host, Class: h.Class, Citations: h.Cited, Score: len(h.Competitors) * h.Cited}
		for _, c := range h.Competitors {
			o.Competitors = append(o.Competitors, c.Name)
		}
		for _, p := range s.Pages {
			if pageHost[p.URL] == h.Host {
				o.Page = p.URL // the pages are sorted most cited first
				break
			}
		}
		s.Outreach = append(s.Outreach, o)
	}
	sort.SliceStable(s.Outreach, func(i, j int) bool { return s.Outreach[i].Score > s.Outreach[j].Score })

	var idxs []int
	for i := range promptText {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		row := PromptRow{Idx: i, Prompt: promptText[i], FirstHand: promptFirst[i], Info: promptInfo[i], Branded: promptBranded[i]}
		if t := prompts[i]; t != nil {
			row.Rates = t.Rates()
		}
		for n, c := range promptTop[i] {
			row.TopPicks = append(row.TopPicks, Count{n, c})
		}
		sort.Slice(row.TopPicks, func(a, b int) bool {
			if row.TopPicks[a].N != row.TopPicks[b].N {
				return row.TopPicks[a].N > row.TopPicks[b].N
			}
			return row.TopPicks[a].Name < row.TopPicks[b].Name
		})
		s.Prompts = append(s.Prompts, row)
	}
	return s
}

// headToHeads sets the brand against every other product on the board, in board order.
func headToHeads(board []Voice, recs []answerRec, brandKey string) []HeadToHead {
	var out []HeadToHead
	for _, v := range board {
		if v.IsBrand {
			continue
		}
		k := strings.ToLower(v.Name)
		h := HeadToHead{Name: v.Name}
		for _, r := range recs {
			comp := slices.Contains(r.names, k)
			switch {
			case r.brand && comp:
				h.Both++
				bp, cp := r.pos[brandKey], r.pos[k]
				switch {
				case r.top == brandKey:
					h.BrandAhead++
				case r.top == k:
					h.CompetitorAhead++
				case bp > 0 && cp > 0 && bp < cp:
					h.BrandAhead++
				case bp > 0 && cp > 0 && cp < bp:
					h.CompetitorAhead++
				}
			case r.brand:
				h.BrandOnly++
			case comp:
				h.CompetitorOnly++
			default:
				h.Neither++
			}
		}
		out = append(out, h)
	}
	return out
}

// competitorNames says which product keys are competitors, by canonical name: the configured
// competitors and their aliases, or, with none configured, every product but the brand.
func competitorNames(cs []sets.Competitor, brandKey string) func(string) (string, bool) {
	names := map[string]string{}
	for _, c := range cs {
		names[strings.ToLower(strings.TrimSpace(c.Name))] = c.Name
		for _, a := range c.Aliases {
			names[strings.ToLower(strings.TrimSpace(a))] = c.Name
		}
	}
	return func(k string) (string, bool) {
		if k == brandKey {
			return "", false
		}
		if len(names) == 0 {
			return k, true
		}
		n, ok := names[k]
		return n, ok
	}
}

// bucket fills in what the answers citing a host name, and the bucket that puts it in.
func bucket(h *HostUse, citers []int, recs []answerRec, board map[string]*Voice, competitor func(string) (string, bool)) {
	counts := map[string]int{}
	for _, i := range citers {
		r := recs[i]
		if r.brand {
			h.BrandNamed++
		}
		seen := map[string]bool{}
		for _, k := range r.names {
			name, ok := competitor(k)
			if !ok || seen[name] {
				continue
			}
			seen[name] = true
			if v := board[k]; v != nil && strings.EqualFold(name, k) {
				name = v.Name // as the board spells it
			}
			counts[name]++
		}
	}
	most := 0
	for n, c := range counts {
		h.Competitors = append(h.Competitors, Count{n, c})
		most = max(most, c)
	}
	sort.Slice(h.Competitors, func(i, j int) bool {
		a, b := h.Competitors[i], h.Competitors[j]
		if a.N != b.N {
			return a.N > b.N
		}
		return a.Name < b.Name
	})
	switch {
	case h.BrandNamed == 0 && most == 0:
	case h.BrandNamed == 0:
		h.Bucket = Missing
	case most == 0:
		h.Bucket = Unique
	case h.BrandNamed > most:
		h.Bucket = Strong
	default:
		h.Bucket = Shared
	}
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
