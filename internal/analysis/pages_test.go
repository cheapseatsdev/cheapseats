package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// pageCtxBuild runs Build with a fake analyst that proposes f and approves it with d, and a
// reader that serves pages (an error for a URL it lacks). It returns every prompt, and the URLs
// read before the analyst's first call and after it.
func pageCtxBuild(t *testing.T, cfg *config.Config, results []run.Result, tried []Tried, dismissed []Dismissed, f Fix, d decision, pages map[string]pageCtxPage) ([]string, []string, []string) {
	t.Helper()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	var prompts, before, after []string
	structuredCLI = func(_ context.Context, _, _, prompt string, _ map[string]any, out any) error {
		prompts = append(prompts, prompt)
		var value any = Analysis{Insights: "A useful starter question.", Fixes: []Fix{f}}
		if len(prompts) == 2 {
			value = struct {
				Decisions []decision `json:"decisions"`
			}{[]decision{d}}
		}
		b, _ := json.Marshal(value)
		return json.Unmarshal(b, out)
	}
	read := func(_ context.Context, u string) (string, bool, error) {
		if len(prompts) == 0 {
			before = append(before, u)
		} else {
			after = append(after, u)
		}
		p, ok := pages[u]
		if !ok {
			return "", false, errors.New("connection refused by https://user:secret@acme.example")
		}
		return p.text, p.cut, nil
	}
	ctx := WithBrandReader(WithVerifier(context.Background(), read), read)
	_, set, _, _, _ := verificationFixture()
	if _, _, err := Build(ctx, cfg, set, results, tried, dismissed); err != nil {
		t.Fatal(err)
	}
	return prompts, before, after
}

type pageCtxPage struct {
	text string
	cut  bool
}

// pageCtxResult is a graded answer citing and fetching urls, and returning search results.
func pageCtxResult(id int64, cited, fetched, returned []string) run.Result {
	a := &answer.Answer{Text: "An answer that names a few trackers."}
	for _, u := range cited {
		a.Citations = append(a.Citations, answer.Link{URL: u, Title: "page"})
	}
	a.Fetches = fetched
	if len(returned) > 0 {
		q := answer.Query{Query: "starter tracker"}
		for _, u := range returned {
			q.Results = append(q.Results, answer.Link{URL: u, Title: "result"})
		}
		a.Searches = []answer.Query{q}
	}
	return run.Result{ID: id, PromptIdx: 0, Answerer: "fake", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Absent}, Answer: a}
}

// The fixes call carries an excerpt of each brand page it is likely to target, within budget and
// with its truncated flag, a note for a page that couldn't be read, and how to use them.
func TestAnalystSeesCurrentBrandPages(t *testing.T) {
	cfg, _, f, pages, d := verificationFixture()
	cfg.Brand.Sites = []config.Site{{URL: "acme.example", Kind: "marketing"}, {URL: "docs.acme.example", Kind: "docs"}}
	long := strings.Repeat("é", MaxAnalystPageRunes) + "PAST THE EXCERPT BUDGET"
	served := map[string]pageCtxPage{
		"https://acme.example":         {text: "Acme home page. A starter tracker for small teams, with setup in minutes."},
		"https://docs.acme.example":    {text: "Docs index, as far as the reader got.", cut: true},
		"https://acme.example/pricing": {text: long},
		f.Target:                       {text: pages[0].Text},
	}
	results := []run.Result{
		pageCtxResult(1, []string{"https://acme.example/pricing", "https://rival.example/top-trackers"}, nil, nil),
		pageCtxResult(2, []string{"https://acme.example/pricing/"}, []string{"https://acme.example/install"}, nil),
		pageCtxResult(3, nil, nil, []string{"https://acme.example/blog/launch"}),
	}
	prompts, before, _ := pageCtxBuild(t, cfg, results, nil, nil, f, d, served)

	want := []string{"https://acme.example", "https://docs.acme.example", "https://acme.example/pricing", "https://acme.example/install", "https://acme.example/blog/launch"}
	if strings.Join(before, " ") != strings.Join(want, " ") {
		t.Fatalf("analyst pages read:\n got %q\nwant %q", before, want)
	}
	p := prompts[0]
	for _, s := range []string{
		"Current text of Acme's own pages",
		fmt.Sprintf("- https://acme.example (truncated: false): %q", served["https://acme.example"].text),
		fmt.Sprintf("- https://docs.acme.example (truncated: true): %q", served["https://docs.acme.example"].text+truncatedMarker),
		fmt.Sprintf("- https://acme.example/pricing (truncated: true): %q", strings.Repeat("é", MaxAnalystPageRunes)+truncatedMarker),
		"- https://acme.example/install: could not be read; what it says now is unknown.",
		"A candidate whose change a supplied page already makes is not a candidate",
		"A truncated excerpt is only the start of its page, so it cannot show that something is absent",
	} {
		if !strings.Contains(p, s) {
			t.Errorf("fixes prompt lacks %q", s)
		}
	}
	if strings.Contains(p, "PAST THE EXCERPT BUDGET") {
		t.Error("an excerpt ran past its budget")
	}
	if strings.Contains(p, "secret") || strings.Contains(p, "connection refused") {
		t.Error("a read error's text reached the analyst")
	}
}

