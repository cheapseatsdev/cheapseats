package report

import (
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// citing is one graded search answer with the citations given.
func citing(prompt int, g grade.Grade, urls ...string) run.Result {
	var links []answer.Link
	for _, u := range urls {
		links = append(links, answer.Link{URL: u})
	}
	return run.Result{PromptIdx: prompt, Prompt: "p", Answerer: "a", Track: answer.Search, Status: "done", Grade: &g, Answer: &answer.Answer{Citations: links}}
}

func TestOpportunities(t *testing.T) {
	brand := config.Brand{Name: "Acme", Domains: []string{"acme.example"}}
	absent := grade.Grade{Verdict: grade.Absent, Named: []string{"Gauge"}}
	mentioned := grade.Grade{Verdict: grade.Mentioned, Named: []string{"Gauge", "Acme"}}
	recommended := grade.Grade{Verdict: grade.Recommended, Named: []string{"Acme"}}
	res := []run.Result{
		// Four editorial listicles, on three hosts, in four answers; one answer names the brand.
		citing(0, absent, "https://www.techradar.com/best/widgets"),
		citing(1, absent, "https://www.techradar.com/best/widgets"),
		citing(2, absent, "https://www.pcmag.com/picks/the-best-widgets"),
		citing(3, mentioned, "https://zapier.com/blog/best-widget-apps/"),
		// Three of the brand's own pages: two docs, one product. Only the class row has enough.
		citing(0, recommended, "https://acme.example/docs/intro"),
		citing(1, recommended, "https://acme.example/docs/intro"),
		citing(2, recommended, "https://acme.example/pricing"),
		// One editorial comparison: too few to list on its own, but it counts toward editorial.
		citing(3, absent, "https://www.pcmag.com/comparisons/acme-vs-gauge"),
	}
	s := BuildWith(brand, res, Options{Competitors: []sets.Competitor{{Name: "Gauge"}}})
	if len(s.Opportunities) != 3 {
		t.Fatalf("want 3 rows (editorial, editorial listicle, own), got %+v", s.Opportunities)
	}
	// Editorial as a whole: 5 of 8 citations, 5 answers, 1 names the brand, all 5 name Gauge.
	o := s.Opportunities[0]
	if o.Class != Editorial || o.Type != "" || o.Owned || o.Citations != 5 || o.Answers != 5 {
		t.Fatalf("row 0 = %+v", o)
	}
	if !near(o.CitationShare, 5.0/8) || !near(o.BrandPresence, 1.0/5) || !near(o.CompetitorPresence, 1) || !near(o.Gap, 5.0/8*4/5) {
		t.Fatalf("row 0 rates = %+v", o)
	}
	if len(o.Competitors) != 1 || o.Competitors[0] != (Count{"Gauge", 5}) {
		t.Fatalf("row 0 competitors = %+v", o.Competitors)
	}
	// The listicles within it: pcmag and techradar tie on citations, so hosts sort by name.
	o = s.Opportunities[1]
	if o.Class != Editorial || o.Type != Listicle || o.Citations != 4 || o.Answers != 4 || !near(o.BrandPresence, 0.25) {
		t.Fatalf("row 1 = %+v", o)
	}
	if len(o.Hosts) != 3 || o.Hosts[0] != "techradar.com" || o.Hosts[1] != "pcmag.com" || o.Hosts[2] != "zapier.com" {
		t.Fatalf("row 1 hosts = %v", o.Hosts)
	}
	if len(o.Prompts) != 4 || o.Prompts[0] != 0 || o.Prompts[3] != 3 {
		t.Fatalf("row 1 prompts = %v", o.Prompts)
	}
	// The brand's own pages: every citing answer names it, so no gap, and last.
	o = s.Opportunities[2]
	if o.Class != Own || o.Type != "" || !o.Owned || o.Citations != 3 || !near(o.BrandPresence, 1) || o.Gap != 0 {
		t.Fatalf("row 2 = %+v", o)
	}
	for _, o := range s.Opportunities {
		if o.Type == Comparison || (o.Class == Own && o.Type != "") {
			t.Fatalf("a cell under %d citations was listed: %+v", MinOpportunityCitations, o)
		}
	}
}

func TestOpportunitiesEmpty(t *testing.T) {
	s := Build(config.Brand{Name: "Acme"}, nil)
	if s.Opportunities == nil || len(s.Opportunities) != 0 {
		t.Fatalf("want an empty list, got %#v", s.Opportunities)
	}
}
