package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// omittedCandidates is the verification fixture's candidate under ten ranks and titles, all of
// which pass the filters, and the decision that verifies it.
func omittedCandidates(n int) ([]Fix, decision) {
	_, _, f, _, d := verificationFixture()
	var fs []Fix
	for i := range n {
		c := f
		c.Rank = i + 1
		c.Title = fmt.Sprintf("Replace the retired hostname, candidate %d", i+1)
		fs = append(fs, c)
	}
	return fs, d
}

// omittedReviewer makes the verifier return decisions; err fails the call instead.
func omittedReviewer(t *testing.T, ds []decision, err error) {
	t.Helper()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	structuredCLI = func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
		if err != nil {
			return err
		}
		b, _ := json.Marshal(struct {
			Decisions []decision `json:"decisions"`
		}{ds})
		return json.Unmarshal(b, out)
	}
}

func omittedPages(text string) context.Context {
	return WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) { return text, false, nil })
}

func omissionFor(t *testing.T, a Analysis, title string) Omission {
	t.Helper()
	for _, o := range a.Omitted {
		if o.Title == title {
			return o
		}
	}
	t.Fatalf("no omission for %q in %+v", title, a.Omitted)
	return Omission{}
}

// Run on 0.7.4, two analyses saved no actions and nothing said why. Every candidate now ends as
// a fix or an omission naming the stage that stopped it, what it became and the specific reason.
func TestOmittedRecordsEveryStageOutcomeAndReason(t *testing.T) {
	cfg, set, _, pages, _ := verificationFixture()
	fs, verified := omittedCandidates(10)

	// Four the filters drop, each for its own reason.
	placeholder, unnamed, empty, unknown := fs[0], fs[0], fs[0], fs[0]
	placeholder.Title = "TBD"
	unnamed.Title, unnamed.Check = "Check a page it does not name", "Inspect the documentation page to confirm whether installation links still use the retired hostname."
	empty.Title, empty.Claim, empty.DoneWhen = "Candidate with no claim written", "", ""
	unknown.Title, unknown.Type = "Candidate of a type nobody knows", Type("blog-post")

	// Ten that pass: ranks 9 and 10 are cut, the other eight reviewed one decision each.
	ds := make([]decision, 8)
	for i := range ds {
		ds[i] = verified
		ds[i].Index = i
		ds[i].Checks = map[string]proof{}
		for g, p := range verified.Checks {
			ds[i].Checks[g] = p
		}
	}
	ds[1].Status, ds[1].Summary = "reject", "The wording change is immaterial to the setup decision."
	ds[2].Status, ds[2].Summary = "boundary", "Kept small-team scope on purpose."
	ds[3].Checks["remaining_change"] = proof{Passed: false, Source: fs[3].Target, Quote: "x", Reason: "The page already uses the current host."}
	ds[4].Status, ds[4].Summary = "investigate", "Needs the full docs inventory first."
	ds[5].Index = 99 // no decision for candidate 5
	ds[6].Status, ds[6].Summary = "already-done", "The link was fixed last week."
	ds[6].Checks["existing_work"] = proof{Passed: true, Source: fs[6].Target, Quote: "a quote that is not on the page", Reason: "The page already links the supported host."}
	ds[7].Checks["eligibility"] = proof{Passed: true, Source: fs[7].Target, Quote: "words the page never says", Reason: "Owned documentation needs no publisher eligibility."}
	omittedReviewer(t, ds, nil)

	in := append([]Fix{placeholder, unnamed, empty, unknown}, fs...)
	a, _, err := verifyCandidates(omittedPages(pages[0].Text), cfg, set, Analysis{Insights: "Report.", Fixes: in}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Fixes) != 1 || a.Fixes[0].Title != fs[0].Title {
		t.Fatalf("want candidate 1 verified, got %+v", a.Fixes)
	}
	if got := len(a.Fixes) + len(a.Omitted); got != len(in) {
		t.Fatalf("%d candidates proposed, %d accounted for: %+v", len(in), got, a.Omitted)
	}
	for _, c := range []struct {
		title, stage, outcome, reason string
	}{
		{"TBD", StageFilter, OutcomeDropped, "Its title is a placeholder."},
		{unnamed.Title, StageFilter, OutcomeDropped, "its check does not name its target URL."},
		{empty.Title, StageFilter, OutcomeDropped, "it has no claim, done_when."},
		{unknown.Title, StageFilter, OutcomeDropped, "its type is not new-page, fix-page, positioning, outreach or product."},
		{fs[8].Title, StageCap, OutcomeDropped, "ranked 9; only the 8 best-ranked candidates are reviewed."},
		{fs[9].Title, StageCap, OutcomeDropped, "ranked 10; only the 8 best-ranked candidates are reviewed."},
		{fs[1].Title, StageReview, OutcomeRejected, "reject: The wording change is immaterial to the setup decision."},
		{fs[2].Title, StageReview, OutcomeFinding, "boundary: Kept small-team scope on purpose."},
		{fs[3].Title, StageReview, OutcomeInvestigation, "verification failed at remaining_change: the reviewer marked it not passed. The page already uses the current host."},
		{fs[4].Title, StageReview, OutcomeInvestigation, "investigate: Needs the full docs inventory first."},
		{fs[5].Title, StageReview, OutcomeInvestigation, "the reviewer returned 0 decisions for it, not one."},
		{fs[6].Title, StageReview, OutcomeInvestigation, "already-done, but its quoted basis did not check out: The link was fixed last week."},
		{fs[7].Title, StageReview, OutcomeInvestigation, "verification failed at eligibility: its quote, source or reason did not check out."},
	} {
		o := omissionFor(t, a, c.title)
		if o.Stage != c.stage || o.Outcome != c.outcome || o.Reason != c.reason {
			t.Errorf("%q: got %s/%s %q, want %s/%s %q", c.title, o.Stage, o.Outcome, o.Reason, c.stage, c.outcome, c.reason)
		}
		if o.Target == "" || o.Type == "" {
			t.Errorf("%q: omission lacks its target or type: %+v", c.title, o)
		}
	}
	// A finding or investigation made from a candidate is in its own list; the omission is brief.
	if len(a.Findings) != 1 || len(a.Investigations) != 5 {
		t.Errorf("want 1 finding and 5 investigations from candidates, got %d and %d", len(a.Findings), len(a.Investigations))
	}

	tally := a.Tally()
	want := Tally{Candidates: 14, Filtered: 4, Capped: 2, Reviewed: 8, Verified: 1, Rejected: 1, Findings: 1, Investigations: 5}
	if len(tally.FilteredBy) != 4 || tally.FilteredBy["Its title is a placeholder."] != 1 {
		t.Errorf("filtered by %v, want each of the four reasons once", tally.FilteredBy)
	}
	tally.FilteredBy, want.FilteredBy = nil, nil
	if fmt.Sprint(tally) != fmt.Sprint(want) {
		t.Errorf("tally = %+v, want %+v", tally, want)
	}
}

