package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

func verificationFixture() (*config.Config, sets.Set, Fix, []artifact, decision) {
	cfg := &config.Config{Brand: config.Brand{
		Name: "Acme", About: "A starter tracker.", Audience: "a small startup", Category: "AI visibility starter workflows",
		Facts:    []string{"The supported documentation host is docs.acme.example."},
		Advisory: config.Advisory{Positioning: "Help small teams start measuring their visibility.", Boundaries: []string{"Do not add enterprise compliance features."}},
	}, Analyst: config.Model{Model: "fake", Provider: config.ClaudeCode}}
	set := sets.Set{Name: "Starter decisions", Description: "Understand setup friction without removing valid alternatives.", Prompts: []sets.Prompt{{ID: "starter", Text: "Which starter tracker?"}}}
	f := Fix{Rank: 1, Type: FixPage, Title: "Replace the retired documentation hostname", Target: "https://acme.example/docs", Claim: "Replace docs.old.example with docs.acme.example.", DoneWhen: "The checked-in link points to docs.acme.example and the local link check passes.", Evidence: "Result 7 could not locate the documentation.", Prompts: []int{1}, Effort: "S", Check: "Inspect https://acme.example/docs to confirm whether the installation links still use the retired documentation hostname.", Decision: "Decide whether repairing the existing setup link is needed before directing startup users to that page."}
	pages := []artifact{{URL: f.Target, Text: "Installation instructions are present. Documentation is linked at docs.old.example, a retired hostname."}}
	d := decision{Index: 0, Status: "verified", Summary: "The setup instructions already exist; only their retired documentation link needs changing.", Checks: map[string]proof{}}
	for _, g := range gates {
		d.Checks[g] = proof{Passed: true, Source: f.Target, Quote: pages[0].Text, Reason: "The existing installation content contains the retired link that this narrowly scoped change replaces."}
	}
	d.Checks["capability"] = proof{Passed: true, Source: "brand.facts", Quote: cfg.Brand.Facts[0], Reason: "The requested hostname is already the supported documented host, with no new feature claim."}
	d.Checks["strategy"] = proof{Passed: true, Source: "brand.advisory", Quote: cfg.Brand.Advisory.Positioning, Reason: "Repairing an existing setup link supports the declared small-team starting experience."}
	d.Checks["completion"] = proof{Passed: true, Source: "candidate.done_when", Quote: f.DoneWhen, Reason: "Changing and locally checking a link is controlled by the team, without requiring a future recommendation."}
	return cfg, set, f, pages, d
}

func analysisRecordedVerification(cfg *config.Config, f Fix) Verification {
	v := Verification{Status: "verified", Summary: "Current artifact checks support this scoped test action.", Sources: []string{webURL(f.Target)}}
	for _, gate := range gates {
		c := VerificationCheck{Gate: gate, Source: webURL(f.Target), Quote: "Current artifact evidence for this scoped change.", Reason: "This explicit fixture records the completed artifact check for the proposed change."}
		if gate == "strategy" {
			c.Source = "brand.advisory"
			c.Quote = cfg.Brand.Advisory.Text()
		}
		if gate == "completion" {
			c.Source = "candidate.done_when"
			c.Quote = f.DoneWhen
		}
		v.Checks = append(v.Checks, c)
	}
	v.Context = VerificationContext(cfg, f.Target, v)
	return v
}

