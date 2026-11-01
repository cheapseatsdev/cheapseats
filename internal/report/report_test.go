package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

func ptr[T any](v T) *T { return &v }

func TestWilson(t *testing.T) {
	r := NewRate(0, 0)
	if r.P != 0 || r.Hi != 0 {
		t.Fatal(r)
	}
	r = NewRate(5, 10)
	if r.P != 0.5 || r.Lo > 0.25 || r.Hi < 0.75 || r.Lo < 0.2 {
		t.Fatalf("%+v", r)
	}
	r = NewRate(10, 10)
	if r.Hi != 1 || r.Lo < 0.6 {
		t.Fatalf("%+v", r)
	}
}

func TestBuild(t *testing.T) {
	brand := config.Brand{Name: "Cheap Seats", Domains: []string{"cheapseats.example"}}
	res := []run.Result{
		{PromptIdx: 0, Prompt: "p0", Answerer: "a", Track: answer.Search, Status: "done",
			Grade: &grade.Grade{Verdict: grade.Recommended, TopPick: ptr("Cheap Seats"), Named: []string{"Cheap Seats", "Coder"}},
			Answer: &answer.Answer{Citations: []answer.Link{{URL: "https://docs.cheapseats.example/x/"}},
				Searches: []answer.Query{{Query: "q", Results: []answer.Link{{URL: "https://docs.cheapseats.example/x"}, {URL: "https://coder.com"}}}}}},
		{PromptIdx: 0, Prompt: "p0", Answerer: "a", Track: answer.Recall, Status: "done",
			Grade: &grade.Grade{Verdict: grade.Absent, TopPick: ptr("Coder"), Named: []string{"Coder", "coder"}}},
		{PromptIdx: 1, Prompt: "p1", Answerer: "a", Track: answer.Search, Status: "error"},
	}
	s := Build(brand, res)
	if s.Done != 2 || s.Errors != 1 {
		t.Fatalf("%+v", s)
	}
	if s.Overall.TopPick.Hits != 1 || s.Overall.Visible.Hits != 1 {
		t.Fatalf("%+v", s.Overall)
	}
	if s.Voice[0].Name != "Coder" || s.Voice[0].Answers != 2 {
		t.Fatalf("voice = %+v", s.Voice)
	}
	if s.BrandRank() != 2 {
		t.Fatalf("rank = %d", s.BrandRank())
	}
	if !s.Pages[0].FirstParty || s.Pages[0].Cited != 1 || s.Pages[0].Surfaced != 1 {
		t.Fatalf("pages = %+v", s.Pages)
	}
	if !s.Prompts[0].FirstHand || s.Prompts[1].FirstHand {
		t.Fatalf("prompts = %+v", s.Prompts)
	}
}

func TestFirstParty(t *testing.T) {
	b := config.Brand{Domains: []string{"cheapseats.example", "github.com/cheapseatsdev/cheapseats"}}
	for u, want := range map[string]bool{
		"https://cheapseats.example":                         true,
		"https://docs.cheapseats.example/cli/":               true,
		"https://www.cheapseats.example/why":                 true,
		"https://github.com/cheapseatsdev/cheapseats/skills": true,
		"https://github.com/cheapseatsdev/cheapseatsx":       false,
		"https://notcheapseats.example/":                     false,
		"https://example.com/?ref=cheapseats.example":        false,
	} {
		if got := b.FirstParty(u); got != want {
			t.Errorf("%s: got %v", u, got)
		}
	}
}

func TestSitesIncludeOnesNoAnswerReached(t *testing.T) {
	brand := config.Brand{Name: "Acme", Sites: []config.Site{{URL: "acme.com", Kind: "marketing"}, {URL: "docs.acme.com", Kind: "docs"}, {URL: "blog.acme.com", Kind: "blog"}}}
	g := &grade.Grade{Verdict: grade.Mentioned}
	res := []run.Result{
		{PromptIdx: 0, Status: "done", Grade: g, Answer: &answer.Answer{Citations: []answer.Link{{URL: "https://docs.acme.com/a"}, {URL: "https://docs.acme.com/b"}},
			Searches: []answer.Query{{Results: []answer.Link{{URL: "https://acme.com/"}}}}}},
		{PromptIdx: 1, Status: "done", Grade: g, Answer: &answer.Answer{Citations: []answer.Link{{URL: "https://docs.acme.com/a"}}}},
	}
	s := Build(brand, res)
	if len(s.Sites) != 3 {
		t.Fatalf("sites: %+v", s.Sites)
	}
	docs, blog := s.Sites[1], s.Sites[2]
	if docs.URL != "docs.acme.com" || docs.Cited != 2 || docs.Pages != 2 || docs.Prompts != 2 {
		t.Errorf("docs: %+v", docs)
	}
	if blog.Cited+blog.Surfaced+blog.Pages+blog.Prompts != 0 {
		t.Errorf("blog should be all zeros: %+v", blog)
	}
	for _, p := range s.Pages {
		if p.URL == "https://docs.acme.com/a" && (p.SiteKind != "docs" || !p.FirstParty) {
			t.Errorf("page: %+v", p)
		}
	}
}

