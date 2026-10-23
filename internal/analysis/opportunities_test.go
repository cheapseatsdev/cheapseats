package analysis

import (
	"context"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/report"
)

func TestOpportunitiesText(t *testing.T) {
	if got := opportunitiesText(context.Background(), "Acme"); got != "" {
		t.Fatalf("no opportunities: %q", got)
	}
	opps := []report.Opportunity{
		{Class: "review", Citations: 9, CitationShare: 0.38, Answers: 9, CompetitorPresence: 8.0 / 9, Gap: 0.38, Hosts: []string{"g2.com", "capterra.com"}, Prompts: []int{1, 4, 6}},
		{Class: "review", Type: "listicle", Citations: 7, CitationShare: 0.30, Answers: 7, CompetitorPresence: 1, Competitors: []report.Count{{Name: "Gauge", N: 6}, {Name: "Profound", N: 3}}, Gap: 0.30, Hosts: []string{"g2.com"}, Prompts: []int{1, 4}},
		{Class: "own", Owned: true, Citations: 5, CitationShare: 0.2, Answers: 5, BrandPresence: 0.6, Gap: 0.08, Hosts: []string{"acme.example"}, Prompts: []int{0}},
		{Class: "ugc", Citations: 3, CitationShare: 0.1, Answers: 3, BrandPresence: 1, Gap: 0, Hosts: []string{"reddit.com"}, Prompts: []int{2}},
	}
	got := opportunitiesText(WithOpportunities(context.Background(), opps), "Acme")
	for _, want := range []string{
		"- review listicle pages (g2.com): 30% of the citations, 7 answers on prompts 2, 5; none name Acme; 7 name a competitor (Gauge 6, Profound 3).",
		"- Acme's own pages (acme.example): 20% of the citations, 5 answers on prompts 1; 3 of 5 name Acme; 0 name a competitor.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- review pages") {
		t.Fatalf("the class row should give way to its page type:\n%s", got)
	}
	if strings.Contains(got, "reddit.com") {
		t.Fatalf("a row with no gap was listed:\n%s", got)
	}
}