func TestVerificationRejectsInventedOrIncompleteProof(t *testing.T) {
	cfg, _, f, pages, d := verificationFixture()
	if v, why := verifyDecision(d, f, cfg, pages); why != "" || v.Status != "verified" || len(v.Checks) != len(gates) {
		t.Fatalf("verified remaining change: %+v %s", v, why)
	}
	d.Checks["remaining_change"] = proof{Passed: true, Source: f.Target, Quote: "This text was never fetched from the page.", Reason: "A plausible sounding but invented quote must never authorize an action."}
	if _, why := verifyDecision(d, f, cfg, pages); !strings.Contains(why, "remaining_change") {
		t.Fatalf("invented quote accepted: %q", why)
	}
	_, _, _, _, d = verificationFixture()
	p := d.Checks["existing_work"]
	p.Source = "https://unread.example"
	d.Checks["existing_work"] = p
	if _, why := verifyDecision(d, f, cfg, pages); why == "" {
		t.Fatal("unread source accepted")
	}
	_, _, _, _, d = verificationFixture()
	delete(d.Checks, "eligibility")
	if _, why := verifyDecision(d, f, cfg, pages); !strings.Contains(why, "eligibility") {
		t.Fatalf("missing check accepted: %q", why)
	}
	_, _, _, _, d = verificationFixture()
	f.Type = NewPage
	if _, why := verifyDecision(d, f, cfg, pages); !strings.Contains(why, "inventory") {
		t.Fatalf("sampled pages proved sitewide absence: %q", why)
	}
}

func TestBuildVerifiesCurrentWorkAndSeparatesUnknowns(t *testing.T) {
	cfg, set, f, pages, d := verificationFixture()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	var prompts []string
	structuredCLI = func(_ context.Context, _ string, _ string, prompt string, _ map[string]any, out any) error {
		prompts = append(prompts, prompt)
		var value any = Analysis{Insights: "A useful starter question.", Fixes: []Fix{f}}
		if len(prompts)%2 == 0 {
			value = struct {
				Decisions []decision `json:"decisions"`
			}{[]decision{d}}
		}
		b, _ := json.Marshal(value)
		return json.Unmarshal(b, out)
	}
	results := []run.Result{{ID: 7, PromptIdx: 0, Answerer: "fake", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Absent}, Answer: &answer.Answer{Text: "A spreadsheet is enough for an initial baseline; a tracker earns its setup for repeat checks.", Searches: []answer.Query{{Query: "starter tracker", Results: []answer.Link{{URL: f.Target, Title: "Existing installation"}}}}}}}
	ctx := WithVerifier(context.Background(), func(_ context.Context, u string) (string, bool, error) {
		if u == f.Target {
			return pages[0].Text, false, nil
		}
		return "", false, errors.New("not read")
	})
	a, _, err := Build(ctx, cfg, set, results, nil, nil)
	if err != nil || len(a.Fixes) != 1 || len(a.Investigations) != 0 {
		t.Fatalf("Build: %+v %v", a, err)
	}
	if len(prompts) != 2 || !strings.Contains(a.Fixes[0].ClaudePrompt, cfg.Brand.Advisory.Positioning) {
		t.Fatalf("missing review or scoped handoff: %+v", a)
	}
	roundTrip, roundTripErr := DecodeAnalysis(Encode(a))
	if roundTripErr != nil || len(roundTrip.Fixes) != 1 || !roundTrip.Fixes[0].Verification.Recorded() || Triage(cfg, set, roundTrip.Fixes[0]).ClaudePrompt == "" {
		t.Fatalf("legitimate Build verification lost readiness on roundtrip: %+v %v", roundTrip, roundTripErr)
	}
	for _, want := range []string{cfg.Brand.Audience, cfg.Brand.Category, set.Description, "A spreadsheet is enough", "returned search result (not proof of reading)"} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("analyst lacks %q", want)
		}
	}
	prompts = nil
	a, _, err = Build(WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) {
		return "", false, errors.New("temporary network failure")
	}), cfg, set, results, nil, nil)
	var verr *VerificationError
	if !errors.As(err, &verr) || !a.VerificationFailed || len(a.Fixes) != 0 || len(a.Investigations) != 1 || len(prompts) != 1 {
		t.Fatalf("failed reads must stay unknown, as a failed verification, without a redundant review call: %+v %v", a, err)
	}
	if a.Investigations[0].Check != f.Check || a.Investigations[0].Decision != f.Decision {
		t.Fatalf("useful unknown lost its specific decision: %+v", a.Investigations[0])
	}
}

