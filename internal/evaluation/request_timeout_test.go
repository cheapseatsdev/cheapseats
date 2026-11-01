package evaluation

import (
	"context"
	"errors"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func TestRequestTimeoutUsesBothCheckAttempts(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	id, _ := evaluationReviewedCase(t, w)
	p := evaluationCheckPlan(id)
	calls := 0
	timeout := func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
		calls++
		return grade.Grade{}, .01, context.DeadlineExceeded
	}
	for attempt := 1; attempt <= 2; attempt++ {
		check, err := w.RunCheck(ctx, p, "human", attempt > 1, timeout)
		if !errors.Is(err, context.DeadlineExceeded) || check.State != CheckFailed || check.Attempt != attempt {
			t.Fatalf("request timeout refunded attempt %d: %+v %v", attempt, check, err)
		}
	}
	if _, err := w.RunCheck(ctx, p, "human", true, timeout); err == nil || calls != 2 {
		t.Fatalf("timed-out check admitted a third call: calls=%d %v", calls, err)
	}
}

func TestRequestTimeoutDoesNotStopLaterExperimentPredictions(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	d := evaluationExperimentDataset(t, w, "development", "")
	a := evaluationExperimentAssessor()
	p := evaluationExperimentPlan(t, w, a, d)
	if _, err := w.CreateExperiment(ctx, a, p, "human"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	assess := func(_ context.Context, r grade.GoldenRecord, prompt, _ string, _ float64) (grade.Grade, float64, error) {
		calls++
		if prompt == grade.System {
			return grade.Grade{}, .01, context.DeadlineExceeded
		}
		return evaluationPositive(r), .01, nil
	}
	for sweep := 0; sweep < 3; sweep++ {
		if err := w.RunExperiment(ctx, p.ID, a, sweep > 0, assess); err != nil {
			t.Fatal("one request stopped the enclosing comparison", err)
		}
		r, err := w.ExperimentReport(ctx, p.ID)
		if err != nil || r.Complete != 2 || r.Failed != 2 || r.Total != 4 {
			t.Fatalf("later predictions were starved: %+v %v", r, err)
		}
	}
	if calls != 6 {
		t.Fatalf("timeout attempts were unbounded or completed predictions repeated: %d", calls)
	}
	rows, _ := w.Predictions(ctx, p.ID)
	for _, row := range rows {
		if row.Variant == 0 && (row.State != CheckFailed || row.Attempt != 2) {
			t.Fatalf("timed-out prediction did not exhaust its allowance: %+v", row)
		}
	}
}

func TestRequestTimeoutDoesNotStopLaterBatchCases(t *testing.T) {
	w, a, _, p := evaluationBatchFixture(t, 2)
	ctx := context.Background()
	b, err := w.CreateCheckBatch(ctx, a, p, "human")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	assess := func(_ context.Context, r grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
		calls++
		if caseID(r) == p.Cases[0].CaseID {
			return grade.Grade{}, .01, context.DeadlineExceeded
		}
		return evaluationPositive(r), .01, nil
	}
	for sweep := 0; sweep < 3; sweep++ {
		if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, sweep > 0, assess); err != nil {
			t.Fatal("one request stopped the enclosing batch", err)
		}
		r, err := w.CheckBatchReport(ctx, b.Plan.ID, "human", a)
		if err != nil || r.Done != 1 || r.Failed != 1 || r.Waiting != 0 || r.Running != 0 {
			t.Fatalf("later cases were starved: %+v %v", r, err)
		}
	}
	check, err := w.Check(ctx, p.Cases[0].CheckID)
	if err != nil || check.State != CheckFailed || check.Attempt != 2 || calls != 3 {
		t.Fatalf("timeout attempts were unbounded: %+v calls=%d %v", check, calls, err)
	}
}
