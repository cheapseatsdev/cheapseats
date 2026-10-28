// Package queries looks at the web searches the assistants ran on the way to an answer: what they
// typed, how far that strayed from the prompt, which names they added, and whether the results
// reached the brand's sites. All of it comes from stored answers; nothing here calls a model.
package queries

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// Input is what Build reads: the brand (with its sites), the set the answers are to (for its
// competitors and prompt ids), and the answers.
type Input struct {
	Brand   config.Brand
	Set     sets.Set
	Results []run.Result
	// Prompt ids already in use in any set, so a suggested prompt doesn't reuse one.
	Taken map[string]bool
}

type Report struct {
	Answers  int `json:"answers"`  // search-track answers
	Searched int `json:"searched"` // of them, answers that ran at least one search
	Searches int `json:"searches"` // searches run
	Distinct int `json:"distinct"` // distinct queries, normalised
	// Mean overlap of each search's words with its prompt's (see Overlap).
	FromPrompt float64 `json:"from_prompt"`
	PromptKept float64 `json:"prompt_kept"`

	Prompts   []Prompt    `json:"prompts"`
	Queries   []QueryRow  `json:"queries"`   // most answers first
	Injected  []Injection `json:"injected"`  // names the assistants added to a search
	Added     []Word      `json:"added"`     // words searches add to their prompt, most used first
	Dropped   []Word      `json:"dropped"`   // prompt words searches leave out
	Suggested []Suggest   `json:"suggested"` // queries worth asking as prompts
}

// Prompt is one prompt's searches.
type Prompt struct {
	Idx       int     `json:"idx"`
	ID        string  `json:"id,omitempty"`
	Prompt    string  `json:"prompt"`
	Answers   int     `json:"answers"`
	Searched  int     `json:"searched"`
	PerAnswer float64 `json:"per_answer"` // searches per answer, over every answer
	Distinct  int     `json:"distinct"`
	// The prompt's queries, each with how many answers ran it, most first.
	Queries    []Count `json:"queries"`
	FromPrompt float64 `json:"from_prompt"`
	PromptKept float64 `json:"prompt_kept"`
	Added      []Word  `json:"added,omitempty"`
	Dropped    []Word  `json:"dropped,omitempty"`
}

type Count struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// QueryRow is one normalised query across the set.
type QueryRow struct {
	Query   string `json:"query"` // as first asked
	Key     string `json:"key"`   // normalised (Normalize)
	Prompts []int  `json:"prompts"`
	Answers int    `json:"answers"` // answers that ran it
	Runs    int    `json:"runs"`    // times it was run, an answer may repeat itself
	Failed  int    `json:"failed"`  // runs that errored or didn't complete
	// Runs whose results included a page on one of the brand's sites.
	FirstParty int `json:"first_party"`
	// Competitors named in the results' titles or hosts, by how many runs surfaced them.
	Competitors []Count `json:"competitors,omitempty"`
	// Of the graded answers that ran it, how many named the brand.
	Named  int `json:"named"`
	Graded int `json:"graded"`
	// Names in the query that its prompt didn't have.
	Injected   []string `json:"injected,omitempty"`
	FromPrompt float64  `json:"from_prompt"`
	PromptKept float64  `json:"prompt_kept"`
}

// Injection is a brand or competitor the assistants put into a search when the prompt hadn't
// named it: "the assistant added X to the search".
type Injection struct {
	Name     string   `json:"name"`
	IsBrand  bool     `json:"is_brand"`
	Searches int      `json:"searches"`
	Answers  int      `json:"answers"`
	Prompts  int      `json:"prompts"`
	Examples []string `json:"examples"` // up to three of the queries
}

// Word is a word added or dropped, by how many searches and prompts it was.
type Word struct {
	Word     string `json:"word"`
	Searches int    `json:"searches"`
	Prompts  int    `json:"prompts"`
}

// Suggest is a query that surfaced competitors and never the brand, as a prompt to add to the
// set's file.
type Suggest struct {
	Query       string   `json:"query"`
	Competitors []string `json:"competitors"`
	Answers     int      `json:"answers"`
	File        string   `json:"file"`
	TOML        string   `json:"toml"`
}

var (
	quotes = strings.NewReplacer(`"`, "", "“", "", "”", "", "‘", "", "’", "", "`", "", "«", "", "»", "")
	// An apostrophe used as one (it's) is part of the word; only quoting ones go.
	edgeQuote = regexp.MustCompile(`(^|\s)'+|'+(\s|$)`)
)