func TestReadArtifactsHasBoundedRequests(t *testing.T) {
	cfg, _, f, _, _ := verificationFixture()
	var fs []Fix
	for i := 0; i < MaxCandidates; i++ {
		x := f
		x.Target = fmt.Sprintf("https://acme.example/page/%d", i)
		fs = append(fs, x)
	}
	for i := 0; i < 20; i++ {
		cfg.Brand.Sites = append(cfg.Brand.Sites, config.Site{URL: fmt.Sprintf("https://site%d.example", i)})
	}
	n := 0
	ctx := WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) {
		n++
		return strings.Repeat("x", MaxVerificationTargetRunes+100), false, nil
	})
	ps := readArtifacts(ctx, cfg, fs)
	if n != MaxVerificationPages || len(ps) != MaxVerificationPages {
		t.Fatalf("unbounded reads: %d", n)
	}
	for i, p := range ps {
		limit := MaxVerificationTargetRunes
		if i >= MaxCandidates {
			limit = MaxVerificationPageRunes // brand pages
		}
		if !p.Truncated || p.Text != strings.Repeat("x", limit)+truncatedMarker {
			t.Fatalf("page %d (%s) not clipped to %d: %d runes, truncated=%v", i, p.URL, limit, len([]rune(p.Text)), p.Truncated)
		}
	}
}

// Truncated must be set exactly when text was cut, counting characters rather than bytes, so
// that a page supplied whole can support a remaining change and an excerpt never can.
func TestReadArtifactsMarksTruncatedOnlyWhenClipped(t *testing.T) {
	cfg, _, f, _, _ := verificationFixture()
	whole, cut := f, f
	whole.Target, cut.Target = "https://acme.example/whole", "https://acme.example/cut"
	cfg.Brand.Sites = []config.Site{{URL: "https://acme.example/brand-whole"}, {URL: "https://acme.example/brand-cut"}}
	body := map[string]string{
		"https://acme.example/whole":       strings.Repeat("é", MaxVerificationTargetRunes),
		"https://acme.example/cut":         strings.Repeat("é", MaxVerificationTargetRunes+1),
		"https://acme.example/brand-whole": strings.Repeat("é", MaxVerificationPageRunes),
		"https://acme.example/brand-cut":   strings.Repeat("é", MaxVerificationPageRunes+1),
	}
	ctx := WithVerifier(context.Background(), func(_ context.Context, u string) (string, bool, error) { return body[u], false, nil })
	got := map[string]artifact{}
	for _, p := range readArtifacts(ctx, cfg, []Fix{whole, cut}) {
		got[p.URL] = p
	}
	for u, want := range map[string]bool{"https://acme.example/whole": false, "https://acme.example/cut": true, "https://acme.example/brand-whole": false, "https://acme.example/brand-cut": true} {
		p := got[u]
		if p.Truncated != want || strings.HasSuffix(p.Text, truncatedMarker) != want {
			t.Errorf("%s: truncated=%v, marker=%v, want %v", u, p.Truncated, strings.HasSuffix(p.Text, truncatedMarker), want)
		}
	}
}

