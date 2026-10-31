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

func TestEvaluationCommittedJudgmentCannotBeOverwrittenByAutosave(t *testing.T) {
	s, wb := evaluationServer(t)
	_, id := evaluationFixture(t, wb)
	ctx := context.Background()
	j := evaluation.Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true}
	if _, err := wb.Save(ctx, id, 0, "judgment", j, "local reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	page := evaluationRequest(h, "GET", "/evaluation/cases/"+id, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "data-committed") {
		t.Fatal("committed form did not disable automatic server drafts")
	}
	values := url.Values{"revision": {"1"}, "rubric": {evaluation.Rubric()}, "note": {"an unfinished edit"}}
	response := evaluationRequest(h, "POST", "/api/evaluation/cases/"+id+"/draft", values)
	if response.Code == 200 {
		t.Fatal("autosave replaced committed judgment")
	}
	c, err := wb.Case(ctx, id)
	history, _ := wb.History(ctx, id)
	if err != nil || c.Review.State != "reviewed" || c.Review.Revision != 1 || c.Review.Judgment.Expected() != j.Expected() || len(history) != 1 {
		t.Fatalf("autosave changed committed review: %+v history=%d %v", c, len(history), err)
	}
}

func TestEvaluationReopenedSavedSessionOffersContinueWithoutAnotherDecision(t *testing.T) {
	s, wb := evaluationServer(t)
	a, id := evaluationFixture(t, wb)
	ctx := context.Background()
	if _, err := wb.Save(ctx, id, 0, "draft", evaluation.Judgment{}, "local reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	session, err := wb.Start(ctx, a.ID, "local reviewer", "attention")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate save success followed by a lost advance/HTTP response.
	if _, err := wb.Save(ctx, id, 1, "judgment", evaluation.Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true}, "local reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	page := evaluationRequest(h, "GET", "/evaluation/sessions/"+session.ID, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), ">Continue session</button>") || strings.Contains(page.Body.String(), ">Save and next</button>") {
		t.Fatalf("reopened session asks to save again: %d", page.Code)
	}
	values := url.Values{"revision": {"2"}, "rubric": {evaluation.Rubric()}, "session": {session.ID}, "cursor": {"0"}, "action": {"next"}}
	response := evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
	history, _ := wb.History(ctx, id)
	if response.Code != 303 || len(history) != 2 {
		t.Fatalf("continue duplicated history: status=%d events=%d", response.Code, len(history))
	}
}

func TestEvaluationSourceLinksRequireMatchingLocalEvidence(t *testing.T) {
	s, wb := evaluationServer(t)
	ctx := context.Background()
	run, _ := evaluationImportRunFixture(t, s)
	preview, err := s.App.EvaluationRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := *preview.Answers[0].Record
	r.Source.Run = "maintainer-only-run"
	data, _ := json.Marshal(r)
	imported, err := wb.ImportJSONL(ctx, "foreign fixture", data)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	page := evaluationRequest(h, "GET", "/evaluation/cases/"+imported.Rows[0].CaseID, nil)
	if page.Code != 200 || strings.Contains(page.Body.String(), "Original source run and answer</a>") {
		t.Fatal("foreign fixture links to a nonexistent local run")
	}
	// Matching numeric IDs alone are insufficient; a different answer must not
	// point at an unrelated local result.
	r.Source.Run = run.ID
	r.Answer = "Different imported answer"
	r.ID = grade.GoldenID(r.Answer)
	data, _ = json.Marshal(r)
	imported, err = wb.ImportJSONL(ctx, "foreign answer", data)
	if err != nil {
		t.Fatal(err)
	}
	page = evaluationRequest(h, "GET", "/evaluation/cases/"+imported.Rows[0].CaseID, nil)
	if page.Code != 200 || strings.Contains(page.Body.String(), "Original source run and answer</a>") {
		t.Fatal("unrelated local answer was linked")
	}
	// Case identity deliberately ignores source IDs; a prior foreign import may
	// own the same case. Use another real saved result to exercise a fresh link.
	localID := preview.Answers[1].ID
	local, err := s.App.ImportEvaluationRun(ctx, run.ID, []int64{localID}, preview.Digest)
	if err != nil {
		t.Fatal(err)
	}
	page = evaluationRequest(h, "GET", "/evaluation/cases/"+local.Rows[0].CaseID, nil)
	want := fmt.Sprintf("/runs/%s#result-%d", run.ID, localID)
	if page.Code != 200 || !strings.Contains(page.Body.String(), want) {
		c, _ := wb.Case(ctx, local.Rows[0].CaseID)
		t.Fatalf("real local source link disappeared: status=%d want=%s source=%+v question=%q answer=%q page=%s", page.Code, want, c.Record.Source, c.Record.Prompt, c.Record.Answer, limit(page.Body.String()))
	}
}