// When review can't run, each candidate is an investigation whose omission says why.
func TestOmittedSaysWhyReviewDidNotRun(t *testing.T) {
	fs, _ := omittedCandidates(1)
	for name, c := range map[string]struct {
		setup func(*config.Config) context.Context
		want  string
	}{
		"no advisory": {func(cfg *config.Config) context.Context {
			cfg.Brand.Advisory = config.Advisory{}
			return omittedPages("text")
		}, "not reviewed: no advisory positioning, goals or boundaries are configured."},
		"no page": {func(*config.Config) context.Context {
			return WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) { return "", false, errors.New("refused") })
		}, "not reviewed: no current page could be read."},
		"call failed": {func(*config.Config) context.Context {
			omittedReviewer(t, nil, errors.New("HTTP 529"))
			return omittedPages("text")
		}, "not reviewed: the verification call failed (call_failed)."},
	} {
		cfg, set, _, _, _ := verificationFixture()
		a, _, _ := verifyCandidates(c.setup(cfg), cfg, set, Analysis{Fixes: fs}, 0)
		if len(a.Omitted) != 1 || a.Omitted[0].Stage != StageReview || a.Omitted[0].Outcome != OutcomeInvestigation || a.Omitted[0].Reason != c.want {
			t.Errorf("%s: %+v, want reason %q", name, a.Omitted, c.want)
		}
	}
}