// omissionFixture is a candidate whose gap is a plain omission: the complete target gives
// the setup commands and says nothing about a runtime version, without any sentence admitting
// the gap. remaining_change quotes the passage the change would sit beside.
func omissionFixture() (*config.Config, sets.Set, Fix, string, decision) {
	cfg, set, f, _, _ := verificationFixture()
	cfg.Brand.Facts = []string{"The CLI requires Go 1.27 or later."}
	f.Title = "State the supported Go version beside setup"
	f.Target = "https://acme.example/setup"
	f.Claim = "Add the supported Go version, Go 1.27 or later, beside the first-run commands."
	f.DoneWhen = "The setup page states Go 1.27 or later beside the first-run commands."
	f.Check = "Inspect https://acme.example/setup to confirm whether the first-run commands state a supported Go version."
	f.Decision = "Decide whether to add the runtime requirement before directing startup users to the setup page."
	page := "Setup. Install the CLI with go install acme.example/tracker@latest. Run tracker init, then tracker serve, to start locally. The server stores answers in a local SQLite file."
	d := decision{Index: 0, Status: "verified", Summary: "The complete setup page gives the commands but no runtime version; adding it beside them is a small owned edit.", Checks: map[string]proof{
		"existing_work":    {Passed: true, Source: f.Target, Quote: "Install the CLI with go install acme.example/tracker@latest.", Reason: "The target already documents installing the CLI with Go, so the version belongs on this page."},
		"remaining_change": {Passed: true, Source: f.Target, Quote: "Run tracker init, then tracker serve, to start locally.", Reason: "The version sits beside these first-run commands; the complete page states no required Go version in any wording."},
		"capability":       {Passed: true, Source: "brand.facts", Quote: cfg.Brand.Facts[0], Reason: "The supported runtime is a stated fact, so the edit adds no new capability claim."},
		"strategy":         {Passed: true, Source: "brand.advisory", Quote: cfg.Brand.Advisory.Positioning, Reason: "A clear runtime requirement helps small teams start measuring without setup failures."},
		"eligibility":      {Passed: true, Source: f.Target, Quote: "Setup. Install the CLI", Reason: "This is the team's own setup page, so no publisher eligibility rules apply."},
		"completion":       {Passed: true, Source: "candidate.done_when", Quote: f.DoneWhen, Reason: "The team edits its own page and can check the stated version directly."},
	}}
	return cfg, set, f, page, d
}

// reviewPageReader reads target as page and fails every other read.
func reviewPageReader(target, page string) VerifyPage {
	return func(_ context.Context, u string) (string, bool, error) {
		if u == target {
			return page, false, nil
		}
		return "", false, errors.New("not read")
	}
}

