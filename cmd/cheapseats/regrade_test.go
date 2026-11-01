package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// A forced dry run says what -force adds and what that costs, and marks the forced runs; an
// unforced one points at -force for the runs it skips.
func TestRegradePlanSaysWhatForceAdds(t *testing.T) {
	forced := run.RegradePlan{Grader: "g", Answers: 6, EstimatedUSD: 0.12, Forced: 1, ForcedAnswers: 6, ForcedUSD: 0.12,
		Runs: []run.RegradeRun{{ID: "r1", CreatedAt: "2026-10-01T00:00:00Z", Answers: 6, FromMethod: "m", ToMethod: "m", EstimatedUSD: 0.12, Forced: true}}}
	var out bytes.Buffer
	printRegradePlan(&out, forced)
	for _, want := range []string{"unchanged (forced)", "-force adds 1 runs (6 answers) already graded with today's inputs, about $0.12.", "Estimated cost: $0.12"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("forced plan lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	printRegradePlan(&out, run.RegradePlan{Grader: "g", Current: 1, CurrentAnswers: 6})
	if !strings.Contains(out.String(), "1 runs (6 answers) already graded with today's inputs: skipped (-force re-grades them).") || strings.Contains(out.String(), "-force adds") {
		t.Errorf("unforced plan:\n%s", out.String())
	}
}