// An own page's credit counts each unbranded citing answer once: named by its verdict, recommended
// only of the choice answers. A branded prompt names the brand trivially, so it is left out, and a
// third-party page cited by the same answers gets nothing.
func TestOwnPageCredit(t *testing.T) {
	brand := config.Brand{Name: "Acme", Domains: []string{"acme.com"}}
	ownCreditCites := func(urls ...string) *answer.Answer {
		a := &answer.Answer{}
		for _, u := range urls {
			a.Citations = append(a.Citations, answer.Link{URL: u})
		}
		return a
	}
	const own, other = "https://acme.com/pricing", "https://review.example/best"
	res := []run.Result{
		{PromptIdx: 0, Prompt: "best tool for x", Track: answer.Search, Status: "done",
			Grade: &grade.Grade{Verdict: grade.Absent}, Answer: ownCreditCites(own, other)},
		{PromptIdx: 1, Prompt: "which tool for y", Track: answer.Search, Status: "done",
			Grade: &grade.Grade{Verdict: grade.Recommended}, Answer: ownCreditCites(own, own, other)},
		{PromptIdx: 2, Prompt: "how does z work", Info: true, Track: answer.Search, Status: "done",
			Grade: &grade.Grade{Verdict: grade.Mentioned}, Answer: ownCreditCites(own, other)},
		{PromptIdx: 3, Prompt: "is Acme good for x", Track: answer.Search, Status: "done",
			Grade: &grade.Grade{Verdict: grade.Recommended}, Answer: ownCreditCites(own, other)},
	}
	s := Build(brand, res)
	var ownPage, otherPage *Page
	for i := range s.Pages {
		switch s.Pages[i].URL {
		case own:
			ownPage = &s.Pages[i]
		case other:
			otherPage = &s.Pages[i]
		}
	}
	if ownPage == nil || otherPage == nil {
		t.Fatalf("pages = %+v", s.Pages)
	}
	if ownPage.Cited != 4 || ownPage.CitedUnbranded != 3 || ownPage.CitedNamed != 2 || ownPage.CitedChoice != 2 || ownPage.CitedRecommended != 1 {
		t.Errorf("own page: cited %d, unbranded %d, named %d, choice %d, recommended %d; want 4, 3, 2, 2, 1",
			ownPage.Cited, ownPage.CitedUnbranded, ownPage.CitedNamed, ownPage.CitedChoice, ownPage.CitedRecommended)
	}
	if otherPage.CitedUnbranded+otherPage.CitedNamed+otherPage.CitedChoice+otherPage.CitedRecommended != 0 {
		t.Errorf("third-party page should get no credit counts: %+v", otherPage)
	}
	// Cited and never credited is the case this is for: get_run must say 0, not drop the key.
	js, _ := json.Marshal(Page{FirstParty: true, CitedUnbranded: 3, CitedChoice: 2})
	for _, w := range []string{`"cited_named":0`, `"cited_recommended":0`} {
		if !strings.Contains(string(js), w) {
			t.Errorf("an uncredited own page's JSON lacks %s: %s", w, js)
		}
	}
}

// The brand's share of voice follows its verdict, so its rank and named rate can't disagree:
// a name the grader listed for an "absent" answer doesn't count, and a "mentioned" answer
// counts even if the list missed it.
func TestBrandVoiceFollowsTheVerdict(t *testing.T) {
	brand := config.Brand{Name: "Acme", Aliases: []string{"acme.io"}}
	res := []run.Result{
		{Status: "done", Grade: &grade.Grade{Verdict: grade.Absent, Named: []string{"Acme", "Other"}}},
		{Status: "done", Grade: &grade.Grade{Verdict: grade.Mentioned, Named: []string{"Other"}}},
		{Status: "done", Grade: &grade.Grade{Verdict: grade.Recommended, Named: []string{"acme.io", "Other"}}},
	}
	s := Build(brand, res)
	for _, v := range s.Voice {
		if v.IsBrand && v.Answers != 2 {
			t.Fatalf("brand named in %d answers, want 2 (the non-absent ones): %+v", v.Answers, s.Voice)
		}
	}
	if s.Overall.Visible.Hits != 2 {
		t.Fatalf("named rate: %+v", s.Overall.Visible)
	}
}

// Aliases fold product names the grader wrote differently onto one row, for names and top picks.
func TestAliasesFoldTheBoard(t *testing.T) {
	brand := config.Brand{Name: "Acme"}
	a, b := "Scrunch AI", "Scrunch"
	res := []run.Result{
		{Status: "done", Track: answer.Search, Grade: &grade.Grade{Verdict: grade.Absent, Named: []string{"Scrunch AI", "Profound"}, TopPick: &a}},
		{Status: "done", Track: answer.Recall, Grade: &grade.Grade{Verdict: grade.Absent, Named: []string{"Scrunch", "tryprofound"}, TopPick: &b}},
	}
	plain := BuildWith(brand, res, Options{})
	if len(plain.Voice) != 4 {
		t.Fatalf("unfolded, four rows: %v", plain.VoiceNames())
	}
	folded := BuildWith(brand, res, Options{Aliases: map[string]string{"scrunch": "Scrunch AI", "tryprofound": "Profound"}})
	if len(folded.Voice) != 2 {
		t.Fatalf("folded, two rows: %v", folded.VoiceNames())
	}
	for _, v := range folded.Voice {
		switch v.Name {
		case "Scrunch AI":
			if v.Answers != 2 || v.TopPick != 2 {
				t.Errorf("Scrunch AI: %+v", v)
			}
		case "Profound":
			if v.Answers != 2 {
				t.Errorf("Profound: %+v", v)
			}
		default:
			t.Errorf("unexpected row %q", v.Name)
		}
	}
}