func reviewWithFakeAnalyst(t *testing.T, cfg *config.Config, set sets.Set, f Fix, read VerifyPage, d decision) (Analysis, string) {
	t.Helper()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	var prompt string
	structuredCLI = func(_ context.Context, _, _, p string, _ map[string]any, out any) error {
		prompt = p
		b, _ := json.Marshal(struct {
			Decisions []decision `json:"decisions"`
		}{[]decision{d}})
		return json.Unmarshal(b, out)
	}
	a, _, err := verifyCandidates(WithVerifier(context.Background(), read), cfg, set, Analysis{Fixes: []Fix{f}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return a, prompt
}

func TestCompletePageVerifiesPlainOmission(t *testing.T) {
	cfg, set, f, page, d := omissionFixture()
	a, prompt := reviewWithFakeAnalyst(t, cfg, set, f, reviewPageReader(f.Target, page), d)
	if len(a.Fixes) != 1 || len(a.Investigations) != 0 || a.Fixes[0].Verification.Status != "verified" {
		t.Fatalf("a supported omission on a complete page did not verify: %+v", a)
	}
	if !strings.Contains(prompt, `"truncated":false`) {
		t.Fatal("the reviewer was not told the target is complete")
	}
	// Each safeguard that keeps an omission claim honest: deleting any of them fails here.
	for _, safeguard := range []string{
		"A quote cannot show absence by itself",
		"pass remaining_change only when the page you quote is not truncated",
		"you have read all of its supplied text",
		"nothing in it already makes the requested change in any wording",
		"If that page is truncated, remaining_change is unknown (passed=false, status investigate)",
		"For a product candidate, if any supplied page about that feature is truncated, nothing it omits counts as missing",
		"If the target already makes the change, status is already-done",
		"a truncated page, an unread page or a read error is not",
		"truncated=true marks an excerpt, which can show what a page says but never what it lacks",
	} {
		if !strings.Contains(prompt, safeguard) {
			t.Errorf("review prompt lost the safeguard %q", safeguard)
		}
	}
}

func TestTruncatedTargetCannotVerifyRemainingChange(t *testing.T) {
	cfg, set, f, page, d := omissionFixture()
	// The same page with a long footer: every quote is still in the supplied excerpt, and the
	// fake reviewer still claims remaining_change passed.
	page += " " + strings.Repeat("Footer link. ", MaxVerificationTargetRunes/10)
	a, prompt := reviewWithFakeAnalyst(t, cfg, set, f, reviewPageReader(f.Target, page), d)
	if len(a.Fixes) != 0 || len(a.Investigations) != 1 || !strings.Contains(a.Investigations[0].Why, "remaining_change") {
		t.Fatalf("an excerpt verified a remaining change: %+v", a)
	}
	if !strings.Contains(prompt, `"truncated":true`) {
		t.Fatal("the reviewer was not told the target is truncated")
	}
}

// What an excerpt does say still counts: a truncated target that already states the requested
// version supports already-done, quoting that statement.
func TestTruncatedTargetCanShowAlreadyDone(t *testing.T) {
	cfg, set, f, _, d := omissionFixture()
	page := "Setup. Install the CLI with go install acme.example/tracker@latest. Requires Go 1.27 or later. Run tracker init, then tracker serve, to start locally. " + strings.Repeat("Footer link. ", MaxVerificationTargetRunes/10)
	d.Status = "already-done"
	d.Summary = "The setup page already states Go 1.27 or later beside the first-run commands."
	d.Checks["existing_work"] = proof{Passed: true, Source: f.Target, Quote: "Requires Go 1.27 or later. Run tracker init, then tracker serve", Reason: "The target already states the requested runtime version beside the first-run commands."}
	d.Checks["remaining_change"] = proof{Passed: false, Source: f.Target, Quote: "", Reason: "Nothing remains: the requested version is already stated."}
	a, _ := reviewWithFakeAnalyst(t, cfg, set, f, reviewPageReader(f.Target, page), d)
	if len(a.Findings) != 1 || len(a.Fixes) != 0 || len(a.Investigations) != 0 || !strings.Contains(a.Findings[0].Evidence, "Requires Go 1.27") {
		t.Fatalf("a quote from an excerpt could not show existing work: %+v", a)
	}
}

// A read that stops short of the page can extract to short text that looks complete. Each such
// read must reach the artifact as truncated, so the omission it seems to show can't verify:
//   - raw-cap: a readable paragraph, then inline script past audit.MaxBody, then the rest;
//   - short-body: the server declares a longer Content-Length and closes after a readable prefix;
//   - shell: a page empty without JavaScript, read with no Reader to render it.
//
// A whole page served the same way verifies, so each failure is the cut's doing.
func TestReadStoppedShortCannotVerifyRemainingChange(t *testing.T) {
	restore := audit.AllowLoopbackForTests()
	defer restore()
	_, _, _, page, _ := omissionFixture()
	more := "<p>" + strings.Repeat("The setup guide covers local installs for small teams. ", 4) + "</p>"
	whole := "<html><body><p>" + page + "</p>" + more + "</body></html>"
	for _, tc := range []struct {
		name      string
		serve     func(w http.ResponseWriter)
		truncated bool
	}{
		{"whole", func(w http.ResponseWriter) { w.Write([]byte(whole)) }, false},
		{"raw-cap", func(w http.ResponseWriter) {
			w.Write([]byte("<html><body><p>" + page + "</p>" + more + "<script>" + strings.Repeat("var x = 1;\n", audit.MaxBody/10) + "</script><p>Requires Go 1.27 or later.</p></body></html>"))
		}, true},
		{"short-body", func(w http.ResponseWriter) {
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			prefix := "<html><body><p>" + page + "</p>" + more
			fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\n\r\n%s", len(prefix)+5000, prefix)
			buf.Flush()
		}, true},
		{"shell", func(w http.ResponseWriter) {
			w.Write([]byte(`<html><body><div id="root"><p>` + page + `</p></div><script src="/app.js"></script></body></html>`))
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, set, f, _, d := omissionFixture()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/setup" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("content-type", "text/html; charset=utf-8")
				tc.serve(w)
			}))
			defer srv.Close()
			old := f.Target
			f.Target = srv.URL + "/setup"
			f.Check = strings.Replace(f.Check, old, f.Target, 1)
			for g, p := range d.Checks {
				if p.Source == old {
					p.Source = f.Target
					d.Checks[g] = p
				}
			}
			read := func(ctx context.Context, u string) (string, bool, error) {
				return audit.ReadCurrent(ctx, u, audit.CiteOptions{}) // no Reader
			}
			pages := readArtifacts(WithVerifier(context.Background(), read), cfg, []Fix{f})
			if len(pages) != 1 || pages[0].Text == "" || pages[0].Truncated != tc.truncated {
				t.Fatalf("truncated=%v, want %v: %+v", len(pages) == 1 && pages[0].Truncated, tc.truncated, pages)
			}
			a, _ := reviewWithFakeAnalyst(t, cfg, set, f, read, d)
			if verified := len(a.Fixes) == 1; verified == tc.truncated {
				t.Fatalf("verified=%v from a read with truncated=%v: %+v", verified, tc.truncated, a)
			}
			if tc.truncated && (len(a.Investigations) != 1 || !strings.Contains(a.Investigations[0].Why, "remaining_change")) {
				t.Fatalf("an omission from a read that stopped short was not held back by remaining_change: %+v", a)
			}
		})
	}
}

