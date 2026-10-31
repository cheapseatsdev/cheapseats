package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func TestEvaluationAssessmentsAdvanceAcrossDisagreementsAgreementsAndEdits(t *testing.T) {
	s, wb := evaluationServer(t)
	ctx := context.Background()
	var data []byte
	for i := 1; i <= 4; i++ {
		r := grade.GoldenRecord{Set: "assessment-flow", Prompt: fmt.Sprintf("Which tracker for pilot %d?", i), Brand: grade.GoldenBrand{Name: "Example Tracker"}, Answer: fmt.Sprintf("For pilot %d, choose Example Tracker.", i), Expected: grade.GoldenExpected{Verdict: grade.Absent}, LabeledBy: "claude"}
		r.ID = grade.GoldenID(r.Answer)
		b, _ := json.Marshal(r)
		data = append(append(data, b...), '\n')
	}
	a, err := wb.ImportJSONL(ctx, "assessment fixture", data)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	plans := map[string]evaluation.CheckPlan{}
	for i, row := range a.Rows {
		c, _ := wb.Case(ctx, row.CaseID)
		j := evaluation.Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{c.Record.Answer}, Note: "Original human judgment"}
		if _, err := wb.Save(ctx, c.ID, 0, "judgment", j, "local reviewer", evaluation.Rubric()); err != nil {
			t.Fatal(err)
		}
		p := s.evaluationAssessor().CheckPlan(c.ID, 3)
		plans[c.ID] = p
		if _, err := wb.RunCheck(ctx, p, "operator", false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
			if i == 0 {
				return grade.Grade{Verdict: grade.Absent, Reasoning: "Visible independent reasoning"}, 0, nil
			}
			brand := "Example Tracker"
			return grade.Grade{Verdict: grade.Recommended, TopPick: &brand, Tone: grade.Positive, Evidence: c.Record.Answer, Reasoning: "Visible independent reasoning"}, 0, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	h := s.Handler()
	first := a.Rows[0].CaseID
	// Old case bookmarks with checks must also enter the assessment workflow.
	r := evaluationRequest(h, "GET", "/evaluation/cases/"+first+"?import="+a.ID, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Review independent assessment · case 1 of 4") || !strings.Contains(r.Body.String(), `id="assessment-comparison"`) || !strings.Contains(r.Body.String(), "Visible independent reasoning") {
		t.Fatalf("assessment remains buried in generic editor: %d %s", r.Code, r.Body.String())
	}
	values := url.Values{"revision": {"1"}, "rubric": {evaluation.Rubric()}, "import": {a.ID}, "checks": {"1"}, "resolving": {"1"}, "action": {"resolve-keep"}, "check": {plans[first].ID}, "resolution_note": {"The answer explicitly recommends the intended brand."}, "verdict": {"absent"}}
	r = evaluationRequest(h, "POST", "/evaluation/cases/"+first, values)
	second := a.Rows[1].CaseID
	if r.Code != 303 || !strings.Contains(r.Header().Get("Location"), "/cases/"+second+"/resolve") {
		t.Fatalf("keeping returned to the same case: %d %s", r.Code, r.Header().Get("Location"))
	}
	r = evaluationRequest(h, "GET", r.Header().Get("Location"), nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Continue to next check") || !strings.Contains(r.Body.String(), `data-evaluation-confirmed-case="`+first+`"`) || strings.Contains(r.Body.String(), ">Save judgment</button>") {
		t.Fatalf("agreement needs redundant judgment or lost confirmation: %d %s", r.Code, r.Body.String())
	}
	c, _ := wb.Case(ctx, second)
	if c.Review.Revision != 1 {
		t.Fatal("viewing agreement wrote a human judgment")
	}
	// Legacy Save judgment forms in check mode must also advance directly.
	values = url.Values{"revision": {"1"}, "rubric": {evaluation.Rubric()}, "import": {a.ID}, "checks": {"1"}, "action": {"judgment"}, "verdict": {"recommended"}, "top_pick": {"true"}, "tone": {"positive"}, "evidence": {c.Record.Answer}, "note": {"Human revised explanation"}}
	r = evaluationRequest(h, "POST", "/evaluation/cases/"+second, values)
	if r.Code != 303 || !strings.Contains(r.Header().Get("Location"), "/cases/"+a.Rows[2].CaseID+"/resolve") {
		t.Fatalf("editing agreement did not advance: %d %s", r.Code, r.Header().Get("Location"))
	}
	// The last agreement finishes the sequence; it does not loop to case one.
	last := a.Rows[3].CaseID
	r = evaluationRequest(h, "GET", "/evaluation/cases/"+last+"/resolve?import="+a.ID, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Finish checking") {
		t.Fatalf("last assessment has no finish action: %d %s", r.Code, r.Body.String())
	}
}
