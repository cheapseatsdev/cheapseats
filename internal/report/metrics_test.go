package report

import (
	"math"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func graded(prompt int, text string, g grade.Grade, a *answer.Answer) run.Result {
	return run.Result{PromptIdx: prompt, Prompt: text, Answerer: "a", Track: answer.Search, Status: "done", Grade: &g, Answer: a}
}

func voiceOf(s Summary, name string) Voice {
	for _, v := range s.Voice {
		if v.Name == name {
			return v
		}
	}
	return Voice{}
}

// Named rate is each product's own, so the board's add up past 100%; share of voice is of all
// mentions, each product once per answer, so it adds up to exactly 100%.
func TestShareOfVoiceSumsTo100(t *testing.T) {
	brand := config.Brand{Name: "Acme"}
	res := []run.Result{
		graded(0, "p", grade.Grade{Verdict: grade.Mentioned, Named: []string{"Acme", "Beta", "Beta", "Gamma"}}, nil),
		graded(0, "p", grade.Grade{Verdict: grade.Absent, Named: []string{"Beta"}}, nil),
		graded(1, "q", grade.Grade{Verdict: grade.Recommended, Named: []string{"Beta", "Acme"}}, nil),
	}
	s := Build(brand, res)
	named, share := 0.0, 0.0
	for _, v := range s.Voice {
		named += v.NamedRate
		share += v.ShareOfVoice
		if v.Share != v.NamedRate {
			t.Errorf("the deprecated share must stay the named rate: %+v", v)
		}
	}
	if !near(share, 1) || named <= 1 {
		t.Fatalf("share of voice sums to %v, named rates to %v: %+v", share, named, s.Voice)
	}
	// Mentions: Acme 2, Beta 3 (once per answer), Gamma 1.
	if b := voiceOf(s, "Beta"); !near(b.ShareOfVoice, 3.0/6) || !near(b.NamedRate, 1) {
		t.Fatalf("beta: %+v", b)
	}
}

func TestPositions(t *testing.T) {
	brand := config.Brand{Name: "Acme"}
	res := []run.Result{
		// Acme first (by its grade), Beta second, Gamma third of three.
		graded(0, "p", grade.Grade{Verdict: grade.Recommended, Position: ptr(1), Named: []string{"Acme", "Beta", "Gamma"}}, nil),
		// Acme fourth of four, Beta first.
		graded(0, "p", grade.Grade{Verdict: grade.Mentioned, Position: ptr(4), Named: []string{"Beta", "Gamma", "Delta", "Acme"}}, nil),
		// Not named.
		graded(0, "p", grade.Grade{Verdict: grade.Absent, Named: []string{"Gamma"}}, nil),
		// An info answer: counts toward the average, not toward named first.
		{PromptIdx: 1, Prompt: "how", Status: "done", Info: true, Grade: &grade.Grade{Verdict: grade.Mentioned, Position: ptr(1), Named: []string{"Acme"}}},
	}
	s := Build(brand, res)
	a := voiceOf(s, "Acme")
	if !near(a.AvgPosition, 2) { // (1+4+1)/3
		t.Errorf("acme avg position %v", a.AvgPosition)
	}
	if a.NamedFirst.Hits != 1 || a.NamedFirst.N != 3 || a.Top3.Hits != 1 {
		t.Errorf("acme first/top3: %+v %+v", a.NamedFirst, a.Top3)
	}
	// (3-1+1)/3 + (4-4+1)/4 + 0 + 1/1, over 4 answers.
	if want := (1 + 0.25 + 0 + 1) / 4; !near(a.Weighted, want) {
		t.Errorf("acme weighted %v, want %v", a.Weighted, want)
	}
	b := voiceOf(s, "Beta")
	if !near(b.AvgPosition, 1.5) || b.NamedFirst.Hits != 1 || b.Top3.Hits != 2 {
		t.Errorf("beta: %+v", b)
	}
	g := voiceOf(s, "Gamma")
	if !near(g.AvgPosition, 2) || g.NamedFirst.Hits != 1 { // 3, 2, 1
		t.Errorf("gamma: %+v", g)
	}
}

func TestHeadToHead(t *testing.T) {
	brand := config.Brand{Name: "Acme"}
	res := []run.Result{
		graded(0, "p", grade.Grade{Verdict: grade.Mentioned, Position: ptr(2), Named: []string{"Beta", "Acme"}}, nil),                         // beta ahead by order
		graded(0, "p", grade.Grade{Verdict: grade.Recommended, Position: ptr(2), TopPick: ptr("Acme"), Named: []string{"Beta", "Acme"}}, nil), // acme ahead: top pick
		graded(0, "p", grade.Grade{Verdict: grade.Mentioned, Position: ptr(1), Named: []string{"Acme"}}, nil),                                 // acme only
		graded(0, "p", grade.Grade{Verdict: grade.Absent, Named: []string{"Beta"}}, nil),                                                      // beta only
		graded(0, "p", grade.Grade{Verdict: grade.Absent, Named: []string{"Gamma"}}, nil),                                                     // neither
	}
	s := Build(brand, res)
	var h HeadToHead
	for _, x := range s.HeadToHead {
		if x.Name == "Beta" {
			h = x
		}
	}
	if h.Both != 2 || h.BrandAhead != 1 || h.CompetitorAhead != 1 || h.BrandOnly != 1 || h.CompetitorOnly != 1 || h.Neither != 1 {
		t.Fatalf("%+v", h)
	}
}

func TestObjections(t *testing.T) {
	brand := config.Brand{Name: "Acme"}
	res := []run.Result{
		graded(0, "p0", grade.Grade{Verdict: grade.Mentioned, Objections: []string{"Pricing unclear.", " pricing   UNCLEAR ", "macOS-only"}}, nil),
		graded(1, "p1", grade.Grade{Verdict: grade.Mentioned, Objections: []string{"pricing, unclear"}}, nil),
		graded(2, "p2", grade.Grade{Verdict: grade.Absent, Objections: []string{"pricing unclear"}}, nil), // doesn't name the brand
	}
	s := Build(brand, res)
	if len(s.Objections) != 2 {
		t.Fatalf("%+v", s.Objections)
	}
	o := s.Objections[0]
	if o.Text != "pricing unclear" || o.Answers != 2 || o.Prompt != 0 || o.PromptText != "p0" {
		t.Fatalf("%+v", o)
	}
	if s.Objections[1].Text != "macos only" {
		t.Fatalf("%+v", s.Objections[1])
	}
}

func TestObjectionsCountThroughClusters(t *testing.T) {
	brand := config.Brand{Name: "Acme"}
	res := []run.Result{
		graded(0, "p0", grade.Grade{Verdict: grade.Mentioned, Objections: []string{"Coverage unverified"}}, nil),
		graded(1, "p1", grade.Grade{Verdict: grade.Mentioned, Objections: []string{"coverage unverified", "Coverage of two assistants unverified"}}, nil), // one answer, counted once
		graded(2, "p2", grade.Grade{Verdict: grade.Mentioned, Objections: []string{"coverage of two assistants unverified", "no free tier"}}, nil),
	}
	clusters := map[string]string{"coverage of two assistants unverified": "Coverage unverified"}
	s := BuildWith(brand, res, Options{Objections: clusters})
	if len(s.Objections) != 2 || s.Objections[0].Text != "coverage unverified" || s.Objections[0].Answers != 3 || s.Objections[0].Wordings != 2 {
		t.Fatalf("%+v", s.Objections)
	}
	if o := s.Objections[1]; o.Text != "no free tier" || o.Answers != 1 || o.Wordings != 1 {
		t.Fatalf("%+v", o)
	}
	// Without the map, each wording is a row with one wording, as before.
	if s := Build(brand, res); len(s.Objections) != 3 || s.Objections[0].Wordings != 1 {
		t.Fatalf("%+v", s.Objections)
	}
}

func TestBranded(t *testing.T) {
	brand := config.Brand{Name: "Acme", Aliases: []string{"acme.io", "AC"}}
	for text, want := range map[string]bool{
		"Is Acme any good?":           true,
		"acme vs beta":                true,
		"what about ACME.IO pricing":  true,
		"is ac worth it":              true,
		"best tools like Acmeware":    false,
		"how do I track an account":   false, // "ac" only inside a word
		"the best visibility tracker": false,
	} {
		if got := Branded(brand, text); got != want {
			t.Errorf("%q: got %v", text, got)
		}
	}
	res := []run.Result{
		graded(0, "Is Acme any good?", grade.Grade{Verdict: grade.Recommended, TopPick: ptr("Acme")}, nil),
		graded(1, "best tracker", grade.Grade{Verdict: grade.Absent}, nil),
		graded(1, "best tracker", grade.Grade{Verdict: grade.Mentioned}, nil),
	}
	s := Build(brand, res)
	if !s.Prompts[0].Branded || s.Prompts[1].Branded || s.BrandedPrompts != 1 {
		t.Fatalf("%+v", s.Prompts)
	}
	if s.Overall.Visible.Hits != 2 || s.Overall.Visible.N != 3 || s.Unbranded.Visible.Hits != 1 || s.Unbranded.Visible.N != 2 || s.Unbranded.TopPick.Hits != 0 {
		t.Fatalf("overall %+v, unbranded %+v", s.Overall, s.Unbranded)
	}
}

func TestClassify(t *testing.T) {
	brand := config.Brand{Name: "Acme", Domains: []string{"acme.com", "github.com/acme"}}
	comps := []sets.Competitor{{Name: "Beta", Domains: []string{"beta.io", "github.com/beta-inc"}}}
	c := NewClassifier(brand, comps, map[string][]string{"ugc": {"forum.example.org"}, "editorial": {"medium.com"}, "review": {"wikipedia.org/wiki/comparison"}})
	for u, want := range map[string]string{
		"https://docs.acme.com/x":                       Own,
		"https://github.com/acme/cli":                   Own,
		"https://www.beta.io/pricing":                   Competitor,
		"https://github.com/beta-inc/beta":              Competitor,
		"https://www.reddit.com/r/x":                    UGC,
		"https://news.ycombinator.com/item?id=1":        UGC,
		"https://github.com/other/repo/issues/12":       UGC,
		"https://github.com/other/repo":                 Reference,
		"https://www.g2.com/products/acme":              Review,
		"https://www.gartner.com/reviews/market/x":      Review,
		"https://www.gartner.com/en/articles":           Editorial,
		"https://en.wikipedia.org/wiki/Acme":            Reference,
		"https://docs.python.org/3/":                    Reference,
		"https://www.nist.gov/x":                        Institutional,
		"https://www.ox.ac.uk/":                         Institutional,
		"https://www.gov.uk/guidance":                   Institutional,
		"https://who.int/":                              Institutional,
		"https://techcrunch.com/2026/01/01/x":           Editorial,
		"https://forum.example.org/t/1":                 UGC,       // override
		"https://sub.forum.example.org/t/1":             UGC,       // override covers subdomains
		"https://medium.com/@x/post":                    Editorial, // override beats the built-in
		"https://wikipedia.org/wiki/comparison/x":       Review,    // override, more specific
		"https://en.wikipedia.org/wiki/Something_else":  Reference,
		"https://www.capterra.com/p/1/acme/?utm=foo#hi": Review,
	} {
		if got, _ := c.Class(u); got != want {
			t.Errorf("%s: %s, want %s", u, got, want)
		}
	}
	if _, who := c.Class("https://beta.io"); who != "Beta" {
		t.Errorf("competitor = %q", who)
	}
}

func cite(urls ...string) *answer.Answer {
	a := &answer.Answer{}
	for _, u := range urls {
		a.Citations = append(a.Citations, answer.Link{URL: u})
	}
	return a
}

func TestSourceRatesAndBuckets(t *testing.T) {
	brand := config.Brand{Name: "Acme", Domains: []string{"acme.com"}}
	opt := Options{Competitors: []sets.Competitor{{Name: "Beta", Domains: []string{"beta.io"}}, {Name: "Gamma"}}}
	searched := func(a *answer.Answer, urls ...string) *answer.Answer {
		q := answer.Query{Query: "q"}
		for _, u := range urls {
			q.Results = append(q.Results, answer.Link{URL: u})
		}
		a.Searches = append(a.Searches, q)
		return a
	}
	res := []run.Result{
		// Names Beta and Gamma, never Acme: everything it cites is missing Acme.
		graded(0, "p", grade.Grade{Verdict: grade.Absent, Named: []string{"Beta", "Gamma"}},
			searched(cite("https://reddit.com/r/a", "https://reddit.com/r/b", "https://beta.io/x"), "https://reddit.com/r/a", "https://acme.com/docs")),
		graded(0, "p", grade.Grade{Verdict: grade.Absent, Named: []string{"Beta"}}, cite("https://reddit.com/r/a", "https://g2.com/beta")),
		// Acme and Beta.
		graded(0, "p", grade.Grade{Verdict: grade.Mentioned, Named: []string{"Acme", "Beta"}}, cite("https://techcrunch.com/x")),
		// Acme twice, Beta once on the same host: strong.
		graded(0, "p", grade.Grade{Verdict: grade.Mentioned, Named: []string{"Acme"}}, cite("https://techcrunch.com/y")),
		// Acme only: unique.
		graded(0, "p", grade.Grade{Verdict: grade.Mentioned, Named: []string{"Acme", "SomeLibrary"}}, cite("https://wikipedia.org/wiki/Acme", "https://acme.com/")),
		// No text kept: not part of the source rates.
		graded(0, "p", grade.Grade{Verdict: grade.Absent}, nil),
	}
	// A recall answer has no tools: it can't reach a page, so it doesn't dilute the rates.
	recall := graded(0, "p", grade.Grade{Verdict: grade.Absent}, &answer.Answer{Text: "from memory"})
	recall.Track = answer.Recall
	res = append(res, recall)
	s := BuildWith(brand, res, opt)
	if s.Answers != 5 {
		t.Fatalf("answers = %d", s.Answers)
	}
	share := 0.0
	for _, p := range s.Pages {
		share += p.CitationShare
	}
	hostShare := 0.0
	host := map[string]HostUse{}
	for _, h := range s.Hosts {
		hostShare += h.CitationShare
		host[h.Host] = h
	}
	classShare := 0.0
	for _, c := range s.Classes {
		classShare += c.CitationShare
	}
	if !near(share, 1) || !near(hostShare, 1) || !near(classShare, 1) {
		t.Fatalf("citation shares sum to %v (pages), %v (hosts), %v (classes)", share, hostShare, classShare)
	}
	var ra Page
	for _, p := range s.Pages {
		if p.URL == "https://reddit.com/r/a" {
			ra = p
		}
	}
	// Cited by 2 of 5 answers, surfaced by 1 (which cited it too), 2 of the run's 9 citations.
	if ra.Cited != 2 || ra.Surfaced != 1 || !near(ra.CitedRate, 0.4) || !near(ra.RetrievedRate, 0.2) || !near(ra.CitationShare, 2.0/9) || !near(ra.CitedWhenSurfaced, 1) || ra.Class != UGC {
		t.Fatalf("reddit page: %+v", ra)
	}
	if r := host["reddit.com"]; r.Cited != 2 || r.Pages != 2 || r.Bucket != Missing || len(r.Competitors) != 2 || r.Competitors[0] != (Count{"Beta", 2}) {
		t.Fatalf("reddit: %+v", r)
	}
	if h := host["techcrunch.com"]; h.Bucket != Strong || h.BrandNamed != 2 {
		t.Fatalf("techcrunch: %+v", h)
	}
	if h := host["wikipedia.org"]; h.Bucket != Unique || len(h.Competitors) != 0 { // SomeLibrary isn't a competitor
		t.Fatalf("wikipedia: %+v", h)
	}
	if h := host["acme.com"]; h.Class != Own || h.Bucket != "" {
		t.Fatalf("acme.com: %+v", h)
	}
	if h := host["beta.io"]; h.Class != Competitor || h.CompetitorSite != "Beta" || h.Bucket != Missing {
		t.Fatalf("beta.io: %+v", h)
	}
	// Outreach: missing hosts that aren't a competitor's, by competitors x citations.
	if len(s.Outreach) != 2 || s.Outreach[0].Host != "reddit.com" || s.Outreach[0].Score != 4 || s.Outreach[0].Page != "https://reddit.com/r/a" || s.Outreach[1].Host != "g2.com" || s.Outreach[1].Score != 1 {
		t.Fatalf("outreach: %+v", s.Outreach)
	}
	if len(s.Uncited) != 1 || s.Uncited[0].URL != "https://acme.com/docs" {
		t.Fatalf("found, not cited: %+v", s.Uncited)
	}
	for _, c := range s.Classes {
		if c.Class == UGC && (c.Cited != 2 || c.Surfaced != 1 || c.Hosts != 1) {
			t.Errorf("ugc: %+v", c)
		}
	}
}

// Shared: the brand named, but not more often than a competitor.
func TestSharedBucket(t *testing.T) {
	brand := config.Brand{Name: "Acme"}
	res := []run.Result{
		graded(0, "p", grade.Grade{Verdict: grade.Mentioned, Named: []string{"Acme", "Beta"}}, cite("https://x.com/a")),
		graded(0, "p", grade.Grade{Verdict: grade.Absent, Named: []string{"Beta"}}, cite("https://x.com/b")),
	}
	s := Build(brand, res) // no competitors configured: every other product counts
	if h := s.Hosts[0]; h.Bucket != Shared || h.Class != UGC {
		t.Fatalf("%+v", h)
	}
}

// Places are all on the list's scale: a grade position that disagrees with the list doesn't give
// an answer two firsts. The position is used only when the list left the brand out.
func TestPositionsShareOneScale(t *testing.T) {
	brand := config.Brand{Name: "Acme", Aliases: []string{"acme.io"}}
	res := []run.Result{
		graded(0, "p", grade.Grade{Verdict: grade.Mentioned, Position: ptr(1), Named: []string{"X", "acme.io", "Y"}}, nil),
		graded(0, "p", grade.Grade{Verdict: grade.Mentioned, Position: ptr(3), Named: []string{"X", "Y"}}, nil),
	}
	s := Build(brand, res)
	a, x := voiceOf(s, "Acme"), voiceOf(s, "X")
	if !near(a.AvgPosition, 2.5) || a.NamedFirst.Hits != 0 || a.Top3.Hits != 2 { // 2 from the list, 3 from the grade
		t.Errorf("acme: %+v", a)
	}
	if x.NamedFirst.Hits != 2 {
		t.Errorf("x: %+v", x)
	}
	for _, h := range s.HeadToHead {
		if h.Name == "X" && (h.Both != 2 || h.CompetitorAhead != 2 || h.BrandAhead != 0) {
			t.Errorf("head to head: %+v", h)
		}
		if h.Name == "Y" && (h.Both != 2 || h.BrandAhead != 1 || h.CompetitorAhead != 1) {
			t.Errorf("head to head: %+v", h)
		}
	}
}