func TestWeakCandidateDoesNotCreateResearchBacklog(t *testing.T) {
	cfg, set, f, _, _ := verificationFixture()
	f.Decision = ""
	reads := 0
	ctx := WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) {
		reads++
		return "", false, errors.New("not needed")
	})
	a, _, err := verifyCandidates(ctx, cfg, set, Analysis{Fixes: []Fix{f}}, 0)
	if err != nil || reads != 0 || len(a.Fixes) != 0 || len(a.Investigations) != 0 || len(a.Findings) != 0 || !strings.Contains(a.Insights, "Omitted candidate") {
		t.Fatalf("a candidate without a decision created work: %+v reads=%d err=%v", a, reads, err)
	}
	f.Decision = "Promote only a supported material change if more research finds something useful."
	a, _, err = verifyCandidates(ctx, cfg, set, Analysis{Fixes: []Fix{f}}, 0)
	if err != nil || len(a.Investigations) != 0 || len(a.Fixes) != 0 {
		t.Fatalf("old generic promotion boilerplate created research: %+v %v", a, err)
	}
}

func TestGeneratedInvestigationsNeedRationaleDecisionAndEvidence(t *testing.T) {
	_, set, f, _, _ := verificationFixture()
	good := Investigation{Title: "Check setup route", Why: "Startup users cannot follow a retired documentation link.", Check: f.Check, Decision: f.Decision, Evidence: f.Evidence, Prompts: []int{1}, Target: f.Target}
	noWhy, noDecision, noEvidence := good, good, good
	noWhy.Why, noDecision.Decision, noEvidence.Evidence = "", "", ""
	a := Analysis{Investigations: []Investigation{noWhy, noDecision, noEvidence, good}}
	normalizeNotes(set, &a)
	if len(a.Investigations) != 1 || a.Investigations[0].Decision != good.Decision {
		t.Fatalf("incomplete investigations retained: %+v", a.Investigations)
	}
}