func TestOmittedReasonIsCut(t *testing.T) {
	cfg, set, _, pages, _ := verificationFixture()
	fs, d := omittedCandidates(1)
	d.Status, d.Summary = "reject", strings.Repeat("long ", 200)
	omittedReviewer(t, []decision{d}, nil)
	a, _, _ := verifyCandidates(omittedPages(pages[0].Text), cfg, set, Analysis{Fixes: fs}, 0)
	if r := []rune(a.Omitted[0].Reason); len(r) != MaxOmissionReason || r[len(r)-1] != '…' {
		t.Errorf("reason is %d characters, want %d ending in an ellipsis: %q", len(r), MaxOmissionReason, a.Omitted[0].Reason)
	}
}

// The envelope keeps the list, and an analysis stored before it decodes with none.
func TestOmittedRoundTripsAndOldEnvelopeLoads(t *testing.T) {
	o := Omission{Title: "Replace the retired hostname", Type: FixPage, Target: "https://acme.example/docs", Stage: StageReview, Outcome: OutcomeRejected, Reason: "reject: immaterial."}
	a, err := DecodeAnalysis(Encode(Analysis{Insights: "Report.", Omitted: []Omission{o}}))
	if err != nil || len(a.Omitted) != 1 || a.Omitted[0] != o {
		t.Fatalf("omitted did not round-trip: %+v %v", a.Omitted, err)
	}
	old := `{"version":1,"insights":"Earlier.","fixes":[],"investigations":[],"market_findings":[]}`
	if a, err := DecodeAnalysis(old); err != nil || len(a.Omitted) != 0 || a.Insights != "Earlier." {
		t.Fatalf("an analysis stored before omitted: %+v %v", a, err)
	}
	if strings.Contains(Encode(Analysis{}), "omitted") {
		t.Error("an analysis with no omissions writes the field")
	}
}

// One line per build says what became of the candidates, with the run and the cost.
func TestAnalysisBuiltIsLogged(t *testing.T) {
	cfg, set, _, pages, _ := verificationFixture()
	cfg.Analyst = config.Model{Model: "claude-opus-5-5"}
	cfg.Pricing = map[string]config.Pricing{"claude-opus-5-5": {Input: 5, Output: 25}}
	fs, d := omittedCandidates(2)
	noCheck := fs[1]
	noCheck.Title, noCheck.Check = "Check a page it does not name", "Inspect the documentation page to confirm whether installation links still use the retired hostname."
	analyst, _ := json.Marshal(Analysis{Insights: "Answers favour tools with a free tier.", Fixes: []Fix{fs[0], noCheck}})
	reviewed, _ := json.Marshal(struct {
		Decisions []decision `json:"decisions"`
	}{[]decision{d}})
	buf := fakeAnalyst(t, emptyReply(string(analyst), 1000, 100), emptyReply(string(reviewed), 2000, 200))
	ctx := WithVerifier(llm.WithOperation(context.Background(), "analysis/r-built"), func(context.Context, string) (string, bool, error) { return pages[0].Text, false, nil })
	a, cost, err := Build(ctx, cfg, set, oneResult, nil, nil)
	if err != nil || len(a.Fixes) != 1 {
		t.Fatalf("%+v %v", a, err)
	}
	log := buf.String()
	for _, want := range []string{`msg="analysis built"`, "run=r-built", "candidates=2", "filtered=1", `filtered_by="its check does not name its target URL. (1)"`, "capped=0", "reviewed=1", "verified=1", "rejected=0", "findings=0", "investigations=0", fmt.Sprintf("cost_usd=%.4f", cost)} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %s:\n%s", want, log)
		}
	}
	if cost != 0.0225 { // 3000 input and 300 output tokens at 5 and 25 per million
		t.Errorf("cost = %v, want 0.0225", cost)
	}
}

