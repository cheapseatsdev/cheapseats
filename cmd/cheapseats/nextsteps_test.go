package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/app"
)

// A failed rebuild that kept the earlier analysis says so once: the analysis error printed above
// the lists already says which analysis is shown, so the closing error doesn't repeat it. When no
// analysis error said it, the closing error still does.
func TestNextStepsKeptAnalysisNoteOnce(t *testing.T) {
	kept := &app.KeptAnalysisError{Err: errors.New("schema refused")}
	failed := app.AnalysisError{RunID: "r1", ShownRunID: "r1", Shown: app.ShownEarlier, AnalysisFailure: app.AnalysisFailure{Message: "schema refused"}}
	for _, tc := range []struct {
		name string
		next app.NextSteps
	}{
		{"with the analysis error", app.NextSteps{Errors: []app.AnalysisError{failed}}},
		{"without it", app.NextSteps{}},
	} {
		var stdout, stderr bytes.Buffer
		err := printNextSteps(&stdout, &stderr, "Set", true, tc.next, kept)
		if err == nil || !errors.Is(err, kept) || !strings.Contains(err.Error(), "next steps rebuild failed: schema refused") {
			t.Fatalf("%s: err = %v, want the kept rebuild failure", tc.name, err)
		}
		all := stdout.String() + stderr.String() + err.Error()
		if n := strings.Count(all, app.KeptAnalysisNote); n != 1 {
			t.Errorf("%s: %q appears %d times, want once:\n%s", tc.name, app.KeptAnalysisNote, n, all)
		}
	}
}

// A finding's evidence prints one clean quote a line, labelled with its result; prose evidence
// prints inline. Result IDs are dummies.
func TestNextStepsPrintsEvidenceQuotes(t *testing.T) {
	next := app.NextSteps{Findings: []app.NextFinding{
		{Finding: analysis.Finding{Title: "Quotes", Detail: "d", Evidence: `201: "Starter is $95/month". 202: "Add-ons cost about **$76/month**".`}},
		{Finding: analysis.Finding{Title: "Prose", Detail: "d", Evidence: `Answers call it \"cheap\".`}},
	}}
	var stdout, stderr bytes.Buffer
	if err := printNextSteps(&stdout, &stderr, "Set", true, next, nil); err != nil {
		t.Fatal(err)
	}
	want := "  - Evidence:\n    - result 201: “Starter is $95/month”\n    - result 202: “Add-ons cost about $76/month”\n"
	if !strings.Contains(stdout.String(), want) || !strings.Contains(stdout.String(), "  - Evidence: Answers call it \"cheap\".\n") {
		t.Fatalf("got:\n%s", stdout.String())
	}
}
