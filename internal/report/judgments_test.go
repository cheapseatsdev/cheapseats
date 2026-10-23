package report

import (
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// Options.PageTypes fills in the type where the rules say other and nowhere else, and only with
// a type the report knows; the page carries who typed it.
func TestOptionsPageTypesFillOnlyTheDefault(t *testing.T) {
	g := grade.Grade{Verdict: grade.Mentioned}
	res := []run.Result{graded(0, "p", g, &answer.Answer{Citations: []answer.Link{
		{URL: "https://example.com/some/thing/", Title: "Seven tools we tried"},
		{URL: "https://example.com/blog/post"},
		{URL: "https://example.com/x"},
	}})}
	brand := config.Brand{Name: "Acme"}
	s := BuildWith(brand, res, Options{PageTypes: map[string]string{
		"https://example.com/some/thing": Listicle, // the rules say other: Jev's type
		"https://example.com/blog/post":  Listicle, // the rules say article: they win
		"https://example.com/x":          "bogus",  // not a type: ignored
	}})
	want := map[string][2]string{
		"https://example.com/some/thing": {Listicle, "jev"},
		"https://example.com/blog/post":  {Article, ""},
		"https://example.com/x":          {OtherPage, ""},
	}
	for _, p := range s.Pages {
		if w := want[p.URL]; p.Type != w[0] || p.TypedBy != w[1] {
			t.Errorf("%s: %s by %q, want %s by %q", p.URL, p.Type, p.TypedBy, w[0], w[1])
		}
	}
	by := map[string]TypeUse{}
	for _, u := range s.PageTypes {
		by[u.Type] = u
	}
	if by[Listicle].Pages != 1 || by[OtherPage].Pages != 1 || by[Article].Pages != 1 {
		t.Errorf("type counts: %+v", s.PageTypes)
	}
	// Without the option, every one of them is as the rules say.
	for _, p := range Build(brand, res).Pages {
		if p.TypedBy != "" || p.URL == "https://example.com/some/thing" && p.Type != OtherPage {
			t.Errorf("rules alone: %+v", p)
		}
	}
	for _, m := range []map[string]string{PageTypeDefinitions, ClassDefinitions} {
		for k, v := range m {
			if v == "" {
				t.Errorf("%s has no definition", k)
			}
		}
	}
	if len(PageTypeDefinitions) != len(PageTypeOrder) || len(ClassDefinitions) != len(ClassOrder)-2 {
		t.Error("a definition for every type, and for every class but own and competitor")
	}
}
