package report

import (
	"testing"

	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/grade"
	"github.com/metcalfc/popcorn/internal/run"
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
	brand := config.Brand{Name: "Popcorn", Domains: []string{"popcorn.example"}}
	res := []run.Result{
		{PromptIdx: 0, Prompt: "p0", Answerer: "a", Track: answer.Search, Status: "done",
			Grade: &grade.Grade{Verdict: grade.Recommended, TopPick: ptr("Popcorn"), Named: []string{"Popcorn", "Coder"}},
			Answer: &answer.Answer{Citations: []answer.Link{{URL: "https://docs.popcorn.example/x/"}},
				Searches: []answer.Query{{Query: "q", Results: []answer.Link{{URL: "https://docs.popcorn.example/x"}, {URL: "https://coder.com"}}}}}},
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
	b := config.Brand{Domains: []string{"popcorn.example", "github.com/metcalfc/popcorn"}}
	for u, want := range map[string]bool{
		"https://popcorn.example":                    true,
		"https://docs.popcorn.example/cli/":          true,
		"https://www.popcorn.example/why":            true,
		"https://github.com/metcalfc/popcorn/skills": true,
		"https://github.com/metcalfc/popcornx":       false,
		"https://notpopcorn.example/":                false,
		"https://example.com/?ref=popcorn.example":   false,
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
