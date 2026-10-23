package report

import (
	"sort"
)

// An Opportunity is one kind of source across the run, scored by how much the answers lean on it
// and how rarely the brand is in the answers that do: a source class, or one page type within it.
// Gap is the cell's share of the run's citations times the share of its citing answers that don't
// name the brand, so a kind of page that carries many citations and never names the brand comes
// first. An owned cell is the brand's own pages, so the fix is writing that kind of page; an
// earned one is somebody else's, so the fix is getting onto them.
type Opportunity struct {
	Class string `json:"class"`
	// The page type within the class, or "" for the class as a whole.
	Type  string `json:"page_type,omitempty"`
	Owned bool   `json:"owned"`
	// Citations of its pages, each answer counted once per page, and their share of the run's.
	Citations     int     `json:"citations"`
	CitationShare float64 `json:"citation_share"`
	// Answers citing any page of it; of those, the share naming the brand, the share naming a
	// competitor, and which competitors how often.
	Answers            int     `json:"answers"`
	BrandPresence      float64 `json:"brand_presence"`
	CompetitorPresence float64 `json:"competitor_presence"`
	Competitors        []Count `json:"competitors_named,omitempty"`
	Gap                float64 `json:"gap"`
	// Its most cited hosts, up to three, and the prompts (0-based) its citing answers were for.
	Hosts   []string `json:"hosts"`
	Prompts []int    `json:"prompts"`
}

// MinOpportunityCitations is the fewest citations a cell needs to be listed on its own. Under it
// a presence rate is one answer's say-so, so the cell counts only toward its class.
const MinOpportunityCitations = 3

// cellKey is a source class and a page type; type "" is the class as a whole.
type cellKey struct{ class, typ string }

// cell is what the run's answers made of one cell: which answers cited a page of it, for which
// prompts, on which hosts, and how many citations that was.
type cell struct {
	answers   map[int]bool   // rec index
	prompts   map[int]bool   // prompt index
	hosts     map[string]int // host -> citations
	citations int
}

// cells collects citations by class and by class and type.
type cells map[cellKey]*cell

// add records one answer's citation of a page: in its class's cell and in its class-and-type cell.
func (cs cells) add(p *Page, host string, rec, prompt int) {
	for _, k := range []cellKey{{p.Class, ""}, {p.Class, p.Type}} {
		c := cs[k]
		if c == nil {
			c = &cell{answers: map[int]bool{}, prompts: map[int]bool{}, hosts: map[string]int{}}
			cs[k] = c
		}
		c.answers[rec] = true
		c.prompts[prompt] = true
		c.hosts[host]++
		c.citations++
	}
}

// opportunities scores every cell with enough citations, biggest gap first. total is the run's
// citations; competitor says which product keys count as competitors, and board spells them.
func (cs cells) opportunities(recs []answerRec, competitor func(string) (string, bool), board map[string]*Voice, total int) []Opportunity {
	out := []Opportunity{}
	for k, c := range cs {
		if c.citations < MinOpportunityCitations {
			continue
		}
		o := Opportunity{Class: k.class, Type: k.typ, Owned: k.class == Own, Citations: c.citations, Answers: len(c.answers), Hosts: []string{}, Prompts: []int{}}
		if total > 0 {
			o.CitationShare = float64(c.citations) / float64(total)
		}
		brand, comp := 0, 0
		counts := map[string]int{}
		for i := range c.answers {
			r := recs[i]
			if r.brand {
				brand++
			}
			seen := map[string]bool{}
			for _, key := range r.names {
				name, ok := competitor(key)
				if !ok || seen[name] {
					continue
				}
				seen[name] = true
				if v := board[key]; v != nil && name == key {
					name = v.Name // as the board spells it
				}
				counts[name]++
			}
			if len(seen) > 0 {
				comp++
			}
		}
		if o.Answers > 0 {
			o.BrandPresence = float64(brand) / float64(o.Answers)
			o.CompetitorPresence = float64(comp) / float64(o.Answers)
		}
		o.Gap = o.CitationShare * (1 - o.BrandPresence)
		for n, x := range counts {
			o.Competitors = append(o.Competitors, Count{n, x})
		}
		sort.Slice(o.Competitors, func(i, j int) bool {
			a, b := o.Competitors[i], o.Competitors[j]
			if a.N != b.N {
				return a.N > b.N
			}
			return a.Name < b.Name
		})
		var hosts []Count
		for h, n := range c.hosts {
			hosts = append(hosts, Count{h, n})
		}
		sort.Slice(hosts, func(i, j int) bool {
			a, b := hosts[i], hosts[j]
			if a.N != b.N {
				return a.N > b.N
			}
			return a.Name < b.Name
		})
		for _, h := range hosts {
			if len(o.Hosts) == 3 {
				break
			}
			o.Hosts = append(o.Hosts, h.Name)
		}
		for p := range c.prompts {
			o.Prompts = append(o.Prompts, p)
		}
		sort.Ints(o.Prompts)
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Gap != b.Gap {
			return a.Gap > b.Gap
		}
		if a.Citations != b.Citations {
			return a.Citations > b.Citations
		}
		if a.Class != b.Class {
			return indexOf(ClassOrder, a.Class) < indexOf(ClassOrder, b.Class)
		}
		return indexOf(PageTypeOrder, a.Type) < indexOf(PageTypeOrder, b.Type) // "" first
	})
	return out
}

// indexOf is x's place in xs, or -1.
func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}
