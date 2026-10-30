package evaluation

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func evaluationReviewedCase(t *testing.T, w *Workbench) (string, Judgment) {
	t.Helper()
	a, err := w.ImportJSONL(context.Background(), "check fixture", evaluationJSON(evaluationRecord()))
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	j := Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{evaluationRecord().Answer}, Note: "HUMAN_NOTE_SENTINEL"}
	if _, err := w.Save(context.Background(), a.Rows[0].CaseID, 0, "judgment", j, "human", Rubric()); err != nil {
		t.Fatal(err)
	}
	return a.Rows[0].CaseID, j
}
func evaluationCheckPlan(id string) CheckPlan {
	return (Assessor{Cfg: &config.Config{Grader: config.Model{Model: "test-grader"}}}).CheckPlan(id, 3)
}

func TestIndependentCheckNeverReceivesLabelsOrOverwritesHumanDecision(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	id, j := evaluationReviewedCase(t, w)
	p := evaluationCheckPlan(id)
	var calls atomic.Int32
	assessor := func(ctx context.Context, r grade.GoldenRecord, system, operation string, limit float64) (grade.Grade, float64, error) {
		calls.Add(1)
		if r.Expected != (grade.GoldenExpected{}) || r.Note != "" || r.Evidence != "" || r.LabeledBy != "" || r.Source != nil {
			t.Error("assessment received labels or proposal evidence")
		}
		if r.Answer != evaluationRecord().Answer || system != grade.System {
			t.Error("missing immutable evidence/rules")
		}
		return grade.Grade{Verdict: grade.Absent}, .01, nil
	}
	checked, err := w.RunCheck(ctx, p, "operator", false, assessor)
	if err != nil || checked.State != "done" {
		t.Fatalf("check: %+v %v", checked, err)
	}
	_, err = w.RunCheck(ctx, p, "operator", false, assessor)
	if err != nil || calls.Load() != 1 {
		t.Fatal("cached check caused another model call")
	}
	c, _ := w.Case(ctx, id)
	if c.Review.Revision != 1 || c.Review.Judgment.Expected() != j.Expected() {
		t.Fatal("model overwrote human")
	}
	pending, err := w.PendingChecks(ctx, c)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	imports, _ := w.Imports(ctx)
	approval := Approval{ImportID: imports[0].ID, Name: "checked", Members: map[string]int{id: 1}}
	if _, err := w.Approve(ctx, approval, "approver"); err == nil {
		t.Fatal("pending dispute approved")
	}
	if _, err := w.Resolve(ctx, id, 1, Judgment{}, "human", Rubric(), Resolution{Checks: pending, Action: "keep"}); err == nil {
		t.Fatal("unexplained resolution accepted")
	}
	resolved, err := w.Resolve(ctx, id, 1, Judgment{}, "human", Rubric(), Resolution{Checks: pending, Action: "keep", Explanation: "The answer explicitly endorses the brand."})
	if err != nil || resolved.Judgment.Expected() != j.Expected() {
		t.Fatalf("keep: %+v %v", resolved, err)
	}
	approval.Members[id] = 2
	d, err := w.Approve(ctx, approval, "approver")
	if err != nil || len(d.Members[0].Resolutions) != 1 || d.Members[0].Provenance != "single human review" {
		t.Fatalf("approval: %+v %v", d, err)
	}
	if _, err := w.Resolve(ctx, id, 1, j, "human", Rubric(), Resolution{Checks: pending, Action: "change", Explanation: "stale"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale resolution: %v", err)
	}
	history, _ := w.History(ctx, id)
	if len(history) != 2 {
		t.Fatal("model check pretended to be human history")
	}
}

func TestCheckClaimsFailuresAndExpiredWorkerAreBounded(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	id, _ := evaluationReviewedCase(t, w)
	p := evaluationCheckPlan(id)
	started, finish := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := w.RunCheck(ctx, p, "operator", false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
			close(started)
			<-finish
			return grade.Grade{Verdict: grade.Absent}, 0, nil
		})
		done <- err
	}()
	<-started
	forbidden := func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
		t.Error("duplicate call admitted")
		return grade.Grade{}, 0, nil
	}
	if _, err := w.RunCheck(ctx, p, "other tab", true, forbidden); err == nil {
		t.Fatal("two workers claimed one check")
	}
	// Simulate a crashed worker by expiring its persisted lease; no test sleep.
	if _, err := w.DB.Exec(`UPDATE evaluation_checks SET lease_until='2000-01-01T00:00:00Z' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := w.RunCheck(ctx, p, "operator", true, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
		return grade.Grade{}, 0, errors.New("provider unavailable")
	})
	if err == nil || failed.State != "failed" || failed.Grade != nil {
		t.Fatalf("failed: %+v %v", failed, err)
	}
	close(finish)
	if err := <-done; !errors.Is(err, ErrConflict) {
		t.Fatalf("expired worker replaced retry: %v", err)
	}
	if _, err := w.RunCheck(ctx, p, "operator", true, forbidden); err == nil {
		t.Fatal("unbounded third attempt")
	}
	c, _ := w.Case(ctx, id)
	ids, _ := w.PendingChecks(ctx, c)
	if len(ids) != 0 {
		t.Fatal("provider failure fabricated an absent assessment")
	}
	saved, _ := w.Check(ctx, p.ID)
	if saved.State != "failed" || !strings.Contains(saved.Error, "provider unavailable") {
		t.Fatal("late worker overwrote failed retry")
	}
}

func TestNewCheckCannotBeSilentlyResolvedFromAnOlderPage(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	id, _ := evaluationReviewedCase(t, w)
	p := evaluationCheckPlan(id)
	assess := func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
		return grade.Grade{Verdict: grade.Absent}, 0, nil
	}
	if _, err := w.RunCheck(ctx, p, "operator", false, assess); err != nil {
		t.Fatal(err)
	}
	c, _ := w.Case(ctx, id)
	old, _ := w.PendingChecks(ctx, c)
	cfg := &config.Config{Grader: config.Model{Model: "second-model"}}
	second := (Assessor{Cfg: cfg}).CheckPlan(id, 3)
	if _, err := w.RunCheck(ctx, second, "operator", false, assess); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Resolve(ctx, id, 1, c.Review.Judgment, "human", Rubric(), Resolution{Checks: old, Action: "keep", Explanation: "I did not see the later check"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unseen check resolved: %v", err)
	}
}