// Only absolute URLs on the brand's sites are read: never a third-party page, a product
// capability or a target written without a scheme, and never more than MaxAnalystPages.
func TestAnalystPagesSkipOtherTargetsAndStopAtTheCap(t *testing.T) {
	brand := config.Brand{Name: "Acme", Sites: []config.Site{{URL: "acme.example", Kind: "marketing"}}}
	var results []run.Result
	for i := 0; i < 12; i++ {
		// Page i is cited by 12-i answers, so the order is p0, p1, ...
		for j := 0; j < 12-i; j++ {
			results = append(results, pageCtxResult(int64(100*i+j), []string{fmt.Sprintf("https://acme.example/p%d", i), "https://rival.example/list"}, nil, nil))
		}
	}
	tried := []Tried{{Target: "Bulk export"}, {Target: "acme.example/roadmap"}, {Target: "https://rival.example/awesome"}}
	dismissed := []Dismissed{{Target: "https://acme.example/old"}}
	got := analystPageURLs(brand, results, tried, dismissed)
	want := []string{"https://acme.example"}
	for i := 0; i < MaxAnalystPages-1; i++ {
		want = append(want, fmt.Sprintf("https://acme.example/p%d", i))
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("pages:\n got %q\nwant %q", got, want)
	}

	// With room left, own absolute targets of earlier attempts and dismissals are read; the rest
	// are not.
	got = analystPageURLs(brand, results[:1], tried, dismissed)
	want = []string{"https://acme.example", "https://acme.example/p0", "https://acme.example/old"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("pages:\n got %q\nwant %q", got, want)
	}
}

// With no brand reader, or nothing on the brand's sites to read, the prompt has no page section.
// The verifier, which follows redirects to any public host for outreach targets, is never used
// for the analyst's pages.
func TestAnalystPagesNeedAReaderAndAPage(t *testing.T) {
	brand := config.Brand{Name: "Acme", Sites: []config.Site{{URL: "acme.example", Kind: "marketing"}}}
	reads := 0
	read := func(context.Context, string) (string, bool, error) {
		reads++
		return "A page with plenty of words on it for the analyst to read today.", false, nil
	}
	if s := currentPagesText(WithVerifier(context.Background(), read), brand, nil, nil, nil); s != "" || reads != 0 {
		t.Fatalf("read %d pages through the verifier: %q", reads, s)
	}
	ctx := WithBrandReader(context.Background(), read)
	if s := currentPagesText(ctx, config.Brand{Name: "Acme"}, nil, nil, nil); s != "" || reads != 0 {
		t.Fatalf("no brand page, yet %d reads: %q", reads, s)
	}
}

// The review is unchanged: Build's review call is the one verifyCandidates makes on its own,
// with the same pages read, and carries no analyst page section.
func TestAnalystPagesLeaveTheReviewUnchanged(t *testing.T) {
	cfg, set, f, pages, d := verificationFixture()
	cfg.Brand.Sites = []config.Site{{URL: "acme.example", Kind: "marketing"}}
	served := map[string]pageCtxPage{f.Target: {text: pages[0].Text}, "https://acme.example": {text: "Acme home page, as it reads today for small teams."}}
	results := []run.Result{pageCtxResult(1, []string{f.Target}, nil, nil)}
	prompts, before, after := pageCtxBuild(t, cfg, results, nil, nil, f, d, served)
	if len(prompts) != 2 || len(before) == 0 {
		t.Fatalf("prompts %d, analyst reads %q", len(prompts), before)
	}

	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	var alone, reads []string
	structuredCLI = func(_ context.Context, _, _, prompt string, _ map[string]any, out any) error {
		alone = append(alone, prompt)
		b, _ := json.Marshal(struct {
			Decisions []decision `json:"decisions"`
		}{[]decision{d}})
		return json.Unmarshal(b, out)
	}
	ctx := WithVerifier(context.Background(), func(_ context.Context, u string) (string, bool, error) {
		reads = append(reads, u)
		p, ok := served[u]
		if !ok {
			return "", false, errors.New("not read")
		}
		return p.text, p.cut, nil
	})
	if _, _, err := verifyCandidates(ctx, cfg, set, Analysis{Insights: "A useful starter question.", Fixes: []Fix{f}}, 0); err != nil {
		t.Fatal(err)
	}
	if len(alone) != 1 || alone[0] != prompts[1] {
		t.Fatalf("Build's review prompt differs from verifyCandidates':\n%s\n---\n%s", prompts[1], strings.Join(alone, "\n"))
	}
	if strings.Join(after, " ") != strings.Join(reads, " ") {
		t.Fatalf("review reads %q, want %q", after, reads)
	}
	if strings.Contains(prompts[1], "Current text of") {
		t.Fatal("the analyst's page section reached the review")
	}
}
