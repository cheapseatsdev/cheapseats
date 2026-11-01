package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
)

func verifyFailureAnalysis() analysis.Analysis {
	return analysis.Analysis{Insights: "One candidate.", VerificationFailed: true, Investigations: []analysis.Investigation{{
		Title: "Check: the pricing page", Why: "w", Check: "Read the pricing page.", Decision: "d", Evidence: "e", Prompts: []int{1},
	}}}
}

func verifyFailureRow(t *testing.T, a *App, id string) (fixes, insights, fixesAt, fixesError sql.NullString, cost float64) {
	t.Helper()
	if err := a.DB.QueryRow(`SELECT fixes, insights, fixes_at, fixes_error, analysis_cost_usd FROM runs WHERE id=?`, id).Scan(&fixes, &insights, &fixesAt, &fixesError, &cost); err != nil {
		t.Fatal(err)
	}
	return
}

// A run's first analysis whose verification failed has nothing better to show: its candidates
// are stored as investigations, marked as such, with the error, so the run page, the API and
// the watch script's fixes_error check all see that the analysis didn't complete.
func TestFailedVerificationOfAFirstBuildIsStoredWithItsError(t *testing.T) {
	a, id := providersApp(t)
	ctx := context.Background()
	failed := &analysis.VerificationError{Candidates: 1, Err: errors.New("HTTP 529: overloaded")}
	if err := a.recordAnalysis(ctx, id, verifyFailureAnalysis(), 0.5, failed); err != nil {
		t.Fatal(err)
	}
	fixes, _, fixesAt, fe, cost := verifyFailureRow(t, a, id)
	if !fe.Valid || !strings.Contains(fe.String, "its 1 candidates are investigations: HTTP 529: overloaded") || !fixesAt.Valid || cost != 0.5 {
		t.Fatalf("fixes_error %v, fixes_at %v, cost %v", fe, fixesAt, cost)
	}
	if an, err := analysis.DecodeAnalysis(fixes.String); err != nil || !an.VerificationFailed || len(an.Investigations) != 1 {
		t.Fatalf("stored analysis lost its investigations or its mark: %+v %v", an, err)
	}

	// A clean rebuild replaces it and clears the error.
	if err := a.recordAnalysis(ctx, id, analysis.Analysis{Insights: "Verified."}, 0, nil); err != nil {
		t.Fatal(err)
	}
	fixes, _, _, fe, _ = verifyFailureRow(t, a, id)
	if an, _ := analysis.DecodeAnalysis(fixes.String); fe.Valid || an.VerificationFailed {
		t.Errorf("a clean analysis kept the error %v or the mark", fe)
	}
}

// A rebuild whose verifier hit a transient 529 replaced the run's verified actions with
// unverified investigations. It must keep the earlier analysis, and its fixes_at, which the
// watch script keys alerts on, and record only the error and the cost.
func TestFailedVerificationOfARebuildKeepsTheEarlierAnalysis(t *testing.T) {
	a, id := providersApp(t)
	ctx := context.Background()
	good := analysis.Analysis{Insights: "The verified analysis.", Fixes: []analysis.Fix{{Rank: 1, Title: "Add the pricing table", Target: "https://cheapseats.dev/pricing"}}}
	if err := a.recordAnalysis(ctx, id, good, 1, nil); err != nil {
		t.Fatal(err)
	}
	fixes, insights, fixesAt, _, _ := verifyFailureRow(t, a, id)

	failed := &analysis.VerificationError{Candidates: 1, Err: errors.New("HTTP 529: overloaded")}
	err := a.recordAnalysis(ctx, id, verifyFailureAnalysis(), 0.5, failed)
	if err == nil || !strings.Contains(err.Error(), "earlier analysis is kept: HTTP 529: overloaded") {
		t.Fatalf("a failed rebuild must report its error: %v", err)
	}
	afterFixes, afterInsights, afterAt, fe, cost := verifyFailureRow(t, a, id)
	if afterFixes != fixes || afterInsights != insights || afterAt != fixesAt {
		t.Errorf("the earlier analysis changed: fixes %v→%v, fixes_at %v→%v", fixes.String, afterFixes.String, fixesAt, afterAt)
	}
	if !fe.Valid || fe.String != err.Error() || cost != 1.5 {
		t.Errorf("fixes_error %v, cost %v", fe, cost)
	}

	// The same failure again changes only the cost: nothing new for the watch script to send.
	a.recordAnalysis(ctx, id, verifyFailureAnalysis(), 0.5, failed)
	if _, _, again, fe2, _ := verifyFailureRow(t, a, id); again != fixesAt || fe2 != fe {
		t.Errorf("a repeated failure changed fixes_at %v or fixes_error %v", again, fe2)
	}
}
