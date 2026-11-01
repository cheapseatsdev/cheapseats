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
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// A failed verification call kept every candidate as an investigation and returned no error, so
// the run read as a clean analysis with nothing verified and the provider's reply was lost.
func TestFailedVerificationReturnsItsErrorWithTheInvestigations(t *testing.T) {
	cfg, set, f, pages, _ := verificationFixture()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	calls := 0
	structuredCLI = func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
		calls++
		if calls == 2 {
			return errors.New(`HTTP 400: {"type":"error","error":{"message":"schema refused"}}`)
		}
		b, _ := json.Marshal(Analysis{Insights: "One candidate.", Fixes: []Fix{f}})
		return json.Unmarshal(b, out)
	}
	results := []run.Result{{ID: 7, PromptIdx: 0, Answerer: "fake", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Absent}, Answer: &answer.Answer{Text: "An answer."}}}
	ctx := WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) { return pages[0].Text, false, nil })
	a, _, err := Build(ctx, cfg, set, results, nil, nil)
	var verr *VerificationError
	if !errors.As(err, &verr) || verr.Candidates != 1 || !strings.Contains(err.Error(), "schema refused") {
		t.Fatalf("want the verifier's error: %v", err)
	}
	if calls != 2 || !a.VerificationFailed || len(a.Fixes) != 0 || len(a.Investigations) != 1 || a.Investigations[0].Check != f.Check {
		t.Fatalf("the candidate must remain an investigation: %+v", a)
	}
}

func verifyFailureBuild(t *testing.T, ctx context.Context, cfg *config.Config, set sets.Set, f Fix) (Analysis, int, error) {
	t.Helper()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	calls := 0
	structuredCLI = func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
		calls++
		b, _ := json.Marshal(Analysis{Insights: "One candidate.", Fixes: []Fix{f}})
		return json.Unmarshal(b, out)
	}
	results := []run.Result{{ID: 7, PromptIdx: 0, Answerer: "fake", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Absent}, Answer: &answer.Answer{Text: "An answer."}}}
	a, _, err := Build(ctx, cfg, set, results, nil, nil)
	return a, calls, err
}

// When the site was briefly down, every page read failed and the candidates became
// investigations with no error, so a rebuild replaced the run's verified analysis with them.
func TestUnreadablePagesAreAFailedVerification(t *testing.T) {
	cfg, set, f, _, _ := verificationFixture()
	ctx := WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) { return "", false, errors.New("HTTP 503") })
	a, calls, err := verifyFailureBuild(t, ctx, cfg, set, f)
	var verr *VerificationError
	if !errors.As(err, &verr) || !strings.Contains(err.Error(), "HTTP 503") || !a.VerificationFailed || len(a.Investigations) != 1 || calls != 1 {
		t.Fatalf("unreadable pages must fail the verification: %+v %v (%d calls)", a, err, calls)
	}
}

// No advisory is the deployment's configuration, not a failure: every candidate is an
// investigation on every run, so it must not stand as an analysis error or block a rebuild.
func TestNoAdvisoryIsACompleteAnalysisOfInvestigations(t *testing.T) {
	cfg, set, f, pages, _ := verificationFixture()
	cfg.Brand.Advisory = config.Advisory{}
	reads := 0
	ctx := WithVerifier(context.Background(), func(context.Context, string) (string, bool, error) { reads++; return pages[0].Text, false, nil })
	a, calls, err := verifyFailureBuild(t, ctx, cfg, set, f)
	if err != nil || a.VerificationFailed || len(a.Investigations) != 1 || !strings.Contains(a.Investigations[0].Why, "No advisory") {
		t.Fatalf("no advisory must give investigations without an error: %+v %v", a, err)
	}
	if reads != 0 || calls != 1 {
		t.Errorf("no advisory read %d pages and made %d calls; nothing can be verified, so neither is needed", reads, calls)
	}
}
