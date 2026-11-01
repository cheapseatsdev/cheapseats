package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
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
	ctx := WithVerifier(context.Background(), func(context.Context, string) (string, error) { return pages[0].Text, nil })
	a, _, err := Build(ctx, cfg, set, results, nil, nil)
	var verr *VerificationError
	if !errors.As(err, &verr) || verr.Candidates != 1 || !strings.Contains(err.Error(), "schema refused") {
		t.Fatalf("want the verifier's error: %v", err)
	}
	if calls != 2 || len(a.Fixes) != 0 || len(a.Investigations) != 1 || a.Investigations[0].Check != f.Check {
		t.Fatalf("the candidate must remain an investigation: %+v", a)
	}
}
