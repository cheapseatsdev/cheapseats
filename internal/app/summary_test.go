package app

import (
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// A past run is summarized with the brand as it saw it (who the brand is shaped its grading) and
// today's sites (which pages count as the brand's own), so every view agrees about it.
func TestRunBrandKeepsIdentityAndTakesTodaysSites(t *testing.T) {
	a := &App{Cfg: &config.Config{Brand: config.Brand{Name: "Renamed", Sites: []config.Site{{URL: "docs.acme.example", Kind: "docs"}}}}}
	r := &run.Run{Snapshot: run.Snapshot{Brand: config.Brand{Name: "Acme", Aliases: []string{"acme.example"}}}}
	b := a.RunBrand(r)
	if b.Name != "Acme" || len(b.Aliases) != 1 || len(b.Sites) != 1 {
		t.Fatalf("brand: %+v", b)
	}
	res := []run.Result{{Status: "done", Track: answer.Search,
		Grade:  &grade.Grade{Verdict: grade.Recommended, TopPick: ptr("Acme"), Named: []string{"Acme", "Other"}},
		Answer: &answer.Answer{Citations: []answer.Link{{URL: "https://docs.acme.example/guide"}}}}}
	s := a.Summary(r, res)
	if s.BrandRank() != 1 || s.Overall.TopPick.Hits != 1 {
		t.Errorf("the run's own brand name should be the brand: rank %d, top picks %d", s.BrandRank(), s.Overall.TopPick.Hits)
	}
	if len(s.Pages) != 1 || !s.Pages[0].FirstParty || s.Pages[0].SiteKind != "docs" {
		t.Errorf("today's sites should classify the run's pages: %+v", s.Pages)
	}
}

func ptr(s string) *string { return &s }
