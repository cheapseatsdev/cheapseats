package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

func evaluationServer(t *testing.T) (*Server, *evaluation.Workbench) {
	t.Helper()
	t.Setenv("CHEAPSEATS_EVALUATION", "1")
	t.Setenv("CHEAPSEATS_TOKENS", "")
	t.Setenv("CHEAPSEATS_AUTH_HEADER", "")
	t.Setenv("CHEAPSEATS_READONLY", "")
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := New(newApp(t, testConfig(t), db))
	if err != nil {
		t.Fatal(err)
	}
	return s, evaluation.New(db.DB)
}

func evaluationRequest(h http.Handler, method, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(values.Encode()))
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func evaluationFixture(t *testing.T, wb *evaluation.Workbench) (evaluation.Import, string) {
	t.Helper()
	r := grade.GoldenRecord{Set: "example", Answer: "Use **Example Tracker** for the pilot. <script>alert('untrusted-answer')</script>", Brand: grade.GoldenBrand{Name: "Example Tracker"}, Prompt: "Which tracker should I choose?", Expected: grade.GoldenExpected{Verdict: grade.Recommended, TopPick: true}, Note: "IMPORTED_MODEL_NOTE_DO_NOT_DISCLOSE", Evidence: "IMPORTED_MODEL_EVIDENCE_DO_NOT_DISCLOSE", LabeledBy: "claude"}
	r.ID = grade.GoldenID(r.Answer)
	b, _ := json.Marshal(r)
	a, err := wb.ImportJSONL(context.Background(), "server fixture", b)
	if err != nil || a.Blocked != 0 {
		t.Fatalf("import: %+v %v", a, err)
	}
	return a, a.Rows[0].CaseID
}

func TestEvaluationGatedAndModelProposalNeverSentBeforeJudgment(t *testing.T) {
	s, wb := evaluationServer(t)
	_, id := evaluationFixture(t, wb)
	h := s.Handler()
	for _, path := range []string{"/evaluation", "/evaluation/cases/" + id} {
		r := evaluationRequest(h, "GET", path, nil)
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
		}
		for _, secret := range []string{"IMPORTED_MODEL_NOTE_DO_NOT_DISCLOSE", "IMPORTED_MODEL_EVIDENCE_DO_NOT_DISCLOSE", "<script>alert('untrusted-answer')</script>"} {
			if strings.Contains(r.Body.String(), secret) {
				t.Fatalf("first review leaked %s", secret)
			}
		}
	}
	var groups int
	if err := wb.DB.QueryRow(`SELECT count(*) FROM evaluation_groupings`).Scan(&groups); err != nil || groups != 0 {
		t.Fatal("viewing pages wrote grouping state")
	}
	values := url.Values{"revision": {"0"}, "rubric": {evaluation.Rubric()}, "note": {"typed draft"}}
	r := evaluationRequest(h, "POST", "/api/evaluation/cases/"+id+"/draft", values)
	if r.Code != 200 {
		t.Fatalf("draft: %d %s", r.Code, r.Body.String())
	}
	c, _ := wb.Case(context.Background(), id)
	if c.FirstReviewed {
		t.Fatal("autosave promoted a model proposal")
	}
	r = evaluationRequest(h, "GET", "/evaluation/cases/"+id, nil)
	if strings.Contains(r.Body.String(), "IMPORTED_MODEL_NOTE_DO_NOT_DISCLOSE") {
		t.Fatal("draft unlocked model proposal")
	}
	values.Set("revision", "1")
	values.Set("action", "judgment")
	values.Set("verdict", "recommended")
	values.Set("top_pick", "true")
	values.Set("tone", "positive")
	values.Set("evidence", "Use **Example Tracker** for the pilot.")
	r = evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
	if r.Code != 303 {
		t.Fatalf("judgment: %d %s", r.Code, r.Body.String())
	}
	r = evaluationRequest(h, "GET", "/evaluation/cases/"+id, nil)
	if !strings.Contains(r.Body.String(), "IMPORTED_MODEL_NOTE_DO_NOT_DISCLOSE") {
		t.Fatal("saved judgment did not unlock comparison")
	}
	// A stale tab retains its typed work but cannot replace the saved decision.
	values.Set("note", "KEEP_THIS_STALE_TAB_TEXT")
	r = evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
	if r.Code != 409 || !strings.Contains(r.Body.String(), "KEEP_THIS_STALE_TAB_TEXT") || !strings.Contains(r.Body.String(), "Reload saved fields") {
		t.Fatalf("conflict lost recovery: %d", r.Code)
	}
	c, _ = wb.Case(context.Background(), id)
	if c.Review.Revision != 2 || c.Review.Judgment.Note != "typed draft" {
		t.Fatal("stale tab overwrote saved work")
	}
	for _, mode := range []string{"disabled", "read-only"} {
		if mode == "disabled" {
			t.Setenv("CHEAPSEATS_EVALUATION", "")
		} else {
			t.Setenv("CHEAPSEATS_EVALUATION", "1")
			t.Setenv("CHEAPSEATS_READONLY", "1")
		}
		disabled, err := New(s.App)
		if err != nil {
			t.Fatal(err)
		}
		r = evaluationRequest(disabled.Handler(), "GET", "/evaluation", nil)
		if r.Code != 404 {
			t.Fatalf("%s evaluation exposed: %d", mode, r.Code)
		}
		r = evaluationRequest(disabled.Handler(), "GET", "/", nil)
		if strings.Contains(r.Body.String(), `href="/evaluation"`) {
			t.Fatalf("%s nav exposed", mode)
		}
	}
}

