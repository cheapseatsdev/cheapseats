package report

import (
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/opinion"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// One objection in three search answers citing two pages, the brand's own pricing page in all
// three and a review site in one, and in one recall answer; an answer not naming the brand raises
// it too and must not count.
func TestObjectionSources(t *testing.T) {
	brand := config.Brand{Name: "Acme", Sites: []config.Site{{URL: "acme.com", Kind: "marketing"}}}
	const own, review = "https://acme.com/pricing", "https://www.g2.com/products/acme/reviews"
	answerCiting := func(urls ...string) *answer.Answer {
		a := &answer.Answer{Text: "x"}
		for _, u := range urls {
			a.Citations = append(a.Citations, answer.Link{URL: u})
		}
		return a
	}
	named := func(objections, praise []string) *grade.Grade {
		return &grade.Grade{Verdict: grade.Mentioned, Named: []string{"Acme"}, Objections: objections, Praise: praise}
	}
	res := []run.Result{
		{PromptIdx: 0, Prompt: "p0", Track: answer.Search, Status: run.ResultDone, Grade: named([]string{"Pricing unclear", "pricing unclear."}, []string{"Open source"}), Answer: answerCiting(own, review, own)},
		{PromptIdx: 1, Prompt: "p1", Track: answer.Search, Status: run.ResultDone, Grade: named([]string{"pricing unclear"}, nil), Answer: answerCiting(own)},
		{PromptIdx: 2, Prompt: "p2", Track: answer.Search, Status: run.ResultDone, Grade: named([]string{"Pricing: unclear"}, nil), Answer: answerCiting(own)},
		{PromptIdx: 0, Prompt: "p0", Track: answer.Recall, Status: run.ResultDone, Grade: named([]string{"pricing unclear"}, []string{"open source"}), Answer: &answer.Answer{Text: "x"}},
		{PromptIdx: 1, Prompt: "p1", Track: answer.Search, Status: run.ResultDone, Grade: &grade.Grade{Verdict: grade.Absent, Objections: []string{"pricing unclear"}}, Answer: answerCiting(review)},
	}
	s := Build(brand, res)
	if len(s.Objections) != 1 {
		t.Fatalf("objections = %+v", s.Objections)
	}
	o := s.Objections[0]
	if o.Text != "pricing unclear" || o.Answers != 4 || o.Recall != 1 || len(o.Sources) != 2 {
		t.Fatalf("objection = %+v", o)
	}
	types := map[string]string{}
	for _, p := range s.Pages {
		types[p.URL] = p.Type
	}
	ownSrc, reviewSrc := o.Sources[0], o.Sources[1]
	if ownSrc.Host != "acme.com" || !ownSrc.Owned || ownSrc.Answers != 3 || ownSrc.Type != types[ownSrc.URL] || ownSrc.Type == "" {
		t.Errorf("owned page = %+v", ownSrc)
	}
	if reviewSrc.Host != "g2.com" || reviewSrc.Owned || reviewSrc.Answers != 1 || reviewSrc.Type != types[reviewSrc.URL] {
		t.Errorf("earned page = %+v (the answer not naming the brand doesn't count)", reviewSrc)
	}

	// The sentiment's praise and objections get the same pages, keyed the same way.
	var grades []*grade.Grade
	for _, r := range res {
		grades = append(grades, r.Grade)
	}
	st := opinion.Summarize(grades)
	s.Trace(&st)
	if p := st.Praise[0]; p.Behind == nil || p.Recall != 1 || len(p.Sources) != 2 || p.Sources[0].Answers != 1 {
		t.Errorf("praise = %+v", p)
	}
	if ob := st.Objections[0]; ob.Behind == nil || ob.Recall != 1 || len(ob.Sources) != 2 || ob.Sources[0] != ownSrc {
		t.Errorf("sentiment objection = %+v", ob)
	}
}

// A report keeps the MaxSources pages most of the answers raising it cited.
func TestObjectionSourcesKeepsTheTopFew(t *testing.T) {
	g := &grade.Grade{Verdict: grade.Mentioned, Objections: []string{"slow"}}
	var res []run.Result
	for i, urls := range [][]string{{"https://a.example/1", "https://b.example/1", "https://c.example/1", "https://d.example/1"}, {"https://d.example/1"}, {"https://c.example/1"}} {
		a := &answer.Answer{}
		for _, u := range urls {
			a.Citations = append(a.Citations, answer.Link{URL: u})
		}
		res = append(res, run.Result{PromptIdx: i, Track: answer.Search, Status: run.ResultDone, Grade: g, Answer: a})
	}
	src := Build(config.Brand{Name: "Acme"}, res).Objections[0].Sources
	if len(src) != MaxSources || src[0].Host != "c.example" || src[1].Host != "d.example" || src[2].Host != "a.example" {
		t.Fatalf("sources = %+v", src)
	}
}
