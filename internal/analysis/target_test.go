package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

func targetBrand() config.Brand {
	return config.Brand{Name: "Acme", Sites: []config.Site{{URL: "https://acme.example", Kind: "marketing"}, {URL: "docs.acme.example", Kind: "docs"}}}
}

func TestResolveTargetFindsTheOnePageProseNames(t *testing.T) {
	b := targetBrand()
	for _, c := range []struct {
		typ          Type
		target, want string
	}{
		{FixPage, "https://docs.acme.example/install", "https://docs.acme.example/install"},
		{FixPage, "The install guide at https://docs.acme.example/install, which omits the CLI.", "https://docs.acme.example/install"},
		{FixPage, "Install guide (see https://docs.acme.example/install).", "https://docs.acme.example/install"},
		{FixPage, "The docs.acme.example/install page's prerequisites section", "https://docs.acme.example/install"},
		{FixPage, "Pricing on WWW.acme.example/pricing", "https://WWW.acme.example/pricing"},
		{Outreach, "The roundup at https://lists.example.org/best-trackers", "https://lists.example.org/best-trackers"},
		{Positioning, "https://acme.example/pricing, https://docs.acme.example/start and https://acme.example/about", "https://acme.example/pricing"},
		{Positioning, "https://reviews.example.org/acme and https://acme.example/about", "https://acme.example/about"},
		{Positioning, "Homepage acme.example and docs.acme.example/start", "https://acme.example"},
		// Never a domain-like token that isn't on a brand site.
		{FixPage, "Update README.md and internal/analysis/fixes.go", "Update README.md and internal/analysis/fixes.go"},
		{FixPage, "README.md, then docs.acme.example/install", "https://docs.acme.example/install"},
		{Outreach, "The awesome list at github.com/someone/awesome-trackers", "The awesome list at github.com/someone/awesome-trackers"},
		{FixPage, "Write to support@acme.example about the docs", "Write to support@acme.example about the docs"},
		// Ambiguous: several pages for one change, or a third-party page beside a brand one.
		{FixPage, "https://acme.example/pricing and https://acme.example/about", "https://acme.example/pricing and https://acme.example/about"},
		{Outreach, "lists.example.org/best or acme.example/compare, https://x.example.org/a https://y.example.org/b", "lists.example.org/best or acme.example/compare, https://x.example.org/a https://y.example.org/b"},
		{FixPage, "The installation section of the docs", "The installation section of the docs"},
		// A product target is a capability name, even when it mentions a page.
		{Product, "Self-hosted install, as asked on https://docs.acme.example/install", "Self-hosted install, as asked on https://docs.acme.example/install"},
	} {
		if got := resolveTarget(b, Fix{Type: c.typ, Target: c.target}); got != c.want {
			t.Errorf("resolveTarget(%s, %q) = %q, want %q", c.typ, c.target, got, c.want)
		}
	}
}

func TestPlanProblemSaysWhichTestFailed(t *testing.T) {
	_, _, f, _, _ := verificationFixture()
	ok := func(check, target string) string { return planProblem(check, f.Decision, f.Evidence, target) }
	if why := ok(f.Check, f.Target); why != "" {
		t.Fatalf("fixture plan rejected: %s", why)
	}
	for _, c := range []struct{ check, target string }{
		{"Read https://www.acme.example/docs/ and confirm whether the install links work.", "https://acme.example/docs"},
		{"Read http://acme.example/docs and confirm whether the install links work.", "https://www.acme.example/docs/"},
		{"Read HTTPS://ACME.example/Docs and confirm whether the install links work.", "acme.example/docs"},
		{"Read the installation section and confirm whether the install links work.", "The installation section of the docs"},
		{"Read the installation section and confirm whether the install links work.", ""},
	} {
		if why := ok(c.check, c.target); why != "" {
			t.Errorf("check %q, target %q: %s", c.check, c.target, why)
		}
	}
	for _, c := range []struct{ check, decision, evidence, target, want string }{
		{"Read https://acme.example/other and confirm whether the install links work.", f.Decision, f.Evidence, f.Target, "its check does not name its target URL"},
		{"Read the page.", f.Decision, f.Evidence, "", "its check is too short"},
		{f.Check, "Decide.", f.Evidence, f.Target, "its decision is too short"},
		{f.Check, f.Decision, " ", f.Target, "it cites no evidence"},
		{f.Check + strings.Repeat(" x", 1000), f.Decision, f.Evidence, f.Target, "too long"},
		{"Publish " + f.Check, f.Decision, f.Evidence, f.Target, "does not start with a read-only verb"},
		{f.Check, "Promote only a supported change if more research finds something useful.", f.Evidence, f.Target, "generic boilerplate"},
		{f.Check, f.Check, f.Evidence, f.Target, "only repeats its decision"},
	} {
		why := planProblem(c.check, c.decision, c.evidence, c.target)
		if c.want == "" && why != "" || !strings.Contains(why, c.want) {
			t.Errorf("planProblem(%q, %q): %q, want %q", c.check, c.target, why, c.want)
		}
	}
}

