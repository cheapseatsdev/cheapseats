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
		return strings.Repeat("x", MaxVerificationPageRunes+100), false, nil
	})
	ps := readArtifacts(ctx, cfg, fs)
	if n != MaxVerificationPages || len(ps) != MaxVerificationPages {
		t.Fatalf("unbounded reads: %d", n)
	}
	for i, p := range ps {
		if !p.Truncated || p.Text != strings.Repeat("x", MaxVerificationPageRunes)+truncatedMarker {
			t.Fatalf("page %d (%s) not clipped to %d: %d runes, truncated=%v", i, p.URL, MaxVerificationPageRunes, len([]rune(p.Text)), p.Truncated)
		}
	}
}

// Truncated is set exactly when the text is incomplete: when verify's own clip applies, counting
// characters rather than bytes, or when the reader says it stopped short of the page. The flag
// and the marker reach the reviewer.
func TestReadArtifactsMarksTruncated(t *testing.T) {
	cfg, set, f, pages, _ := verificationFixture()
	cfg.Brand.Sites = nil
	type page struct {
		text string
		cut  bool
	}
	body := map[string]page{
		"https://acme.example/whole":      {strings.Repeat("é", MaxVerificationPageRunes), false},
		"https://acme.example/clipped":    {strings.Repeat("é", MaxVerificationPageRunes+1), false},
		"https://acme.example/reader-cut": {"A short excerpt that the reader cut.", true},
	}
	var fs []Fix
	for u := range body {
		x := f
		x.Target = u
		fs = append(fs, x)
	}
	ctx := WithVerifier(context.Background(), func(_ context.Context, u string) (string, bool, error) { return body[u].text, body[u].cut, nil })
	got := map[string]artifact{}
	for _, p := range readArtifacts(ctx, cfg, fs) {
		got[p.URL] = p
	}
	for u, want := range map[string]bool{"https://acme.example/whole": false, "https://acme.example/clipped": true, "https://acme.example/reader-cut": true} {
		p := got[u]
		if p.Truncated != want || strings.HasSuffix(p.Text, truncatedMarker) != want {
			t.Errorf("%s: truncated=%v, marker=%v, want %v", u, p.Truncated, strings.HasSuffix(p.Text, truncatedMarker), want)
		}
	}
	if p := got["https://acme.example/reader-cut"]; p.Text != body[p.URL].text+truncatedMarker {
		t.Errorf("a reader-cut page lost its text or gained more than the marker: %q", p.Text)
	}

	// The reviewer sees the flag on every supplied page.
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	var prompt string
	structuredCLI = func(_ context.Context, _, _, p string, _ map[string]any, out any) error {
		prompt = p
		return json.Unmarshal([]byte(`{"decisions":[]}`), out)
	}
	cut := WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) { return pages[0].Text, true, nil })
	if _, _, err := verifyCandidates(cut, cfg, set, Analysis{Fixes: []Fix{f}}, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"truncated":true`) || !strings.Contains(prompt, truncatedMarker) {
		t.Fatal("the reviewer was not shown the truncated flag and marker")
	}
}

// A read that stops short of the page reaches the artifact as truncated, through the app's real
// reader (audit.ReadCurrent, with no Exa Reader):
//   - raw-cap: a readable paragraph, then inline script past audit.MaxBody, then the rest;
//   - short-body: the server declares a longer Content-Length and closes after a readable prefix;
//   - shell: a page empty without JavaScript, read with no Reader to render it.
//
// A whole page served the same way is not truncated.
func TestReadStoppedShortIsTruncated(t *testing.T) {
	restore := audit.AllowLoopbackForTests()
	defer restore()
	page := "Setup. Install the CLI with go install acme.example/tracker@latest. Run tracker init, then tracker serve, to start locally. The server stores answers in a local SQLite file."
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
			cfg, _, f, _, _ := verificationFixture()
			cfg.Brand.Sites = nil
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/setup" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("content-type", "text/html; charset=utf-8")
				tc.serve(w)
			}))
			defer srv.Close()
			f.Target = srv.URL + "/setup"
			read := func(ctx context.Context, u string) (string, bool, error) {
				return audit.ReadCurrent(ctx, u, audit.CiteOptions{}) // no Reader
			}
			pages := readArtifacts(WithVerifier(context.Background(), read), cfg, []Fix{f})
			if len(pages) != 1 || pages[0].Text == "" || pages[0].Truncated != tc.truncated {
				t.Fatalf("truncated=%v, want %v: %+v", len(pages) == 1 && pages[0].Truncated, tc.truncated, pages)
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
	// A product target is never read as a page, so the reader has only the brand's site.
	cfg.Brand.Domains = []string{"acme.example"}
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
