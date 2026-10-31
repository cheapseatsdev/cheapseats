package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func TestEvaluationCommittedJudgmentCannotBeOverwrittenByAutosave(t *testing.T) {
	for _, state := range []string{"reviewed", "unresolved"} {
		t.Run(state, func(t *testing.T) {
			s, wb := evaluationServer(t)
			_, id := evaluationFixture(t, wb)
			ctx := context.Background()
			j := evaluation.Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true}
			kind := "judgment"
			if state == "unresolved" {
				kind = "unresolved"
				j.Issue = "identity"
				j.Note = "The product identity needs confirmation."
			}
			if _, err := wb.Save(ctx, id, 0, kind, j, "local reviewer", evaluation.Rubric()); err != nil {
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
			if err != nil || c.Review.State != state || c.Review.Revision != 1 || c.Review.Judgment.Expected() != j.Expected() || len(history) != 1 {
				t.Fatalf("autosave changed committed review: %+v history=%d %v", c, len(history), err)
			}
		})
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

func TestEvaluationShutdownWaitsForPausedStateAndClosesAdmission(t *testing.T) {
	s, wb := evaluationServer(t)
	imported, id := evaluationFixture(t, wb)
	ctx := context.Background()
	if _, err := wb.Save(ctx, id, 0, "judgment", evaluation.Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true}, "local reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	a := s.evaluationAssessor()
	offer, err := wb.PrepareCheckBatch(ctx, a, imported.ID, "", "local reviewer")
	if err != nil {
		t.Fatal(err)
	}
	b, err := wb.CreateCheckBatch(ctx, a, offer.Plan, "local reviewer")
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	job, claimed, err := s.claimEvaluationJob(b.Plan.ID, cancel)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		defer s.finishEvaluationJob(b.Plan.ID, job)
		finished <- wb.RunCheckBatch(workerCtx, b.Plan.ID, "local reviewer", a, false, func(callCtx context.Context, _ grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
			close(started)
			<-callCtx.Done()
			// Subscription processes can return a killed-process error instead
			// of context.Canceled. The canceled call context still marks a pause.
			return grade.Grade{}, 0, errors.New("process killed")
		})
	}()
	<-started
	s.StopEvaluationJobs()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal("shutdown was treated as provider failure", err)
	}
	check, err := wb.Check(ctx, b.Plan.Cases[0].CheckID)
	if err != nil || check.State != evaluation.CheckReady || check.Attempt != 0 {
		t.Fatalf("shutdown returned before preserving the attempt: %+v %v", check, err)
	}
	b, err = wb.CheckBatch(ctx, b.Plan.ID, "local reviewer")
	if err != nil || b.State != evaluation.CheckReady || b.LeaseUntil != "" {
		t.Fatalf("shutdown left a running batch: %+v %v", b, err)
	}
	nextCtx, stop := context.WithCancel(ctx)
	defer stop()
	if _, claimed, err := s.claimEvaluationJob("too-late", stop); err == nil || claimed || nextCtx.Err() == nil {
		t.Fatal("shutdown admitted another evaluation worker")
	}
}

func TestEvaluationEndSessionPreservesSavedDecision(t *testing.T) {
	for _, kind := range []string{"judgment", "unresolved"} {
		t.Run(kind, func(t *testing.T) {
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
			j := evaluation.Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true, Note: "Saved decision"}
			if kind == "unresolved" {
				j.Issue = "identity"
			}
			before, err := wb.Save(ctx, id, 1, kind, j, "local reviewer", evaluation.Rubric())
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{"revision": {"2"}, "rubric": {evaluation.Rubric()}, "session": {session.ID}, "cursor": {"0"}, "action": {"end"}, "note": {"An unsaved edit must not replace the decision"}}
			r := evaluationRequest(s.Handler(), "POST", "/evaluation/cases/"+id, form)
			after, err := wb.Case(ctx, id)
			history, _ := wb.History(ctx, id)
			if r.Code != 303 || err != nil || after.Review.State != before.State || after.Review.Revision != before.Revision || after.Review.Judgment.Note != j.Note || len(history) != 2 {
				t.Fatalf("End session overwrote the saved decision: status=%d case=%+v events=%d %v", r.Code, after, len(history), err)
			}
			paused, err := wb.Session(ctx, session.ID, "local reviewer")
			if err != nil || paused.State != "paused" || paused.Cursor != 0 {
				t.Fatalf("session did not pause at the same case: %+v %v", paused, err)
			}
		})
	}
}

func TestEvaluationSingleCheckPersistsPauseBeforeShutdownReturns(t *testing.T) {
	started := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer api.Close()
	t.Setenv("ANTHROPIC_API_KEY", "fake-single-check-key")
	t.Setenv(config.AnthropicBaseURLVar, api.URL)
	s, wb := evaluationServer(t)
	defer s.StopEvaluationJobs()
	_, id := evaluationFixture(t, wb)
	ctx := context.Background()
	if _, err := wb.Save(ctx, id, 0, "judgment", evaluation.Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true}, "local reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	p := s.evaluationAssessor().CheckPlan(id, 3)
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		finished <- evaluationRequest(s.Handler(), "POST", "/evaluation/checks/"+id, url.Values{"plan": {p.ID}, "max_usd": {"3"}})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("single check did not reach the fake provider")
	}
	s.StopEvaluationJobs()
	check, err := wb.Check(ctx, p.ID)
	if err != nil || check.State != evaluation.CheckReady || check.Attempt != 0 {
		t.Fatalf("shutdown failed to wait for the single check: %+v %v", check, err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("single-check handler did not finish")
	}
	var spend float64
	if err := wb.DB.QueryRow(`SELECT coalesce(sum(amount_usd),0) FROM spend_events`).Scan(&spend); err != nil || spend <= 0 {
		t.Fatal("cancellation erased provider accounting", spend, err)
	}
	check, err = wb.RunCheck(ctx, p, "local reviewer", false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
		return grade.Grade{Verdict: grade.Absent}, 0, nil
	})
	if err != nil || check.State != evaluation.CheckDone || check.Attempt != 1 {
		t.Fatalf("single check could not resume without retry after shutdown: %+v %v", check, err)
	}
}

func TestEvaluationRetiredSessionBookmarkReturnsToQueue(t *testing.T) {
	s, wb := evaluationServer(t)
	a, id := evaluationFixture(t, wb)
	ids, _ := json.Marshal([]string{id})
	if _, err := wb.DB.Exec(`INSERT INTO evaluation_sessions VALUES ('retired',?,'local reviewer',?,0,'done','old','old')`, a.ID, string(ids)); err != nil {
		t.Fatal(err)
	}
	r := evaluationRequest(s.Handler(), "GET", "/evaluation/sessions/retired", nil)
	if r.Code != 303 || r.Header().Get("Location") != "/evaluation?import="+a.ID {
		t.Fatalf("retired session reopened instead of returning to its queue: %d %s", r.Code, r.Header().Get("Location"))
	}
	r = evaluationRequest(s.Handler(), "POST", "/evaluation/cases/"+id, url.Values{"revision": {"0"}, "session": {"retired"}, "cursor": {"0"}, "rubric": {evaluation.Rubric()}, "action": {"end"}})
	if r.Code != 409 {
		t.Fatal("retired bookmark could change a decision", r.Code)
	}
}