// reviewWith runs verifyCandidates over fs with a reviewer that returns ds and a reader that
// serves page for every URL, and returns the analysis and every reviewer prompt.
func reviewWith(t *testing.T, fs []Fix, ds []decision, page string) (Analysis, []string, []string) {
	t.Helper()
	cfg, set, _, _, _ := verificationFixture()
	cfg.Brand.Sites = targetBrand().Sites
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	var prompts, reads []string
	structuredCLI = func(_ context.Context, _, _, prompt string, _ map[string]any, out any) error {
		prompts = append(prompts, prompt)
		b, _ := json.Marshal(struct {
			Decisions []decision `json:"decisions"`
		}{ds})
		return json.Unmarshal(b, out)
	}
	ctx := WithVerifier(context.Background(), func(_ context.Context, u string) (string, error) {
		reads = append(reads, u)
		if page == "" {
			return "", errors.New("not read")
		}
		return page, nil
	})
	a, _, err := verifyCandidates(ctx, cfg, set, Analysis{Insights: "Report.", Fixes: fs}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return a, prompts, reads
}

func TestProseTargetWithOneBrandURLIsResolvedAndVerified(t *testing.T) {
	_, _, f, pages, d := verificationFixture()
	f.Target = "The setup section of https://acme.example/docs, where install links live"
	a, prompts, reads := reviewWith(t, []Fix{f}, []decision{d}, pages[0].Text)
	if len(prompts) != 1 || strings.Contains(a.Insights, "Omitted") {
		t.Fatalf("resolved candidate did not reach review: %+v", a)
	}
	if len(reads) == 0 || reads[0] != "https://acme.example/docs" || !strings.Contains(prompts[0], `"target":"https://acme.example/docs"`) {
		t.Fatalf("pages read and reviewer input must use the resolved URL: reads %v", reads)
	}
	if len(a.Fixes) != 1 || a.Fixes[0].Target != "https://acme.example/docs" || a.Fixes[0].Verification.Status != "verified" || !strings.Contains(a.Fixes[0].ClaudePrompt, "https://acme.example/docs") {
		t.Fatalf("resolved candidate not verified at its URL: %+v", a)
	}
}

func TestPositioningTargetResolvesToFirstBrandPage(t *testing.T) {
	_, _, f, pages, d := verificationFixture()
	f.Type = Positioning
	f.Target = "https://reviews.example.org/acme, https://acme.example/docs and https://docs.acme.example/start"
	a, prompts, _ := reviewWith(t, []Fix{f}, []decision{d}, pages[0].Text)
	if len(prompts) != 1 || len(a.Fixes) != 1 || a.Fixes[0].Target != "https://acme.example/docs" {
		t.Fatalf("positioning target not resolved to its first brand page: %+v", a)
	}
}

func TestUnresolvableProseTargetIsReviewedAsAnInvestigation(t *testing.T) {
	_, _, f, pages, d := verificationFixture()
	for _, target := range []string{"Docs", "The installation section of the docs"} {
		f.Target = target
		_, _, reads := reviewWith(t, []Fix{f}, []decision{d}, pages[0].Text)
		for _, u := range reads {
			if !strings.Contains(u, "acme.example") {
				t.Fatalf("prose target %q read as a page: %v", target, reads)
			}
		}
	}
	a, prompts, _ := reviewWith(t, []Fix{f}, []decision{d}, pages[0].Text)
	if len(prompts) != 1 || strings.Contains(a.Insights, "Omitted") {
		t.Fatalf("prose target dropped before review: %+v", a)
	}
	if len(a.Fixes) != 0 || len(a.Investigations) != 1 || !strings.Contains(a.Investigations[0].Why, "could not be read") || a.Investigations[0].Target != f.Target {
		t.Fatalf("a target never read must stay an investigation, never verified: %+v", a)
	}
}

func TestProductTargetSkipsTheLocatorTest(t *testing.T) {
	_, _, f, pages, d := verificationFixture()
	f.Type = Product
	d.Status = "investigate"
	// Prose, and a name shaped like a host: neither is a page the check must name.
	for _, target := range []string{"Self-hosted install", "acme.example/self-hosted"} {
		f.Target = target
		a, prompts, _ := reviewWith(t, []Fix{f}, []decision{d}, pages[0].Text)
		if len(prompts) != 1 || strings.Contains(a.Insights, "Omitted") || len(a.Investigations) != 1 || a.Investigations[0].Target != target {
			t.Fatalf("product candidate %q dropped by the locator test or rewritten: %+v", target, a)
		}
	}
}

func TestCheckNotNamingTargetHasItsOwnReason(t *testing.T) {
	_, _, f, pages, d := verificationFixture()
	f.Check = "Inspect https://acme.example/pricing to confirm whether the installation links still use the retired hostname."
	a, prompts, _ := reviewWith(t, []Fix{f}, []decision{d}, pages[0].Text)
	if len(prompts) != 0 || !strings.Contains(a.Insights, "its check does not name its target URL") || strings.Contains(a.Insights, "no bounded read-only check") {
		t.Fatalf("locator failure lacks its own reason: %q", a.Insights)
	}
}

func TestAnalystIsToldTargetIsOneURLTheCheckNames(t *testing.T) {
	cfg, set, f, _, _ := verificationFixture()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	var prompt string
	structuredCLI = func(_ context.Context, _, _, p string, _ map[string]any, out any) error {
		prompt = p
		return json.Unmarshal([]byte(`{"insights":"A useful starter question.","fixes":[]}`), out)
	}
	tried := []Tried{{Title: "TRIED-TITLE", Target: f.Target}}
	results := []run.Result{{ID: 7, PromptIdx: 0, Answerer: "fake", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Absent}, Answer: &answer.Answer{Text: "BODY-TEXT for the run."}}}
	if _, _, err := Build(context.Background(), cfg, set, results, tried, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"target (one page, as below)",
		"target is one page, written as one absolute URL and nothing else.",
		"on one of Acme's own sites (listed above)",
		"positioning names any other pages in claim, never in target",
		"Never prose, a site area or several URLs. For product, target is a short name of the capability.",
		"The check must contain the target URL exactly as written",
		"TRIED-TITLE", "BODY-TEXT",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("analyst prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, "%!") {
		t.Errorf("analyst prompt has a formatting error")
	}
	sent, err := llm.SchemaJSON(schema)
	if err != nil || llm.CheckSchema(schema) != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"One absolute https URL (fix-page, positioning, new-page, outreach) or a short capability name (product).", "One absolute URL of the page the check reads, or empty."} {
		if !strings.Contains(string(sent), want) {
			t.Errorf("schema lacks %q", want)
		}
	}
	if i, j := strings.Index(string(sent), `"title"`), strings.Index(string(sent), `"target"`); i < 0 || j < i {
		t.Error("schema property order changed")
	}
}
