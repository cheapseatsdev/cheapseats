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

func TestPlanProblemSaysWhichTestFailed(t *testing.T) {
	_, _, f, _, _ := verificationFixture()
	ok := func(check, target string) string { return planProblem(check, f.Decision, f.Evidence, target) }
	if why := ok(f.Check, f.Target); why != "" {
		t.Fatalf("fixture plan rejected: %s", why)
	}
	for _, c := range []struct{ check, target string }{
		{"Read https://www.acme.example/docs/ and confirm whether the install links work.", "https://acme.example/docs"},
		{"Read http://acme.example/docs and confirm whether the install links work.", "https://www.acme.example/docs/"},
		{"Read HTTPS://WWW.ACME.example/Docs and confirm whether the install links work.", "https://acme.example/Docs"},
		{"Read https://acme.example/Guide_(CLI) and confirm whether the install links work.", "https://acme.example/Guide_(CLI)"},
		{"Compare https://acme.example/pricing with https://acme.example/docs?tab=Install to confirm the links.", "https://acme.example/docs?tab=Install"},
		// Sentence punctuation after a URL in the check is not part of it; the target's own is.
		{"Read https://acme.example/a. Then confirm whether the install links work.", "https://acme.example/a"},
		// An apostrophe between two URL characters is part of the URL; a quote around it is not.
		{"Read https://x.example/john's-guide and confirm whether the install links work.", "https://x.example/john's-guide"},
		{"Read https://x.example/john\u2019s-guide and confirm whether the install links work.", "https://x.example/john\u2019s-guide"},
		{"Read 'https://x.example/a' and confirm whether the install links work.", "https://x.example/a"},
		{"Read 'https://x.example/john's-guide' and confirm whether the install links work.", "https://x.example/john's-guide"},
		{"Read https://x.example/a's. Then confirm whether the install links work.", "https://x.example/a's"},
		{"Read https://x.example/a!'note' and confirm whether the install links work.", "https://x.example/a"},
		// A delimited URL is taken exactly as written inside its delimiters.
		{"Read <https://acme.example/docs> and confirm whether the install links work.", "https://acme.example/docs"},
		{"Read [https://acme.example/docs] and confirm whether the install links work.", "https://acme.example/docs"},
		{"Read `https://acme.example/docs/` and confirm whether the install links work.", "https://acme.example/docs"},
		{"Read https://acme.example/a, https://acme.example/b; then confirm whether the links work.", "https://acme.example/b"},
		{"Read \u201chttps://acme.example/a\u201d and confirm whether the install links work.", "https://acme.example/a"},
		{"Check the CLI guide (https://acme.example/Guide_(CLI)) to confirm whether the install links work.", "https://acme.example/Guide_(CLI)"},
		{"Check the CLI guide (https://acme.example/Guide_(CLI)). Confirm whether the install links work.", "https://acme.example/Guide_(CLI)"},
		{"Read the installation section and confirm whether the install links work.", "The installation section of the docs"},
		{"Read acme.example/docs/devcontainers and confirm whether the install links work.", "acme.example/docs"},
		{"Read the installation section and confirm whether the install links work.", ""},
	} {
		if why := ok(c.check, c.target); why != "" {
			t.Errorf("check %q, target %q: %s", c.check, c.target, why)
		}
	}
	for _, c := range []struct{ check, decision, evidence, target, want string }{
		{"Read https://acme.example/other and confirm whether the install links work.", f.Decision, f.Evidence, f.Target, "its check does not name its target URL"},
		// Paths, queries and fragments are case-sensitive: /Docs is not /docs.
		{"Read https://acme.example/Docs and confirm whether the install links work.", f.Decision, f.Evidence, "https://acme.example/docs", "its check does not name its target URL"},
		{"Read https://acme.example/docs?tab=install and confirm whether the links work.", f.Decision, f.Evidence, "https://acme.example/docs?tab=Install", "its check does not name its target URL"},
		// No punctuation is trimmed, and a longer URL that starts with the target is another page.
		{"Read https://acme.example/Guide_ and confirm whether the install links work.", f.Decision, f.Evidence, "https://acme.example/Guide_(CLI)", "its check does not name its target URL"},
		{"Read https://x.example/john's-guide and confirm whether the install links work.", f.Decision, f.Evidence, "https://x.example/john", "its check does not name its target URL"},
		{"Read 'https://x.example/a' and confirm whether the install links work.", f.Decision, f.Evidence, "https://x.example/a'", "its check does not name its target URL"},
		// Inside delimiters nothing is trimmed: the punctuation is part of the URL written there.
		{"Read <https://acme.example/docs!> and confirm whether the install links work.", f.Decision, f.Evidence, "https://acme.example/docs", "its check does not name its target URL"},
		{"Read \"https://acme.example/docs.\" and confirm whether the install links work.", f.Decision, f.Evidence, "https://acme.example/docs", "its check does not name its target URL"},
		{"Read \u201chttps://acme.example/docs;\u201d and confirm whether the install links work.", f.Decision, f.Evidence, "https://acme.example/docs", "its check does not name its target URL"},
		{"Read https://acme.example/Guide_(CLI and confirm whether the install links work.", f.Decision, f.Evidence, "https://acme.example/Guide_(CLI)", "its check does not name its target URL"},
		{"Read https://acme.example/docs/old and confirm whether the install links work.", f.Decision, f.Evidence, "https://acme.example/docs", "its check does not name its target URL"},
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
	ctx := WithVerifier(context.Background(), func(_ context.Context, u string) (string, bool, error) {
		reads = append(reads, u)
		if page == "" {
			return "", false, errors.New("not read")
		}
		return page, false, nil
	})
	a, _, err := verifyCandidates(ctx, cfg, set, Analysis{Insights: "Report.", Fixes: fs}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return a, prompts, reads
}

func TestNonURLTargetIsReviewedAsAnInvestigationWithoutARead(t *testing.T) {
	_, _, f, pages, d := verificationFixture()
	// Prose, a one-word name, prose holding a URL, and a bare host: none is one absolute URL,
	// none is rewritten or read, and each is reviewed rather than dropped.
	for _, target := range []string{"Docs", "acme.example/docs", "The setup section of https://acme.example/docs", "The installation section of the docs"} {
		f.Target = target
		a, prompts, reads := reviewWith(t, []Fix{f}, []decision{d}, pages[0].Text)
		for _, u := range reads {
			if u != "https://acme.example" && u != "https://docs.acme.example" {
				t.Fatalf("non-URL target %q read as a page: %v", target, reads)
			}
		}
		if len(prompts) != 1 || len(a.Investigations) != 1 || a.Investigations[0].Target != target {
			t.Fatalf("non-URL target %q not reviewed as written: %+v", target, a)
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

func TestBareHostTargetEqualToAReadBrandPageDoesNotVerify(t *testing.T) {
	_, _, f, pages, d := verificationFixture()
	// acme.example is a brand site, read as reference at https://acme.example; written bare, the
	// target was never read as itself, so the reviewer's quotes from that page can't verify it.
	f.Target = "acme.example"
	for g, p := range d.Checks {
		if p.Source == "https://acme.example/docs" {
			p.Source = "https://acme.example"
			d.Checks[g] = p
		}
	}
	a, prompts, reads := reviewWith(t, []Fix{f}, []decision{d}, pages[0].Text)
	if len(prompts) != 1 || len(reads) == 0 || reads[0] != "https://acme.example" {
		t.Fatalf("brand page not read as reference: %v", reads)
	}
	if len(a.Fixes) != 0 || len(a.Investigations) != 1 || !strings.Contains(a.Investigations[0].Why, "could not be read") {
		t.Fatalf("a bare-host target verified against a reference page: %+v", a)
	}
}

func TestProductTargetSkipsTheLocatorTest(t *testing.T) {
	_, _, f, pages, d := verificationFixture()
	f.Type = Product
	d.Status = "investigate"
	// Prose, and names shaped like addresses: none is a page the check must name or the reader
	// fetches, even an absolute URL the check never mentions.
	for _, target := range []string{"Self-hosted install", "acme.example/self-hosted", "https://acme.example/self-hosted"} {
		f.Target = target
		a, prompts, reads := reviewWith(t, []Fix{f}, []decision{d}, pages[0].Text)
		if len(prompts) != 1 || strings.Contains(a.Insights, "Omitted") || len(a.Investigations) != 1 || a.Investigations[0].Target != target {
			t.Fatalf("product candidate %q dropped by the locator test or rewritten: %+v", target, a)
		}
		for _, u := range reads {
			if strings.Contains(u, "self-hosted") {
				t.Fatalf("product target %q fetched as a page: %v", target, reads)
			}
		}
		if len(reads) != 2 {
			t.Fatalf("product candidate reads: %v", reads)
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
