package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func TestEvaluationBatchStartsOnceShowsProgressAndKeepsSelectedWithoutOverwritingConflicts(t *testing.T) {
	var calls atomic.Int32
	reached := make(chan struct{})
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "HUMAN_NOTE_SENTINEL") || strings.Contains(string(body), "MODEL_NOTE_SENTINEL") {
			t.Error("labels leaked to independent checks")
		}
		if calls.Add(1) == 1 {
			close(reached)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"verdict":"absent","named":[],"tone":"none","reasoning":"Visible model explanation"}`}}, "usage": map[string]any{"input_tokens": 100, "output_tokens": 20}})
	}))
	defer api.Close()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	t.Setenv(config.AnthropicBaseURLVar, api.URL)
	s, wb := evaluationServer(t)
	defer s.StopEvaluationJobs()
	ctx := context.Background()
	var data []byte
	for i := 0; i < 3; i++ {
		r := grade.GoldenRecord{Set: "batch-ui", Prompt: fmt.Sprintf("Which tracker for pilot %d?", i), Brand: grade.GoldenBrand{Name: "Example Tracker"}, Answer: fmt.Sprintf("Choose Example Tracker for pilot %d.", i), Expected: grade.GoldenExpected{Verdict: grade.Absent}, Note: "MODEL_NOTE_SENTINEL", LabeledBy: "claude"}
		r.ID = grade.GoldenID(r.Answer)
		b, _ := json.Marshal(r)
		data = append(append(data, b...), '\n')
	}
	imported, err := wb.ImportJSONL(ctx, "batch ui", data)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	for _, row := range imported.Rows {
		c, _ := wb.Case(ctx, row.CaseID)
		if _, err := wb.Save(ctx, c.ID, 0, "judgment", evaluation.Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{c.Record.Answer}, Note: "HUMAN_NOTE_SENTINEL"}, "local reviewer", evaluation.Rubric()); err != nil {
			t.Fatal(err)
		}
	}
	h := s.Handler()
	page := evaluationRequest(h, "GET", "/evaluation?import="+imported.ID, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Run checks for all 3") {
		t.Fatalf("no one-click batch offer: %d", page.Code)
	}
	var count int
	wb.DB.QueryRow(`SELECT count(*) FROM evaluation_checks`).Scan(&count)
	if count != 0 {
		t.Fatal("viewing offer made model calls")
	}
	offer, err := wb.PrepareCheckBatch(ctx, s.evaluationAssessor(), imported.ID, "", "local reviewer")
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"plan": {offer.PlanJSON}, "budget": {fmt.Sprint(offer.Plan.MaxUSD)}}
	started := evaluationRequest(h, "POST", "/evaluation/check-batches", values)
	if started.Code != 303 {
		t.Fatalf("batch start: %d %s", started.Code, started.Body.String())
	}
	path := started.Header().Get("Location")
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not start")
	}
	duplicate := evaluationRequest(h, "POST", "/evaluation/check-batches", values)
	if duplicate.Header().Get("Location") != path || calls.Load() != 1 {
		t.Fatal("duplicate confirmation started another batch")
	}
	progress := evaluationRequest(h, "GET", path+"/progress", nil)
	var live struct {
		Running bool `json:"running"`
	}
	json.Unmarshal(progress.Body.Bytes(), &live)
	if progress.Code != 200 || !live.Running {
		t.Fatalf("no running progress: %d %s", progress.Code, progress.Body.String())
	}
	close(release)
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		p := evaluationRequest(h, "GET", path+"/progress", nil)
		var status struct {
			Finished bool `json:"finished"`
			Done     int  `json:"done"`
		}
		json.Unmarshal(p.Body.Bytes(), &status)
		if status.Finished && status.Done == 3 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("batch incomplete: %s", p.Body.String())
		case <-ticker.C:
		}
	}
	results := evaluationRequest(h, "GET", path, nil)
	if results.Code != 200 || !strings.Contains(results.Body.String(), "All 3 comparisons") || strings.Count(results.Body.String(), "Visible model explanation") != 3 {
		t.Fatalf("not all results visible together: %d %s", results.Code, results.Body.String())
	}
	b, err := wb.CheckBatch(ctx, strings.TrimPrefix(path, "/evaluation/check-batches/"), "local reviewer")
	if err != nil {
		t.Fatal(err)
	}
	first, second := b.Plan.Cases[0], b.Plan.Cases[1]
	c, _ := wb.Case(ctx, second.CaseID)
	j := c.Review.Judgment
	j.Note = "Newer reviewer explanation"
	if _, err := wb.Save(ctx, second.CaseID, 1, "judgment", j, "local reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	keep := url.Values{"case": {first.CaseID, second.CaseID}, "note": {"Both saved answers explicitly endorse the intended software."}, "rubric": {evaluation.Rubric()}, "revision_" + first.CaseID: {"1"}, "revision_" + second.CaseID: {"1"}, "checks_" + first.CaseID: {first.CheckID}, "checks_" + second.CaseID: {second.CheckID}}
	partial := evaluationRequest(h, "POST", path+"/keep", keep)
	if partial.Code != 409 || !strings.Contains(partial.Body.String(), "Kept 1 saved judgments") || !strings.Contains(partial.Body.String(), "Some judgments changed") {
		t.Fatalf("partial keep obscured conflict: %d %s", partial.Code, partial.Body.String())
	}
	a, _ := wb.Case(ctx, first.CaseID)
	c, _ = wb.Case(ctx, second.CaseID)
	if a.Review.Judgment.Verdict != grade.Recommended || a.Review.Revision != 2 || c.Review.Revision != 2 || c.Review.Judgment.Note != "Newer reviewer explanation" {
		t.Fatal("bulk keep copied model labels or overwrote concurrent human work")
	}
	foreign := httptest.NewRequest("GET", "http://localhost"+path, nil)
	foreign.Header.Set("Authorization", "Bearer another-actor")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, foreign)
	if rec.Code != 404 {
		t.Fatalf("another reviewer opened batch: %d", rec.Code)
	}
	if calls.Load() != 3 {
		t.Fatalf("viewing or keeping repeated model calls: %d", calls.Load())
	}
}

func TestEvaluationBatchRoutesStayDisabledOutsideWritableWorkbench(t *testing.T) {
	s, wb := evaluationServer(t)
	a, id := evaluationFixture(t, wb)
	if _, err := wb.Save(context.Background(), id, 0, "judgment", evaluation.Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true}, "local reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	offer, err := wb.PrepareCheckBatch(context.Background(), s.evaluationAssessor(), a.ID, "", "local reviewer")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := wb.CreateCheckBatch(context.Background(), s.evaluationAssessor(), offer.Plan, "local reviewer")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"disabled", "read-only"} {
		t.Setenv("CHEAPSEATS_EVALUATION", "1")
		t.Setenv("CHEAPSEATS_READONLY", "")
		if mode == "disabled" {
			t.Setenv("CHEAPSEATS_EVALUATION", "")
		} else {
			t.Setenv("CHEAPSEATS_READONLY", "1")
		}
		guarded, err := New(s.App)
		if err != nil {
			t.Fatal(err)
		}
		h := guarded.Handler()
		for _, route := range []struct{ method, path string }{{"GET", "/evaluation/check-batches/" + batch.Plan.ID}, {"GET", "/evaluation/check-batches/" + batch.Plan.ID + "/progress"}, {"POST", "/evaluation/check-batches"}, {"POST", "/evaluation/check-batches/" + batch.Plan.ID + "/keep"}} {
			r := evaluationRequest(h, route.method, route.path, nil)
			want := 404
			if mode == "read-only" && route.method == "POST" {
				want = 403
			}
			if r.Code != want {
				t.Fatalf("%s exposed %s %s: %d", mode, route.method, route.path, r.Code)
			}
		}
	}
}