// Normalize is a query's key for counting: lowercase, quotes stripped, whitespace collapsed.
func Normalize(q string) string {
	q = quotes.Replace(strings.ToLower(q))
	q = edgeQuote.ReplaceAllString(q, "$1$2")
	return strings.Join(strings.Fields(q), " ")
}

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the and or but if then so of to in on at by for with from into onto about as is are was were be been being
		do does did done have has had having i me my we our us you your it its it's they them their this that these those there here
		what which who whom whose when where why how can could should would will shall may might must not no yes
		any some all each every more most other such than too very just also only own same both few
		get got need want use using used like make one way good am im i'm ive i've we're were dont don't`) {
		stop[w] = true
	}
}

// Tokens are a text's words for comparing a query with its prompt: lowercase runs of letters
// and digits (keeping inner dots, dashes and apostrophes, so "otterly.ai" and "it's" hold
// together), stopwords and one-letter words dropped, each once, in order.
func Tokens(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '\'' || r == '’')
	}) {
		f = strings.Trim(strings.ReplaceAll(f, "’", "'"), ".-'")
		if len([]rune(f)) < 2 || stop[f] || seen[stem(f)] {
			continue
		}
		seen[stem(f)] = true
		out = append(out, f)
	}
	return out
}

// stem folds a plain plural onto its singular, so "tools" in a query matches "tool" in a prompt.
func stem(w string) string {
	if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is") {
		return w[:len(w)-1]
	}
	return w
}

// Overlap compares a query's words with its prompt's: fromPrompt is the share of the query's
// words that are in the prompt, kept the share of the prompt's words still in the query. Added
// and dropped are the words on one side only. A side with no words scores 0.
func Overlap(prompt, query string) (fromPrompt, kept float64, added, dropped []string) {
	p, q := Tokens(prompt), Tokens(query)
	in := func(ws []string) map[string]bool {
		m := map[string]bool{}
		for _, w := range ws {
			m[stem(w)] = true
		}
		return m
	}
	pm, qm := in(p), in(q)
	both := 0
	for _, w := range q {
		if pm[stem(w)] {
			both++
		} else {
			added = append(added, w)
		}
	}
	for _, w := range p {
		if !qm[stem(w)] {
			dropped = append(dropped, w)
		}
	}
	if len(q) > 0 {
		fromPrompt = float64(both) / float64(len(q))
	}
	if len(p) > 0 {
		kept = float64(both) / float64(len(p))
	}
	return
}

// Name is a brand or competitor and the forms it goes by.
type Name struct {
	Name    string
	Forms   []string
	IsBrand bool
}

// Names are the brand and the set's competitors, each with its aliases.
func Names(b config.Brand, set sets.Set) []Name {
	out := []Name{{Name: b.Name, Forms: append([]string{b.Name}, b.Aliases...), IsBrand: true}}
	for _, c := range set.Competitors {
		if strings.EqualFold(c.Name, b.Name) {
			continue
		}
		out = append(out, Name{Name: c.Name, Forms: append([]string{c.Name}, c.Aliases...)})
	}
	return out
}

// In reports whether text names n by any of its forms, as a whole word.
func (n Name) In(text string) bool {
	for _, f := range n.Forms {
		if sets.Names(text, f) {
			return true
		}
	}
	return false
}

// OnHost reports whether a URL's host is n's own: a form squeezed to letters and digits
// ("Peec AI" to "peecai") matches the host squeezed the same way ("peec.ai"), or its first
// label ("otterly" in otterly.ai). Forms shorter than four letters don't count, so a short
// alias can't match half the web.
func (n Name) OnHost(url string) bool {
	host := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(url), "https://"), "http://")
	if i := strings.IndexAny(host, "/?#:"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimPrefix(host, "www.")
	whole := squeeze(host)
	label := host
	if i := strings.LastIndex(host, "."); i >= 0 {
		label = host[:i]
		if j := strings.LastIndex(label, "."); j >= 0 {
			label = label[j+1:]
		}
	}
	label = squeeze(label)
	for _, f := range n.Forms {
		s := squeeze(f)
		if len(s) < 4 {
			continue
		}
		if s == whole || s == label {
			return true
		}
	}
	return false
}

func squeeze(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Injected is the names a query has and its prompt doesn't.
func Injected(names []Name, prompt, query string) []Name {
	var out []Name
	for _, n := range names {
		if n.In(query) && !n.In(prompt) {
			out = append(out, n)
		}
	}
	return out
}

// withoutNames drops the words of any name the query has: a name added to a search counts under
// Injected, not as vocabulary.
func withoutNames(words []string, names []Name, query string) []string {
	skip := map[string]bool{}
	for _, n := range names {
		for _, f := range n.Forms {
			if sets.Names(query, f) {
				for _, w := range Tokens(f) {
					skip[stem(w)] = true
				}
			}
		}
	}
	var out []string
	for _, w := range words {
		if !skip[stem(w)] {
			out = append(out, w)
		}
	}
	return out
}

// Build reads every search-track answer's searches.
func Build(in Input) Report {
	var rep Report
	names := Names(in.Brand, in.Set)
	type promptAcc struct {
		Prompt
		order   int
		queries map[string]int
		shown   map[string]string
		from    []float64
		kept    []float64
		added   map[string]int
		dropped map[string]int
	}
	prompts := map[string]*promptAcc{}
	var promptOrder []string
	type queryAcc struct {
		QueryRow
		prompts     map[int]bool
		competitors map[string]int
		injected    map[string]bool
		from, kept  []float64
	}
	queries := map[string]*queryAcc{}
	type injAcc struct {
		Injection
		answers int
		prompts map[int]bool
		seen    map[string]bool
	}
	inj := map[string]*injAcc{}
	addedAll, droppedAll := map[string]*Word{}, map[string]*Word{}
	addedPrompts, droppedPrompts := map[string]map[int]bool{}, map[string]map[int]bool{}
	var fromAll, keptAll []float64

	for _, r := range in.Results {
		if r.Track != answer.Search || r.Status != run.ResultDone || r.Answer == nil {
			continue
		}
		pk := r.PromptID
		if pk == "" {
			pk = fmt.Sprintf("#%d", r.PromptIdx)
		}
		p := prompts[pk]
		if p == nil {
			p = &promptAcc{Prompt: Prompt{Idx: r.PromptIdx, ID: r.PromptID, Prompt: r.Prompt}, queries: map[string]int{}, shown: map[string]string{},
				added: map[string]int{}, dropped: map[string]int{}}
			prompts[pk] = p
			promptOrder = append(promptOrder, pk)
		}
		rep.Answers++
		p.Answers++
		if len(r.Answer.Searches) > 0 {
			rep.Searched++
			p.Searched++
		}
		named := r.Grade != nil && r.Grade.Verdict != grade.Absent
		ranHere := map[string]bool{}
		injHere := map[string]bool{}
		for _, s := range r.Answer.Searches {
			key := Normalize(s.Query)
			if key == "" {
				continue
			}
			rep.Searches++
			p.PerAnswer++
			q := queries[key]
			if q == nil {
				q = &queryAcc{QueryRow: QueryRow{Query: strings.TrimSpace(s.Query), Key: key}, prompts: map[int]bool{}, competitors: map[string]int{}, injected: map[string]bool{}}
				queries[key] = q
			}
			q.Runs++
			q.prompts[r.PromptIdx] = true
			if s.Error != "" || !s.Completed {
				q.Failed++
			}
			first := false
			comp := map[string]bool{}
			for _, l := range s.Results {
				if in.Brand.FirstParty(l.URL) {
					first = true
				}
				for _, n := range names[1:] {
					if n.In(l.Title) || n.OnHost(l.URL) {
						comp[n.Name] = true
					}
				}
			}
			if first {
				q.FirstParty++
			}
			for c := range comp {
				q.competitors[c]++
			}
			from, kept, added, dropped := Overlap(r.Prompt, s.Query)
			added = withoutNames(added, names, s.Query)
			q.from, q.kept = append(q.from, from), append(q.kept, kept)
			p.from, p.kept = append(p.from, from), append(p.kept, kept)
			fromAll, keptAll = append(fromAll, from), append(keptAll, kept)
			for _, w := range added {
				p.added[w]++
				tally(addedAll, addedPrompts, w, r.PromptIdx)
			}
			for _, w := range dropped {
				p.dropped[w]++
				tally(droppedAll, droppedPrompts, w, r.PromptIdx)
			}
			for _, n := range Injected(names, r.Prompt, s.Query) {
				q.injected[n.Name] = true
				ia := inj[n.Name]
				if ia == nil {
					ia = &injAcc{Injection: Injection{Name: n.Name, IsBrand: n.IsBrand}, prompts: map[int]bool{}, seen: map[string]bool{}}
					inj[n.Name] = ia
				}
				ia.Searches++
				ia.prompts[r.PromptIdx] = true
				if !injHere[n.Name] {
					injHere[n.Name] = true
					ia.answers++
				}
				if !ia.seen[key] && len(ia.Examples) < 3 {
					ia.seen[key] = true
					ia.Examples = append(ia.Examples, q.Query)
				}
			}
			if !ranHere[key] {
				ranHere[key] = true
				q.Answers++
				p.queries[key]++
				if _, ok := p.shown[key]; !ok {
					p.shown[key] = q.Query
				}
				if r.Grade != nil {
					q.Graded++
					if named {
						q.Named++
					}
				}
			}
		}
	}

	rep.Distinct = len(queries)
	rep.FromPrompt, rep.PromptKept = mean(fromAll), mean(keptAll)
	sort.SliceStable(promptOrder, func(i, j int) bool { return prompts[promptOrder[i]].Idx < prompts[promptOrder[j]].Idx })
	for _, k := range promptOrder {
		p := prompts[k]
		if p.Answers > 0 {
			p.PerAnswer /= float64(p.Answers)
		}
		p.Distinct = len(p.queries)
		for key, n := range p.queries {
			p.Queries = append(p.Queries, Count{p.shown[key], n})
		}
		sortCounts(p.Queries)
		p.FromPrompt, p.PromptKept = mean(p.from), mean(p.kept)
		p.Added, p.Dropped = words(p.added, 8), words(p.dropped, 8)
		rep.Prompts = append(rep.Prompts, p.Prompt)
	}
	for _, q := range queries {
		for i := range q.prompts {
			q.Prompts = append(q.Prompts, i)
		}
		sort.Ints(q.Prompts)
		for c, n := range q.competitors {
			q.Competitors = append(q.Competitors, Count{c, n})
		}
		sortCounts(q.Competitors)
		for n := range q.injected {
			q.Injected = append(q.Injected, n)
		}
		sort.Strings(q.Injected)
		q.FromPrompt, q.PromptKept = mean(q.from), mean(q.kept)
		rep.Queries = append(rep.Queries, q.QueryRow)
	}
	sort.Slice(rep.Queries, func(i, j int) bool {
		a, b := rep.Queries[i], rep.Queries[j]
		if a.Answers != b.Answers {
			return a.Answers > b.Answers
		}
		if len(a.Prompts) != len(b.Prompts) {
			return len(a.Prompts) > len(b.Prompts)
		}
		return a.Key < b.Key
	})
	for _, ia := range inj {
		ia.Answers, ia.Prompts = ia.answers, len(ia.prompts)
		rep.Injected = append(rep.Injected, ia.Injection)
	}
	sort.Slice(rep.Injected, func(i, j int) bool {
		a, b := rep.Injected[i], rep.Injected[j]
		if a.Searches != b.Searches {
			return a.Searches > b.Searches
		}
		return a.Name < b.Name
	})
	rep.Added, rep.Dropped = rankWords(addedAll, addedPrompts), rankWords(droppedAll, droppedPrompts)
	rep.Suggested = suggest(in, names, rep.Queries)
	return rep
}

func tally(m map[string]*Word, prompts map[string]map[int]bool, w string, idx int) {
	k := stem(w)
	if m[k] == nil {
		m[k] = &Word{Word: w}
		prompts[k] = map[int]bool{}
	}
	m[k].Searches++
	prompts[k][idx] = true
}

func rankWords(m map[string]*Word, prompts map[string]map[int]bool) []Word {
	out := []Word{}
	for k, w := range m {
		w.Prompts = len(prompts[k])
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Searches != out[j].Searches {
			return out[i].Searches > out[j].Searches
		}
		if out[i].Prompts != out[j].Prompts {
			return out[i].Prompts > out[j].Prompts
		}
		return out[i].Word < out[j].Word
	})
	return out
}

func words(m map[string]int, n int) []Word {
	var out []Word
	for w, c := range m {
		out = append(out, Word{Word: w, Searches: c, Prompts: 1})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Searches != out[j].Searches {
			return out[i].Searches > out[j].Searches
		}
		return out[i].Word < out[j].Word
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func sortCounts(c []Count) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].N != c[j].N {
			return c[i].N > c[j].N
		}
		return c[i].Name < c[j].Name
	})
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return t / float64(len(xs))
}

// maxSuggest is how many queries are offered as prompts.
const maxSuggest = 10

// suggest picks the queries whose results named competitors and never reached the brand's
// sites, most answers first: searches buyers' assistants run where the brand isn't in the
// running. A query naming the brand is left out of every set but head-to-heads, which alone may
// name it (sets.Check).
func suggest(in Input, names []Name, rows []QueryRow) []Suggest {
	out := []Suggest{}
	ids := map[string]bool{}
	for k := range in.Taken {
		ids[k] = true
	}
	for _, p := range in.Set.Prompts {
		ids[p.ID] = true
	}
	texts := map[string]bool{}
	for _, p := range in.Set.Prompts {
		texts[Normalize(p.Text)] = true
	}
	for _, q := range rows {
		if len(out) == maxSuggest {
			break
		}
		if q.FirstParty > 0 || len(q.Competitors) == 0 || texts[q.Key] || in.Set.ID != sets.HeadToHeads && names[0].In(q.Query) {
			continue
		}
		var comp []string
		for _, c := range q.Competitors {
			comp = append(comp, c.Name)
		}
		// One search can run several queries, joined with " | ": each is its own prompt. Ones
		// with search operators (site:, quotes for an exact match) are a search engine's
		// language, not a buyer's.
		for _, part := range strings.Split(q.Query, " | ") {
			part = strings.TrimSpace(part)
			k := Normalize(part)
			if len(out) == maxSuggest || k == "" || texts[k] || strings.Contains(part, "site:") || strings.Contains(part, "site.") || strings.Count(part, `"`) > 0 || len(strings.Fields(part)) < 3 {
				continue
			}
			texts[k] = true
			id := PromptID(part, ids)
			ids[id] = true
			out = append(out, Suggest{Query: part, Competitors: comp, Answers: q.Answers, File: in.Set.File, TOML: Snippet(sets.Prompt{ID: id, Text: part})})
		}
	}
	return out
}

