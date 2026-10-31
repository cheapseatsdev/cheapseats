package evaluation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

func evaluationBatchFixture(t *testing.T, n int) (*Workbench, Assessor, Import, CheckBatchPlan) {
	t.Helper()
	w := evaluationWorkbench(t)
	ctx := context.Background()
	var data []byte
	for i := 0; i < n; i++ {
		r := evaluationRecord()
		r.Answer = fmt.Sprintf("Choose Cheap Seats for pilot %d.", i)
		r.ID = grade.GoldenID(r.Answer)
		r.Prompt = fmt.Sprintf("Which tracker for team %d?", i)
		r.Note = "IMPORTED_NOTE_SENTINEL"
		data = append(data, evaluationJSON(r)...)
	}
	imported, err := w.ImportJSONL(ctx, "batch fixture", data)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	for _, row := range imported.Rows {
		c, _ := w.Case(ctx, row.CaseID)
		if _, err := w.Save(ctx, c.ID, 0, "judgment", Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{c.Record.Answer}, Note: "HUMAN_NOTE_SENTINEL"}, "human", Rubric()); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Grader: config.Model{Model: "test-grader"}, Pricing: map[string]config.Pricing{"test-grader": {Input: 1, Output: 1}}, Budget: config.Budget{DailyUSD: 20, ReservationUSD: .6, Concurrency: 2}}
	a := Assessor{Cfg: cfg, Budget: budget.New(cfg, &store.DB{DB: w.DB})}
	offer, err := w.PrepareCheckBatch(ctx, a, imported.ID, "", "human")
	if err != nil {
		t.Fatal(err)
	}
	return w, a, imported, offer.Plan
}
func TestCheckBatchRunsAllAndReusesCacheWithoutExposingLabels(t *testing.T) {
	w, a, imported, p := evaluationBatchFixture(t, 10)
	ctx := context.Background()
	b, err := w.CreateCheckBatch(ctx, a, p, "human")
	if err != nil {
		t.Fatal(err)
	}
	same, err := w.CreateCheckBatch(ctx, a, p, "human")
	if err != nil || same.Plan.ID != b.Plan.ID {
		t.Fatalf("duplicate confirmation changed batch: %+v %v", same, err)
	}
	if _, err := w.CheckBatch(ctx, b.Plan.ID, "someone else"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("batch disclosed to another reviewer")
	}
	var calls atomic.Int32
	assess := func(_ context.Context, r grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
		if r.Note != "" || r.Evidence != "" || r.LabeledBy != "" || r.Expected != (grade.GoldenExpected{}) || r.Source != nil {
			t.Error("labels reached model")
		}
		calls.Add(1)
		return evaluationPositive(r), 0, nil
	}
	if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, false, assess); err != nil {
		t.Fatal(err)
	}
	report, err := w.CheckBatchReport(ctx, b.Plan.ID, "human", a)
	if err != nil || report.Done != 10 || report.Agreements != 10 || report.Failed != 0 || report.Total != 10 || calls.Load() != 10 {
		t.Fatalf("batch results: %+v calls %d %v", report, calls.Load(), err)
	}
	if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, true, assess); err != nil || calls.Load() != 10 {
		t.Fatalf("resume repeated completed work: %d %v", calls.Load(), err)
	}
	// Another actor shares evidence cache, but has their own batch identity.
	offer, err := w.PrepareCheckBatch(ctx, a, imported.ID, "", "another human")
	if err != nil || offer.Cached != 10 || offer.New != 0 {
		t.Fatalf("cached offer: %+v %v", offer, err)
	}
	other, err := w.CreateCheckBatch(ctx, a, offer.Plan, "another human")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RunCheckBatch(ctx, other.Plan.ID, "another human", a, false, assess); err != nil || calls.Load() != 10 {
		t.Fatalf("cache not reused: %d %v", calls.Load(), err)
	}
}
func TestCheckBatchPreservesPartialResultsAndRetriesOnlyWhenAsked(t *testing.T) {
	w, a, _, p := evaluationBatchFixture(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	b, err := w.CreateCheckBatch(ctx, a, p, "human")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	first := func(_ context.Context, r grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
		calls++
		if calls == 2 {
			cancel()
			return grade.Grade{}, 0, context.Canceled
		}
		return evaluationPositive(r), 0, nil
	}
	if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, false, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	ctx = context.Background()
	report, err := w.CheckBatchReport(ctx, b.Plan.ID, "human", a)
	if err != nil || report.Done != 1 || report.Failed != 1 || report.Waiting != 1 {
		t.Fatalf("partial work lost: %+v %v", report, err)
	}
	positive := func(_ context.Context, r grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
		calls++
		return evaluationPositive(r), 0, nil
	}
	if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, false, positive); err != nil {
		t.Fatal(err)
	}
	report, _ = w.CheckBatchReport(ctx, b.Plan.ID, "human", a)
	if report.Done != 2 || report.Failed != 1 || calls != 3 {
		t.Fatalf("implicit failed retry: %+v %d", report, calls)
	}
	if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, true, positive); err != nil {
		t.Fatal(err)
	}
	report, _ = w.CheckBatchReport(ctx, b.Plan.ID, "human", a)
	if report.Done != 3 || report.Failed != 0 || calls != 4 {
		t.Fatalf("explicit retry failed: %+v %d", report, calls)
	}
}
func TestCheckBatchRejectsChangedJudgmentAndConfiguration(t *testing.T) {
	w, a, _, p := evaluationBatchFixture(t, 2)
	ctx := context.Background()
	id := p.Cases[0].CaseID
	if _, err := w.Save(ctx, id, 1, "draft", Judgment{}, "human", Rubric()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.CreateCheckBatch(ctx, a, p, "human"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale preview accepted: %v", err)
	}
	c, _ := w.Case(ctx, id)
	yes := true
	if _, err := w.Save(ctx, id, c.Review.Revision, "judgment", Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{c.Record.Answer}}, "human", Rubric()); err != nil {
		t.Fatal(err)
	}
	offer, _ := w.PrepareCheckBatch(ctx, a, p.ImportID, "", "human")
	b, err := w.CreateCheckBatch(ctx, a, offer.Plan, "human")
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg.Grader.Model = "other-model"
	if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
		t.Error("changed config called provider")
		return grade.Grade{}, 0, nil
	}); err == nil {
		t.Fatal("changed config resumed")
	}
}