// A reviewer that says verified with no summary leaves the candidate an investigation, and the
// omission says that rather than "verified: ".
func TestOmittedVerifiedWithoutSummary(t *testing.T) {
	cfg, set, _, pages, _ := verificationFixture()
	fs, d := omittedCandidates(1)
	d.Summary = "  "
	omittedReviewer(t, []decision{d}, nil)
	a, _, err := verifyCandidates(omittedPages(pages[0].Text), cfg, set, Analysis{Fixes: fs}, 0)
	if err != nil || len(a.Fixes) != 0 || len(a.Investigations) != 1 {
		t.Fatalf("%+v %v", a, err)
	}
	o := a.Omitted[0]
	if o.Stage != StageReview || o.Outcome != OutcomeInvestigation || o.Reason != "the reviewer marked it verified but gave no summary." {
		t.Errorf("omission = %+v", o)
	}
}

// A failed verification's error can carry the claude CLI's result, its stderr, a provider's
// reply or a page's read error. The log line and the stored omissions name its category only.
func TestVerificationFailureTextStaysOutOfLogAndOmitted(t *testing.T) {
	const secret = "sk-ant-SENTINEL-0123456789 and an answer quoting the run"
	for _, c := range []struct {
		name     string
		err      error // the verifier call's error, or nil for an unreadable page
		category string
	}{
		{"cli result", fmt.Errorf("claude: error_during_execution: %s", secret), "call_failed"},
		{"provider reply", &llm.APIError{Status: 529, Body: secret}, "http_529"},
		{"timeout", fmt.Errorf("claude: %w: %s", context.DeadlineExceeded, secret), "timeout"},
		{"decode", fmt.Errorf("%s: %w", secret, &json.SyntaxError{}), "decode"},
		{"unreadable page", nil, "no_pages"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, set, f, pages, _ := verificationFixture()
			analyst, _ := json.Marshal(Analysis{Insights: "Answers favour tools with a free tier.", Fixes: []Fix{f}})
			calls := 0
			old := structuredCLI
			t.Cleanup(func() { structuredCLI = old })
			structuredCLI = func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
				calls++
				if calls == 1 {
					return json.Unmarshal(analyst, out)
				}
				return c.err
			}
			read := func(context.Context, string) (string, bool, error) { return pages[0].Text, false, nil }
			if c.err == nil {
				read = func(context.Context, string) (string, bool, error) { return "", false, errors.New(secret) }
			}
			buf := captureLog(t)
			ctx := WithVerifier(llm.WithOperation(context.Background(), "analysis/r-secret"), read)
			a, _, err := Build(ctx, cfg, set, []run.Result{oneResult[0]}, nil, nil)
			var verr *VerificationError
			if !errors.As(err, &verr) {
				t.Fatalf("want a VerificationError, got %v", err)
			}
			log := buf.String()
			stored := Encode(a)
			if strings.Contains(log, "SENTINEL") || strings.Contains(Encode(Analysis{Omitted: a.Omitted}), "SENTINEL") {
				t.Errorf("the failure's text reached the log or omitted:\n%s\n%s", log, stored)
			}
			if !strings.Contains(log, "verify_error="+c.category) {
				t.Errorf("log lacks verify_error=%s:\n%s", c.category, log)
			}
			want := "not reviewed: the verification call failed (" + c.category + ")."
			if c.err == nil {
				want = "not reviewed: no current page could be read."
			}
			if len(a.Omitted) != 1 || a.Omitted[0].Reason != want {
				t.Errorf("omitted = %+v, want reason %q", a.Omitted, want)
			}
		})
	}
}