// PromptID makes a prompt id from a query: its first five words, lowercase and dashed, with a
// number added if taken already has it.
func PromptID(query string, taken map[string]bool) string {
	var parts []string
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !(r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r))) }) {
		if stop[w] && len(parts) > 0 {
			continue
		}
		parts = append(parts, w)
		if len(parts) == 5 {
			break
		}
	}
	base := strings.Join(parts, "-")
	if base == "" {
		base = "search-query"
	}
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

// Snippet is a prompt as a [[prompts]] table, ready to paste into a set's file.
func Snippet(p sets.Prompt) string {
	var b bytes.Buffer
	e := toml.NewEncoder(&b)
	e.Indent = ""
	e.Encode(struct {
		Prompts []sets.Prompt `toml:"prompts"`
	}{[]sets.Prompt{p}})
	return strings.TrimSpace(b.String())
}

// Brief is a few lines on what the searches show, for a reader (or a model) that won't look at
// the tables: the words the assistants add to prompts and the names they put into searches.
func (r Report) Brief() string {
	if r.Searches == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d searches over %d answers (%d distinct queries). On average %.0f%% of a search's words come from its prompt, and it keeps %.0f%% of the prompt's.\n",
		r.Searches, r.Answers, r.Distinct, 100*r.FromPrompt, 100*r.PromptKept)
	if len(r.Added) > 0 {
		var ws []string
		for i, w := range r.Added {
			if i == 10 {
				break
			}
			ws = append(ws, fmt.Sprintf("%s (%d)", w.Word, w.Searches))
		}
		fmt.Fprintf(&b, "Words searches add to the prompt: %s.\n", strings.Join(ws, ", "))
	}
	if len(r.Injected) > 0 {
		var ns []string
		for i, n := range r.Injected {
			if i == 8 {
				break
			}
			ns = append(ns, fmt.Sprintf("%s (%d searches, %d prompts)", n.Name, n.Searches, n.Prompts))
		}
		fmt.Fprintf(&b, "Names the assistants added to searches the prompt didn't name: %s.\n", strings.Join(ns, ", "))
	}
	return strings.TrimSpace(b.String())
}