func TestCheckBatchExpiredLeaseFencesLateWorker(t *testing.T) {
	w, a, _, p := evaluationBatchFixture(t, 1)
	ctx := context.Background()
	b, err := w.CreateCheckBatch(ctx, a, p, "human")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- w.RunCheckBatch(ctx, b.Plan.ID, "human", a, false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
			close(entered)
			<-release
			return grade.Grade{Verdict: grade.Absent}, 0, nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old worker did not start")
	}
	if _, err := w.DB.Exec(`UPDATE evaluation_check_batches SET lease_until='2000-01-01T00:00:00Z' WHERE id=?`, b.Plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.DB.Exec(`UPDATE evaluation_checks SET lease_until='2000-01-01T00:00:00Z' WHERE id=?`, p.Cases[0].CheckID); err != nil {
		t.Fatal(err)
	}
	if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, true, func(_ context.Context, r grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
		return evaluationPositive(r), 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("late worker was not fenced: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old worker did not stop")
	}
	report, err := w.CheckBatchReport(ctx, b.Plan.ID, "human", a)
	if err != nil || report.Done != 1 || report.Rows[0].Check.Grade.Verdict != grade.Recommended {
		t.Fatalf("late result replaced new assessment: %+v %v", report, err)
	}
}

func TestCheckBatchSharesActualProviderBudgetAndStopsBeforeUnstartedCases(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(writer).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"verdict":"absent","tone":"none","named":[]}`}}, "usage": map[string]any{"input_tokens": 550000, "output_tokens": 0}})
	}))
	defer api.Close()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	t.Setenv(config.AnthropicBaseURLVar, api.URL)
	w, a, _, p := evaluationBatchFixture(t, 3)
	p.MaxUSD = .8
	b, err := w.CreateCheckBatch(context.Background(), a, p, "human")
	if err != nil {
		t.Fatal(err)
	}
	err = w.RunCheckBatch(context.Background(), b.Plan.ID, "human", a, false, nil)
	if !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("group admission did not stop the batch: %v", err)
	}
	report, err := w.CheckBatchReport(context.Background(), b.Plan.ID, "human", a)
	if err != nil || calls.Load() != 1 || report.Done != 1 || report.Failed != 0 || report.Waiting != 2 || report.SpentUSD != .55 {
		t.Fatalf("budget or pending work lost: calls %d report %+v %v", calls.Load(), report, err)
	}
}