func TestVerifiedBoundaryBecomesFindingRatherThanWork(t *testing.T) {
	cfg, set, f, pages, d := verificationFixture()
	f.Type = Product
	f.Title = "Add enterprise compliance features"
	f.Check = "Inspect https://acme.example/docs for the current intended audience and enterprise procurement claims."
	f.Decision = "Decide whether to retain the stated small-team scope instead of expanding into enterprise procurement."
	d.Status = "boundary"
	d.Summary = "Enterprise procurement is outside the stated product scope."
	d.Checks["strategy"] = proof{Passed: false, Source: "brand.advisory", Quote: cfg.Brand.Advisory.Boundaries[0], Reason: "The proposed enterprise expansion contradicts the explicitly accepted product boundary."}
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	structuredCLI = func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
		b, _ := json.Marshal(struct {
			Decisions []decision `json:"decisions"`
		}{[]decision{d}})
		return json.Unmarshal(b, out)
	}
	ctx := WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) { return pages[0].Text, false, nil })
	a, _, err := verifyCandidates(ctx, cfg, set, Analysis{Fixes: []Fix{f}}, 0)
	if err != nil || len(a.Fixes) != 0 || len(a.Investigations) != 0 || len(a.Findings) != 1 {
		t.Fatalf("accepted boundary became work: %+v %v", a, err)
	}
}

func TestReviewerCanDiscardCandidateWithoutResearchTask(t *testing.T) {
	cfg, set, f, pages, d := verificationFixture()
	d.Status = "reject"
	d.Summary = "The proposed wording change is immaterial to the startup setup decision and does not justify more research."
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	structuredCLI = func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
		b, _ := json.Marshal(struct {
			Decisions []decision `json:"decisions"`
		}{[]decision{d}})
		return json.Unmarshal(b, out)
	}
	ctx := WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) { return pages[0].Text, false, nil })
	a, _, err := verifyCandidates(ctx, cfg, set, Analysis{Fixes: []Fix{f}}, 0)
	if err != nil || len(a.Fixes) != 0 || len(a.Investigations) != 0 || len(a.Findings) != 0 || !strings.Contains(a.Insights, d.Summary) {
		t.Fatalf("discarded candidate created a backlog or market claim: %+v %v", a, err)
	}
}

func TestAnalysisEnvelopeAndLegacyTrust(t *testing.T) {
	cfg, set, f, _, _ := verificationFixture()
	f.Verification = analysisRecordedVerification(cfg, f)
	f = Triage(cfg, set, f)
	old, _ := json.Marshal([]Fix{f})
	a, err := DecodeAnalysis(string(old))
	if err != nil || len(a.Fixes) != 1 || a.Fixes[0].Verification.Status == "verified" || a.Fixes[0].ClaudePrompt != "" || len(a.Investigations) != 0 {
		t.Fatalf("legacy array upgraded or duplicated: %+v %v", a, err)
	}
	want := Analysis{Fixes: []Fix{f}, Investigations: []Investigation{{Title: "Check inclusion"}}, Findings: []Finding{{Title: "Intentional limitation"}}}
	a, err = DecodeAnalysis(Encode(want))
	if err != nil || len(a.Investigations) != 1 || len(a.Findings) != 1 || len(Decode(Encode(want))) != 1 || a.Fixes[0].Verification.Status != "verified" {
		t.Fatalf("envelope lost categories: %+v %v", a, err)
	}
	if _, err := DecodeAnalysis(`{"version":99}`); err == nil {
		t.Fatal("unknown version silently accepted")
	}
	if _, err := DecodeAnalysis(`broken`); err == nil {
		t.Fatal("malformed JSON silently accepted")
	}
	f.Verification.Context = ""
	a, err = DecodeAnalysis(Encode(Analysis{Fixes: []Fix{f}}))
	if err != nil || a.Fixes[0].Verification.Status == "verified" || a.Fixes[0].ClaudePrompt != "" {
		t.Fatalf("envelope lacking provenance retained readiness: %+v %v", a, err)
	}
}

