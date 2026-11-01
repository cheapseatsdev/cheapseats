package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
)

// A failed verification keeps its investigations and records the error, so the run page, the
// API and the watch script's fixes_error check all see that the analysis didn't complete.
func TestFailedVerificationIsStoredWithItsInvestigations(t *testing.T) {
	a, id := providersApp(t)
	ctx := context.Background()
	an := analysis.Analysis{Insights: "One candidate.", Investigations: []analysis.Investigation{{
		Title: "Check: the pricing page", Why: "w", Check: "Read the pricing page.", Decision: "d", Evidence: "e", Prompts: []int{1},
	}}}
	failed := &analysis.VerificationError{Candidates: 1, Err: errors.New("HTTP 529: overloaded")}
	if err := a.recordAnalysis(ctx, id, an, 0.5, failed); err != nil {
		t.Fatal(err)
	}
	_, fe, err := a.Fixes(ctx, id)
	if err != nil || fe == nil || !strings.Contains(*fe, "HTTP 529: overloaded") {
		t.Fatalf("fixes_error = %v, %v", fe, err)
	}
	next, err := a.RunNextSteps(ctx, id)
	if err != nil || len(next.Investigations) != 1 {
		t.Fatalf("investigations lost: %+v %v", next, err)
	}
	var cost float64
	a.DB.QueryRow(`SELECT analysis_cost_usd FROM runs WHERE id=?`, id).Scan(&cost)
	if cost != 0.5 {
		t.Errorf("cost %v", cost)
	}

	// A clean rebuild clears the error.
	if err := a.recordAnalysis(ctx, id, an, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, fe, _ := a.Fixes(ctx, id); fe != nil {
		t.Errorf("a clean analysis kept the error %q", *fe)
	}
}