func TestEvaluationSessionEndResumeAndSavedDecisionRecovery(t *testing.T) {
	s, wb := evaluationServer(t)
	a, id := evaluationFixture(t, wb)
	h := s.Handler()
	// Attention sessions can include any draft, independently of the pilot split.
	wb.Save(context.Background(), id, 0, "draft", evaluation.Judgment{Note: "initial"}, "local reviewer", evaluation.Rubric())
	session, err := wb.Start(context.Background(), a.ID, "local reviewer", "attention")
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"revision": {"1"}, "rubric": {evaluation.Rubric()}, "session": {session.ID}, "cursor": {"0"}, "action": {"end"}, "note": {"unfinished before leaving"}}
	r := evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
	if r.Code != 303 {
		t.Fatalf("end: %d %s", r.Code, r.Body.String())
	}
	resumed, _ := wb.Resume(context.Background(), a.ID, "local reviewer")
	if resumed == nil || resumed.Cursor != 0 || resumed.State != "paused" {
		t.Fatalf("resume: %+v", resumed)
	}
	// Simulate an interrupted request after the decision committed but before
	// advancing. Continue acknowledges it without writing a second judgment.
	review, err := wb.Save(context.Background(), id, 2, "unresolved", evaluation.Judgment{Issue: "identity", Note: "Need to establish product identity."}, "local reviewer", evaluation.Rubric())
	if err != nil {
		t.Fatal(err)
	}
	values.Set("revision", strconv.Itoa(review.Revision))
	values.Set("action", "next")
	r = evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
	if r.Code != 303 {
		t.Fatalf("continue: %d %s", r.Code, r.Body.String())
	}
	history, _ := wb.History(context.Background(), id)
	if len(history) != 3 {
		t.Fatalf("continue added a duplicate decision: %d", len(history))
	}
	finished, _ := wb.Session(context.Background(), session.ID, "local reviewer")
	if finished.State != "done" || finished.Cursor != 1 {
		t.Fatalf("finish: %+v", finished)
	}
	r = evaluationRequest(h, "POST", "/evaluation/cases/"+id, values)
	if r.Code != 409 {
		t.Fatalf("double submission advanced: %d", r.Code)
	}
	foreign := httptest.NewRequest("POST", "http://localhost/evaluation/import", nil)
	foreign.Header.Set("Origin", "https://other.example")
	blocked := httptest.NewRecorder()
	h.ServeHTTP(blocked, foreign)
	if blocked.Code != 403 {
		t.Fatalf("cross-site write: %d", blocked.Code)
	}
}

func TestEvaluationApprovalExportsFrozenHumanVersion(t *testing.T) {
	s, wb := evaluationServer(t)
	a, id := evaluationFixture(t, wb)
	h := s.Handler()
	if _, err := wb.Save(context.Background(), id, 0, "judgment", evaluation.Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true, Note: "Human explanation for this test"}, "human reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	r := evaluationRequest(h, "POST", "/evaluation/datasets/prepare", url.Values{"import": {a.ID}})
	if r.Code != 303 {
		t.Fatalf("prepare: %d %s", r.Code, r.Body.String())
	}
	r = evaluationRequest(h, "GET", r.Header().Get("Location"), nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Approve dataset version") {
		t.Fatalf("preview: %d %s", r.Code, r.Body.String())
	}
	values := url.Values{"import": {a.ID}, "name": {"Approved test version"}, "member": {id}, "revision_" + id: {"1"}}
	r = evaluationRequest(h, "POST", "/evaluation/datasets", values)
	if r.Code != 303 {
		t.Fatalf("approve: %d %s", r.Code, r.Body.String())
	}
	path := r.Header().Get("Location")
	r = evaluationRequest(h, "GET", path, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "insufficient coverage") || !strings.Contains(r.Body.String(), "1 / 1") {
		t.Fatalf("version: %d %s", r.Code, r.Body.String())
	}
	r = evaluationRequest(h, "GET", path+"/export.jsonl", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"labeled_by":"human"`) || strings.Contains(r.Body.String(), "IMPORTED_MODEL_NOTE_DO_NOT_DISCLOSE") || r.Header().Get("Content-Disposition") == "" {
		t.Fatalf("export: %d %s", r.Code, r.Body.String())
	}
	if _, err := wb.Save(context.Background(), id, 1, "draft", evaluation.Judgment{Note: "A later unfinished edit"}, "reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	r = evaluationRequest(h, "GET", path+"/export.jsonl", nil)
	if strings.Contains(r.Body.String(), "later unfinished") || !strings.Contains(r.Body.String(), "Human explanation for this test") {
		t.Fatal("download followed a mutable current label")
	}
	for _, route := range []string{path, path + "/export.jsonl", path + "/manifest.json", "/evaluation/datasets/new?import=" + a.ID} {
		s.evaluation = false
		r = evaluationRequest(s.Handler(), "GET", route, nil)
		if r.Code != 404 {
			t.Fatalf("disabled route exposed %s: %d", route, r.Code)
		}
	}
}
