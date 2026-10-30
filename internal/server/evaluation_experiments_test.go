package server

import (
	"context"
	"encoding/json"
	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"net/url"
	"strings"
	"testing"
)

func TestEvaluationComparisonMasksReferenceAndKeepsDatasetFrozen(t *testing.T) {
	s, wb := evaluationServer(t)
	ctx := context.Background()
	var data []byte
	for _, answer := range []string{"Use Example Tracker for this pilot.", "Choose Example Tracker for this pilot."} {
		r := grade.GoldenRecord{Set: "experiment", Prompt: "Which tracker fits this pilot?", Brand: grade.GoldenBrand{Name: "Example Tracker"}, Answer: answer, Expected: grade.GoldenExpected{Verdict: grade.Absent}, LabeledBy: "claude"}
		r.ID = grade.GoldenID(answer)
		b, _ := json.Marshal(r)
		data = append(data, b...)
		data = append(data, '\n')
	}
	imported, err := wb.ImportJSONL(ctx, "comparison fixture", data)
	if err != nil {
		t.Fatal(err)
	}
	members := map[string]int{}
	yes := true
	for _, row := range imported.Rows {
		c, _ := wb.Case(ctx, row.CaseID)
		j := evaluation.Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{c.Record.Answer}, Note: "HIDDEN_HUMAN_REFERENCE_NOTE"}
		if _, err := wb.Save(ctx, c.ID, 0, "judgment", j, "human", evaluation.Rubric()); err != nil {
			t.Fatal(err)
		}
		members[c.ID] = 1
	}
	d, err := wb.Approve(ctx, evaluation.Approval{ImportID: imported.ID, Name: "fixture", Members: members}, "approver")
	if err != nil {
		t.Fatal(err)
	}
	a := s.evaluationAssessor()
	p, err := wb.PrepareExperiment(ctx, a, d.ID, d.Members[0].Group.Partition, []evaluation.Variant{{Name: "SECRET_CANDIDATE_IDENTITY", Prompt: grade.System + "\nCANDIDATE_PROMPT_SENTINEL"}}, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	h := s.Handler()
	response := evaluationRequest(h, "POST", "/evaluation/experiments", url.Values{"plan": {string(raw)}})
	if response.Code != 303 {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	path := response.Header().Get("Location")
	response = evaluationRequest(h, "GET", path, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Comparison incomplete") || !strings.Contains(response.Body.String(), "</html>") {
		t.Fatalf("report: %d %s", response.Code, response.Body.String())
	}
	if err := wb.RunExperiment(ctx, p.ID, a, false, func(_ context.Context, r grade.GoldenRecord, system, _ string, _ float64) (grade.Grade, float64, error) {
		if system == grade.System {
			return grade.Grade{Verdict: grade.Absent}, 0, nil
		}
		brand := r.Brand.Name
		return grade.Grade{Verdict: grade.Recommended, TopPick: &brand, Tone: grade.Positive, Evidence: r.Answer}, 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	response = evaluationRequest(h, "GET", path, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "100%") || !strings.Contains(response.Body.String(), "insufficient coverage") || !strings.Contains(response.Body.String(), "</html>") {
		t.Fatalf("class metrics: %d %s", response.Code, response.Body.String())
	}
	reviewPath := path + "/review/" + d.Members[0].CaseID
	response = evaluationRequest(h, "GET", reviewPath, nil)
	if response.Code != 200 {
		t.Fatalf("review: %d %s", response.Code, response.Body.String())
	}
	for _, secret := range []string{"SECRET_CANDIDATE_IDENTITY", "HIDDEN_HUMAN_REFERENCE_NOTE", "CANDIDATE_PROMPT_SENTINEL", "Production control"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Errorf("blinded page disclosed %s", secret)
		}
	}
	values := url.Values{"revision": {"0"}, "rubric": {evaluation.Rubric()}, "action": {"unresolved"}, "issue": {"rubric"}, "note": {"I need a clearer recommendation rule."}}
	response = evaluationRequest(h, "POST", reviewPath, values)
	if response.Code != 303 {
		t.Fatalf("unresolved: %d %s", response.Code, response.Body.String())
	}
	response = evaluationRequest(h, "GET", reviewPath, nil)
	if strings.Contains(response.Body.String(), "SECRET_CANDIDATE_IDENTITY") {
		t.Fatal("unresolved review revealed identities")
	}
	values.Set("revision", "1")
	values.Set("action", "judgment")
	values.Set("verdict", "recommended")
	values.Set("top_pick", "true")
	values.Set("tone", "positive")
	values.Set("evidence", d.Members[0].Record.Answer)
	values.Set("note", "The answer explicitly recommends the intended brand.")
	response = evaluationRequest(h, "POST", reviewPath, values)
	if response.Code != 303 {
		t.Fatalf("judgment: %d %s", response.Code, response.Body.String())
	}
	response = evaluationRequest(h, "GET", reviewPath, nil)
	if !strings.Contains(response.Body.String(), "SECRET_CANDIDATE_IDENTITY") || !strings.Contains(response.Body.String(), "HIDDEN_HUMAN_REFERENCE_NOTE") {
		t.Fatal("committed comparison review did not reveal identities and reference")
	}
	frozen, _ := wb.Dataset(ctx, d.ID)
	if frozen.Members[0].Revision != 1 {
		t.Fatal("comparison review changed approved dataset")
	}
	s.evaluation = false
	for _, route := range []string{path, path + "/report.json", reviewPath, "/evaluation/experiments/new?dataset=" + d.ID} {
		response = evaluationRequest(s.Handler(), "GET", route, nil)
		if response.Code != 404 {
			t.Fatalf("disabled route: %s %d", route, response.Code)
		}
	}
}
