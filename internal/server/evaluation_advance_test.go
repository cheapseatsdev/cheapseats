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

func TestEvaluationQueueSaveContinuesExistingSessionWithoutRepeatingCase(t *testing.T) {
	s, wb := evaluationServer(t)
	ctx := context.Background()
	var data []byte
	for i := 0; i < 30; i++ {
		r := grade.GoldenRecord{Set: "advance", Prompt: fmt.Sprintf("How should team %d track answers?", i), Brand: grade.GoldenBrand{Name: "Example Tracker"}, Answer: fmt.Sprintf("Team %d can use a spreadsheet.", i), Expected: grade.GoldenExpected{Verdict: grade.Absent}, LabeledBy: "claude"}
		r.ID = grade.GoldenID(r.Answer)
		b, _ := json.Marshal(r)
		data = append(append(data, b...), '\n')
	}
	a, err := wb.ImportJSONL(ctx, "advance fixture", data)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	first := a.Rows[0].CaseID
	firstSave := evaluationRequest(h, "POST", "/evaluation/cases/"+first, url.Values{"revision": {"0"}, "rubric": {evaluation.Rubric()}, "import": {a.ID}, "action": {"judgment-next"}, "verdict": {"absent"}, "absence_reason": {"not-named"}, "full_answer": {"on"}})
	resumed, err := wb.Resume(ctx, a.ID, "local reviewer")
	if firstSave.Code != 303 || err != nil || resumed == nil || len(resumed.CaseIDs) < 3 || resumed.CaseIDs[resumed.Cursor] == first {
		t.Fatalf("first queue save did not start a different answer: %d %+v %v", firstSave.Code, resumed, err)
	}
	session := *resumed
	for cursor := 0; cursor < 2; cursor++ {
		id := session.CaseIDs[cursor]
		r := evaluationRequest(h, "GET", "/evaluation/cases/"+id+"?import="+a.ID, nil)
		if r.Code != 200 || !strings.Contains(r.Body.String(), `value="judgment-next">Save and next`) || !strings.Contains(r.Body.String(), "Save and stay") {
			t.Fatalf("queue case has no continuation: %d %s", r.Code, r.Body.String())
		}
		values := url.Values{"revision": {"0"}, "rubric": {evaluation.Rubric()}, "import": {a.ID}, "action": {"judgment-next"}, "verdict": {"absent"}, "absence_reason": {"not-named"}, "full_answer": {"on"}}
		if cursor == 0 {
			// Invalid evidence stays on the answer and cannot move the session.
			values.Del("full_answer")
			r = evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
			current, _ := wb.Session(ctx, session.ID, "local reviewer")
			if r.Code != 400 || current.Cursor != 0 || !strings.Contains(r.Body.String(), a.ID) {
				t.Fatalf("invalid save moved queue or lost import: %d %+v", r.Code, current)
			}
			values.Set("full_answer", "on")
		}
		r = evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
		if r.Code != 303 || !strings.Contains(r.Header().Get("Location"), "/evaluation/sessions/"+session.ID) {
			t.Fatalf("queue save stayed on its case: %d %s", r.Code, r.Body.String())
		}
		current, _ := wb.Session(ctx, session.ID, "local reviewer")
		if current.Cursor != cursor+1 || current.CaseIDs[current.Cursor] == id {
			t.Fatalf("saved case repeated: %+v", current)
		}
		c, _ := wb.Case(ctx, id)
		if !c.FirstReviewed || c.Review.Revision != 1 {
			t.Fatalf("continuation wrote extra decisions: %+v", c.Review)
		}
		// A repeated POST cannot overwrite the judgment or skip another case.
		duplicate := evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
		if duplicate.Code != 409 {
			t.Fatalf("duplicate save was accepted: %d", duplicate.Code)
		}
	}
	var reviewed int
	if err := wb.DB.QueryRow(`SELECT count(*) FROM evaluation_reviews WHERE state='reviewed'`).Scan(&reviewed); err != nil || reviewed != 3 {
		t.Fatalf("unique reviewed count: %d %v", reviewed, err)
	}
	values := url.Values{"revision": {"0"}, "rubric": {evaluation.Rubric()}, "import": {a.ID}, "action": {"unresolved-next"}, "issue": {"rubric"}, "note": {"Need to clarify the rule."}}
	r := evaluationRequest(h, "POST", "/evaluation/cases/"+session.CaseIDs[2], values)
	current, _ := wb.Session(ctx, session.ID, "local reviewer")
	if r.Code != 303 || current.Cursor != 3 {
		t.Fatalf("unresolved save did not continue: %d %+v", r.Code, current)
	}
}

func TestEvaluationQueueSaveAndStayAndLastCaseCompletion(t *testing.T) {
	s, wb := evaluationServer(t)
	a, id := evaluationFixture(t, wb)
	h := s.Handler()
	values := url.Values{"revision": {"0"}, "rubric": {evaluation.Rubric()}, "import": {a.ID}, "action": {"judgment"}, "verdict": {"absent"}, "absence_reason": {"not-named"}, "full_answer": {"on"}}
	// Save and stay still edits just this answer, without creating a session.
	r := evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
	if r.Code != 303 || !strings.Contains(r.Header().Get("Location"), "/evaluation/cases/"+id) {
		t.Fatalf("save and stay advanced: %d %s", r.Code, r.Header().Get("Location"))
	}
	values.Set("action", "judgment-next")
	values.Set("revision", "1")
	r = evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
	if r.Code != 303 || !strings.Contains(r.Header().Get("Location"), "review=finished") {
		t.Fatalf("last case did not finish: %d %s", r.Code, r.Body.String())
	}
	r = evaluationRequest(h, "GET", r.Header().Get("Location"), nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "No more cases are eligible") {
		t.Fatalf("missing completion explanation: %d %s", r.Code, r.Body.String())
	}
	var calls int
	if err := wb.DB.QueryRow(`SELECT count(*) FROM evaluation_checks`).Scan(&calls); err != nil || calls != 0 {
		t.Fatalf("review continuation ran assessments: %d %v", calls, err)
	}
}
