package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// Every schema this package sends must be one Anthropic structured outputs accept. The claude CLI
// takes keywords the API answers with an HTTP 400, so tests through that path never see them.
func TestAnalysisSchemasFitStructuredOutputs(t *testing.T) {
	for name, s := range map[string]map[string]any{
		"schema":       schema,
		"reviewSchema": reviewSchema,
	} {
		if err := llm.CheckSchema(s); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The schema can't cap the fix list, so Build does: past MaxCandidates, the worst ranked go.
func TestBuildKeepsOnlyTheBestRankedCandidates(t *testing.T) {
	cfg, set, f, _, _ := verificationFixture()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	calls := 0
	structuredCLI = func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
		calls++
		var fs []Fix
		for rank := MaxCandidates + 2; rank >= 1; rank-- {
			x := f
			x.Rank = rank
			x.Title = fmt.Sprintf("candidate %d", rank)
			fs = append(fs, x)
		}
		b, _ := json.Marshal(Analysis{Insights: "Many candidates.", Fixes: fs})
		return json.Unmarshal(b, out)
	}
	results := []run.Result{{ID: 7, PromptIdx: 0, Answerer: "fake", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Absent}, Answer: &answer.Answer{Text: "An answer."}}}
	ctx := WithVerifier(context.Background(), func(context.Context, string) (string, error) { return "", errors.New("not read") })
	a, _, err := Build(ctx, cfg, set, results, nil, nil)
	// No page can be read, so verification fails with the candidates kept as investigations.
	var verr *VerificationError
	if !errors.As(err, &verr) || verr.Candidates != MaxCandidates || calls != 1 || len(a.Investigations) != MaxCandidates {
		t.Fatalf("want %d candidates kept: %d investigations, %d calls, %v", MaxCandidates, len(a.Investigations), calls, err)
	}
	for _, inv := range a.Investigations {
		for _, dropped := range []string{fmt.Sprintf("candidate %d", MaxCandidates+1), fmt.Sprintf("candidate %d", MaxCandidates+2)} {
			if inv.Title == "Check: "+dropped {
				t.Fatalf("kept the worst ranked %q", dropped)
			}
		}
	}
}
