package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func TestEvaluationFlowConnectsSavedSessionChecksResolutionAndApproval(t *testing.T) {
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
	h := s.Handler()
	request := httptest.NewRequest("GET", "http://localhost/evaluation?import="+a.ID, nil)
	values := url.Values{"revision": {"1"}, "rubric": {evaluation.Rubric()}, "session": {session.ID}, "cursor": {"0"}, "action": {"judgment"}, "verdict": {"recommended"}, "top_pick": {"true"}, "tone": {"positive"}, "evidence": {"Use **Example Tracker** for the pilot."}, "import": {a.ID}}
	r := evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
	if r.Code != 303 {
		t.Fatalf("save: %d %s", r.Code, r.Body.String())
	}
	r = evaluationRequest(h, "GET", r.Header().Get("Location"), nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "1 / 1 finished human judgments") || !strings.Contains(r.Body.String(), `action="/evaluation/check-batches"`) {
		t.Fatalf("session handoff: %d %s", r.Code, r.Body.String())
	}
	for _, path := range []string{"/evaluation?import=" + a.ID, "/evaluation/checks?import=" + a.ID} {
		r = evaluationRequest(h, "GET", path, nil)
		if r.Code != 200 || !(strings.Contains(r.Body.String(), "Run independent checks") || strings.Contains(r.Body.String(), "Check saved judgments")) {
			t.Fatalf("missing entry point: %s %d %s", path, r.Code, r.Body.String())
		}
		if strings.Contains(r.Body.String(), "IMPORTED_MODEL_NOTE_DO_NOT_DISCLOSE") {
			t.Fatal("queue exposed a model proposal")
		}
	}
	var count int
	if err := wb.DB.QueryRow(`SELECT count(*) FROM evaluation_checks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("opening handoff pages scheduled a check: %d %v", count, err)
	}
	d, err := s.evaluationHomeData(request)
	if err != nil || d.Flow.Eligible != 1 || d.Flow.Unchecked != 1 || !strings.Contains(d.Flow.NextURL, "/evaluation/checks/"+id) {
		t.Fatalf("unchecked action: %+v %v", d.Flow, err)
	}
	p := s.evaluationAssessor().CheckPlan(id, 3)
	if _, err := wb.RunCheck(ctx, p, "operator", false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
		return grade.Grade{Verdict: grade.Absent}, 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	d, err = s.evaluationHomeData(request)
	if err != nil || d.Flow.Pending != 1 || d.Flow.NextAction != "Resolve differences" {
		t.Fatalf("disagreement not actionable: %+v %v", d.Flow, err)
	}
	r = evaluationRequest(h, "GET", d.Flow.NextURL, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Continue checking saved judgments") {
		t.Fatalf("resolution lost queue navigation: %d %s", r.Code, r.Body.String())
	}
	values = url.Values{"revision": {"2"}, "rubric": {evaluation.Rubric()}, "check": {p.ID}, "action": {"resolve-keep"}, "resolution_note": {"The complete answer explicitly recommends the intended product."}, "checks": {"1"}, "import": {a.ID}}
	r = evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
	if r.Code != 303 || !strings.Contains(r.Header().Get("Location"), "/evaluation/checks?") || !strings.Contains(r.Header().Get("Location"), "confirmed_revision=3") || !strings.Contains(r.Header().Get("Location"), a.ID) {
		t.Fatalf("resolution lost return context: %d %s", r.Code, r.Header().Get("Location"))
	}
	d, err = s.evaluationHomeData(request)
	if err != nil || d.Flow.Pending != 0 || d.Flow.Done != 1 || d.Flow.NextURL != "" {
		t.Fatalf("resolved result did not advance flow: %+v %v", d.Flow, err)
	}
	r = evaluationRequest(h, "GET", "/evaluation/checks?import="+a.ID, nil)
	if !strings.Contains(r.Body.String(), "All 1 saved judgments have completed checks") || !strings.Contains(r.Body.String(), "Preview dataset version") {
		t.Fatalf("missing approval handoff: %s", r.Body.String())
	}
	// A different model needs a new assessment, even if the old check is done.
	s.App.Cfg.Grader.Model += "-changed"
	d, err = s.evaluationHomeData(request)
	if err != nil || d.Flow.Unchecked != 1 || d.Flow.Done != 0 {
		t.Fatalf("old model counted as current check: %+v %v", d.Flow, err)
	}
	if _, err := wb.Save(ctx, id, 3, "draft", evaluation.Judgment{}, "local reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	d, err = s.evaluationHomeData(request)
	if err != nil || d.Flow.Eligible != 0 {
		t.Fatalf("reopened draft remained checkable: %+v %v", d.Flow, err)
	}
	r = evaluationRequest(h, "GET", "/evaluation/cases/"+id, nil)
	if strings.Contains(r.Body.String(), "Preview an independent check") {
		t.Fatal("reopened draft links to a check that would reject it")
	}
}

func TestEvaluationFlowKeepsFailedChecksDistinctAndExplainsExhaustedRetries(t *testing.T) {
	s, wb := evaluationServer(t)
	a, id := evaluationFixture(t, wb)
	ctx := context.Background()
	yes := true
	j := evaluation.Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{"Use **Example Tracker** for the pilot."}}
	if _, err := wb.Save(ctx, id, 0, "judgment", j, "human", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	p := s.evaluationAssessor().CheckPlan(id, 3)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := wb.RunCheck(ctx, p, "operator", attempt > 0, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
			return grade.Grade{}, 0, errors.New("fictional provider timeout")
		}); err == nil {
			t.Fatal("failed assessment lost its error")
		}
	}
	r := evaluationRequest(s.Handler(), "GET", "/evaluation/checks?import="+a.ID, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "check failed") || !strings.Contains(r.Body.String(), "View failed check") || strings.Contains(r.Body.String(), "All 1 saved judgments have completed checks") {
		t.Fatalf("failed result presented as completed: %d %s", r.Code, r.Body.String())
	}
	r = evaluationRequest(s.Handler(), "GET", "/evaluation/checks/"+id+"?import="+a.ID, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Both assessment attempts have been used") || strings.Contains(r.Body.String(), ">Retry failed or expired check</button>") {
		t.Fatalf("exhausted check offered another retry: %d %s", r.Code, r.Body.String())
	}
}

func TestEvaluationFlowDoesNotRecommendApprovalWhileAnotherGraderIsRunning(t *testing.T) {
	c := evaluation.Case{ID: "case", Review: evaluation.Review{State: "reviewed"}}
	views := []evaluation.CheckView{
		{Check: evaluation.Check{Plan: evaluation.CheckPlan{ID: "current"}, State: evaluation.CheckDone}},
		{Check: evaluation.Check{Plan: evaluation.CheckPlan{ID: "previous-model"}, State: evaluation.CheckRunning}},
	}
	var flow evaluationFlow
	flow.add(c, views, "current", "import")
	if flow.Done != 1 || flow.NextAction != "View running assessment" || flow.NextURL == "" {
		t.Fatalf("running assessment lost the next action: %+v", flow)
	}
	// A build change must not silently discard an unfinished check.
	views[1].Stale = true
	flow = evaluationFlow{}
	flow.add(c, views, "current", "import")
	if flow.NextAction != "View running assessment" || flow.NextURL == "" {
		t.Fatalf("older running assessment was silently discarded: %+v", flow)
	}
}