func TestPersistedReadinessNeedsCompleteRecordedChecks(t *testing.T) {
	cfg, _, f, _, _ := verificationFixture()
	f.Verification = Verification{Status: "verified", Sources: []string{f.Target}}
	f.Verification.Context = VerificationContext(cfg, f.Target, f.Verification)
	a, err := DecodeAnalysis(Encode(Analysis{Fixes: []Fix{f}}))
	if err != nil || a.Fixes[0].Verification.Status == "verified" {
		t.Fatalf("a claimed status/context without checks became ready: %+v %v", a, err)
	}
	f.Verification = analysisRecordedVerification(cfg, f)
	if !f.Verification.Recorded() {
		t.Fatal("complete recorded checks rejected")
	}
	duplicate := f.Verification
	duplicate.Checks = append([]VerificationCheck(nil), f.Verification.Checks...)
	duplicate.Checks[1].Gate = duplicate.Checks[0].Gate
	if duplicate.Recorded() {
		t.Fatal("duplicated gate replaced a required check")
	}
	unread := f.Verification
	unread.Checks = append([]VerificationCheck(nil), f.Verification.Checks...)
	unread.Checks[0].Source = "https://unread.example/page"
	if unread.Recorded() {
		t.Fatal("recorded check cites a source never recorded as read")
	}
	wrongStrategy := f.Verification
	wrongStrategy.Checks = append([]VerificationCheck(nil), f.Verification.Checks...)
	for i := range wrongStrategy.Checks {
		if wrongStrategy.Checks[i].Gate == "strategy" {
			wrongStrategy.Checks[i].Source = "brand.facts"
		}
	}
	if wrongStrategy.Recorded() {
		t.Fatal("public feature facts became proof of strategic intent")
	}
	f.Verification.Sources = nil
	if f.Verification.Recorded() {
		t.Fatal("checks without a recorded artifact became ready")
	}
}

func TestProductChangeRequiresQuoteFromExplicitAdvisoryGoal(t *testing.T) {
	cfg, _, f, pages, d := verificationFixture()
	f.Type = Product
	f.Title = "Add documentation-host validation to onboarding"
	f.Claim = "Propose a narrow validation step for the configured documentation host during startup onboarding."
	if _, why := verifyDecision(d, f, cfg, pages); !strings.Contains(why, "explicit advisory goal") {
		t.Fatalf("generic positioning authorized product expansion: %q", why)
	}
	cfg.Brand.Advisory.Goals = []string{"Add documentation-host validation to startup onboarding."}
	if _, why := verifyDecision(d, f, cfg, pages); !strings.Contains(why, "explicit advisory goal") {
		t.Fatalf("unrelated positioning quote authorized expansion merely because a goal exists: %q", why)
	}
	p := d.Checks["strategy"]
	p.Quote = cfg.Brand.Advisory.Goals[0]
	p.Reason = "This specific product proposal is the documentation-host validation expressly requested by the stated goal."
	d.Checks["strategy"] = p
	v, why := verifyDecision(d, f, cfg, pages)
	if why != "" || !v.Recorded() {
		t.Fatalf("explicit goal-supported proposal rejected: %+v %s", v, why)
	}
}

func TestTriageInvalidatesChangedBriefAndHandlesOldIndexes(t *testing.T) {
	cfg, set, f, _, _ := verificationFixture()
	f.Verification = Verification{Status: "verified"}
	f.ClaudePrompt = "Old executable handoff"
	if unknown := Triage(cfg, set, f); unknown.Verification.Status == "verified" || unknown.ClaudePrompt != "" {
		t.Fatal("verification without a product context received a fresh handoff")
	}
	f.Verification = analysisRecordedVerification(cfg, f)
	f.Prompts = []int{-1, 1, 1, 999}
	f = Triage(cfg, set, f)
	if len(f.Prompts) != 1 || f.ClaudePrompt == "" {
		t.Fatalf("valid old index lost: %+v", f)
	}
	cfg.Brand.Advisory.Boundaries = append(cfg.Brand.Advisory.Boundaries, "Do not change documentation yet.")
	f = Triage(cfg, set, f)
	if f.Verification.Status == "verified" || f.ClaudePrompt != "" {
		t.Fatal("changed strategy retained executable work")
	}
}
