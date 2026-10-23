package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// The page-types pass asks about pages the rules call other and hosts the lists call editorial,
// once each; the report reads the answers at today's threshold under the config's [sources].
func TestClassifyPagesTypesTheDefaultsAndClassesTheirHosts(t *testing.T) {
	a, id := providersApp(t)
	ctx := context.Background()
	// Prompt 1's search answer cites a page the rules can't type on a host the lists don't know,
	// and a blog post on another; its search also returned a review page the config places, and
	// an untitled page.
	listicle := answer.Link{URL: "https://example.org/some/thing", Title: "The 7 tools we tried for tracking AI answers"}
	ans := answer.Answer{
		Text: "Scrunch AI is a strong pick. Profound is the enterprise option.",
		Citations: []answer.Link{listicle,
			{URL: "https://blog.example.net/blog/why-we-switched/", Title: "Why we switched"}},
		Searches: []answer.Query{{Query: "q", Completed: true, Results: []answer.Link{listicle,
			{URL: "https://reviews.example.com/acme", Title: "Acme reviews"},
			{URL: "https://example.org/untitled"}}}},
	}
	aj, _ := json.Marshal(ans)
	if _, err := a.DB.Exec(`UPDATE results SET answer=? WHERE run_id=? AND prompt_idx=0 AND track='search'`, string(aj), id); err != nil {
		t.Fatal(err)
	}
	// The config places the review site, and, for the first pass, the blog host.
	a.Cfg.Sources = map[string][]string{"review": {"reviews.example.com"}, "editorial": {"blog.example.net"}}

	var asked []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req)
		answers := map[string]any{}
		for q := range req["questions"].(map[string]any) {
			if q == "class" {
				answers[q] = map[string]any{"type": "choice", "choice": "ugc", "confidence": 0.6, "probabilities": map[string]float64{"ugc": 0.8, "editorial": 0.15, "review": 0.05}}
			} else {
				answers[q] = map[string]any{"type": "choice", "choice": "listicle", "confidence": 0.7, "probabilities": map[string]float64{"listicle": 0.85, "article": 0.1, "other": 0.05}}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": answers, "usage": map[string]any{"input_tokens": 400, "output_tokens": 5}})
	}))
	defer srv.Close()
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, srv.URL)

	if p, ok := PassByCommand("classify"); !ok || p.Name != "page-types" || p.Order != 40 || p.Provider != config.TypeSafeProvider || !p.On(a.Cfg) {
		t.Fatalf("the pass: %+v %v", p, ok)
	}
	r, _ := a.Runs.Get(ctx, id)
	res, _ := a.Runs.Results(ctx, id)
	summary := func() report.Summary { return a.Summary(r, res) }
	before := summary()
	if p := pageOf(before, listicle.URL); p.Type != report.OtherPage || p.TypedBy != "" {
		t.Fatalf("by rule the page is other: %+v", p)
	}
	if h := hostOfPage(before, "example.org"); h.Class != report.Editorial {
		t.Fatalf("by rule the host is editorial: %+v", h)
	}
	otherBefore, listicleBefore := typeUse(before, report.OtherPage).Pages, typeUse(before, report.Listicle).Pages
	if classUse(before, report.Editorial).Hosts != 2 {
		t.Fatalf("two editorial hosts before: %+v", before.Classes)
	}

	n, err := a.ClassifyPages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// One page: the titled one the rules call other. Not the blog post (article by rule), the
	// review page (review by title), or the untitled page. One host: example.org. Not the blog
	// host or the review site (both in [sources]) and not the brand's.
	if n != 2 || len(asked) != 2 {
		t.Fatalf("asked %d in %d requests: %v", n, len(asked), asked)
	}
	st := asked[0]["state"].(map[string]any)
	if st["url"] != listicle.URL || st["title"] != listicle.Title || st["category"] != "An AI visibility tracker." {
		t.Errorf("the page question's state: %v", st)
	}
	q := asked[0]["questions"].(map[string]any)["type"].(map[string]any)
	crit := q["criteria"].(map[string]any)
	if q["type"] != "choice" || len(crit) != len(report.PageTypeOrder) || crit["listicle"] != report.PageTypeDefinitions[report.Listicle] {
		t.Errorf("the page question offers every type with its definition: %v", q)
	}
	st = asked[1]["state"].(map[string]any)
	pages := st["pages"].([]any)
	if st["host"] != "example.org" || st["category"] != "An AI visibility tracker." || len(pages) != 2 || pages[0].(map[string]any)["title"] != listicle.Title {
		t.Errorf("the host question's state carries the host and its pages: %v", st)
	}
	q = asked[1]["questions"].(map[string]any)["class"].(map[string]any)
	crit = q["criteria"].(map[string]any)
	if len(crit) != 5 || crit["ugc"] != report.ClassDefinitions[report.UGC] || crit["own"] != nil {
		t.Errorf("the host question offers the third-party classes: %v", q)
	}

	after := summary()
	if p := pageOf(after, listicle.URL); p.Type != report.Listicle || p.TypedBy != "jev" {
		t.Errorf("after the pass the page is a listicle, marked as Jev's: %+v", p)
	}
	if typeUse(after, report.OtherPage).Pages != otherBefore-1 || typeUse(after, report.Listicle).Pages != listicleBefore+1 {
		t.Errorf("the type counts move from other to listicle: %+v", after.PageTypes)
	}
	if hostOfPage(after, "example.org").Class != report.UGC || hostOfPage(after, "blog.example.net").Class != report.Editorial {
		t.Errorf("example.org is ugc, the configured blog host stays editorial: %+v", after.Hosts)
	}
	if classUse(after, report.UGC).Hosts != 1 || classUse(after, report.Editorial).Hosts != 1 {
		t.Errorf("the class counts move from editorial to ugc: %+v", after.Classes)
	}
	if js, _ := a.RunPageJudgments(ctx, id); len(js) != 2 || js[0].Kind != "page_type" || js[0].Confidence != 0.85 || !js[0].Applies || js[1].Key != "example.org" || js[1].Confidence != 0.8 {
		t.Errorf("stored with the chosen option's probability, not the confidence statistic: %+v", js)
	}

	// Taking the blog host out of [sources] makes it a question on the next pass, and nothing else.
	a.Cfg.Sources = map[string][]string{"review": {"reviews.example.com"}}
	a.invalidateJudgments()
	if n, err := a.ClassifyPages(ctx, id); err != nil || n != 1 || len(asked) != 3 || asked[2]["state"].(map[string]any)["host"] != "blog.example.net" {
		t.Fatalf("second pass asked %d (%v): %v", n, err, asked[len(asked)-1]["state"])
	}
	if s := summary(); classUse(s, report.UGC).Hosts != 2 || classUse(s, report.Editorial).Hosts != 0 {
		t.Errorf("both hosts are ugc now: %+v", s.Classes)
	}
	// A configured [sources] entry wins over Jev's decision, without a request.
	a.Cfg.Sources = map[string][]string{"review": {"reviews.example.com"}, "editorial": {"blog.example.net"}}
	a.invalidateJudgments()
	if s := summary(); hostOfPage(s, "blog.example.net").Class != report.Editorial || hostOfPage(s, "example.org").Class != report.UGC {
		t.Errorf("[sources] wins over Jev: %+v", s.Hosts)
	}
	a.Cfg.Sources = map[string][]string{"review": {"reviews.example.com"}}
	a.invalidateJudgments()
	// Asking again asks nothing: every page and host is decided.
	if n, err := a.ClassifyPages(ctx, id); err != nil || n != 0 || len(asked) != 3 {
		t.Errorf("third pass asked %d (%v)", n, err)
	}
	// A stricter threshold restores the rules' answers from the same rows, without a request.
	a.Cfg.TypeSafe.MinConfidence = 0.9
	a.invalidateJudgments()
	strict := summary()
	if p := pageOf(strict, listicle.URL); p.Type != report.OtherPage || p.TypedBy != "" {
		t.Errorf("below the threshold the rule's type stands: %+v", p)
	}
	if classUse(strict, report.UGC).Hosts != 0 || classUse(strict, report.Editorial).Hosts != 2 || len(asked) != 3 {
		t.Errorf("below the threshold the hosts are editorial again: %+v", strict.Classes)
	}
	if js, _ := a.RunPageJudgments(ctx, id); len(js) != 3 || js[0].Applies {
		t.Errorf("the rows are still there, no longer applying: %+v", js)
	}
	var events int
	a.DB.QueryRow(`SELECT COUNT(*) FROM spend_events WHERE kind LIKE 'provider-attempt/page-types/%'`).Scan(&events)
	if events == 0 {
		t.Error("the requests are in the ledger under the pass's name")
	}
}

// With no decisions stored, the report is the rules' alone, and the config's [sources] pass
// through untouched.
func TestPageJudgmentsEmptyLeavesTheRules(t *testing.T) {
	a, id := providersApp(t)
	a.Cfg.Sources = map[string][]string{"ugc": {"forum.example.org"}}
	if len(a.PageTypes()) != 0 || len(a.SourceClasses()) != 1 || a.SourceClasses()["ugc"][0] != "forum.example.org" {
		t.Errorf("types %v sources %v", a.PageTypes(), a.SourceClasses())
	}
	ctx := context.Background()
	r, _ := a.Runs.Get(ctx, id)
	res, _ := a.Runs.Results(ctx, id)
	if p := pageOf(a.Summary(r, res), "https://cheapseats.dev/docs/metrics"); p.Type != report.Docs || p.Class != report.Own {
		t.Errorf("%+v", p)
	}
}

func pageOf(s report.Summary, url string) report.Page {
	for _, p := range s.Pages {
		if p.URL == url {
			return p
		}
	}
	return report.Page{}
}

func hostOfPage(s report.Summary, host string) report.HostUse {
	for _, h := range s.Hosts {
		if h.Host == host {
			return h
		}
	}
	return report.HostUse{}
}

func typeUse(s report.Summary, t string) report.TypeUse {
	for _, u := range s.PageTypes {
		if u.Type == t {
			return u
		}
	}
	return report.TypeUse{}
}

func classUse(s report.Summary, c string) report.ClassUse {
	for _, u := range s.Classes {
		if u.Class == c {
			return u
		}
	}
	return report.ClassUse{}
}
