package evaluation

import (
	"context"
	"errors"
	"fmt"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"strings"
	"sync/atomic"
	"testing"
)

func evaluationExperimentDataset(t *testing.T, w *Workbench, partition, question string) Dataset {
	t.Helper()
	ctx := context.Background()
	var r grade.GoldenRecord
	found := false
	// Select a synthetic fixture in the requested deterministic partition. This
	// does not change or mock the production grouping algorithm.
	for i := 0; i < 100; i++ {
		r = evaluationRecord()
		r.Prompt = fmt.Sprintf("Which tracker for test team %d?", i)
		if question != "" {
			r.Prompt = question
		}
		r.Answer = fmt.Sprintf("Use Cheap Seats for test team %d. It is the strongest fit for this pilot.", i)
		r.ID = grade.GoldenID(r.Answer)
		id := caseID(r)
		if grouping([]Case{{ID: id, Record: r}}).Cases[id].Partition == partition {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no fixture in requested partition")
	}
	r.Note = "FROZEN_REFERENCE_NOTE_SENTINEL"
	a, err := w.ImportJSONL(ctx, "experiment fixture "+partition+question, evaluationJSON(r))
	if err != nil {
		t.Fatal(err)
	}
	id := a.Rows[0].CaseID
	yes := true
	j := Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{r.Answer}, Note: "HUMAN_REFERENCE_SENTINEL"}
	if _, err := w.Save(ctx, id, 0, "judgment", j, "fixture human", Rubric()); err != nil {
		t.Fatal(err)
	}
	d, err := w.Approve(ctx, Approval{ImportID: a.ID, Name: "experiment fixture", Members: map[string]int{id: 1}}, "approver")
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func evaluationExperimentAssessor() Assessor {
	return Assessor{Cfg: &config.Config{Grader: config.Model{Model: "test-grader"}}}
}
func evaluationExperimentPlan(t *testing.T, w *Workbench, a Assessor, d Dataset) ExperimentPlan {
	t.Helper()
	p, err := w.PrepareExperiment(context.Background(), a, d.ID, d.Members[0].Group.Partition, []Variant{{Name: "SECRET_CANDIDATE_NAME", Prompt: grade.System + "\nA proposed experiment instruction."}}, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func evaluationPositive(r grade.GoldenRecord) grade.Grade {
	brand := r.Brand.Name
	return grade.Grade{Verdict: grade.Recommended, TopPick: &brand, Tone: grade.Positive, Named: []string{brand}, Evidence: r.Answer}
}

func TestExperimentRepeatsControlResumesOnlyMissingAndKeepsDenominator(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	d := evaluationExperimentDataset(t, w, "development", "")
	a := evaluationExperimentAssessor()
	p := evaluationExperimentPlan(t, w, a, d)
	if _, err := w.CreateExperiment(ctx, a, p, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.CreateExperiment(ctx, a, p, "operator"); err != nil {
		t.Fatal("idempotent create failed", err)
	}
	calls, control, candidate := 0, 0, 0
	assess := func(ctx context.Context, r grade.GoldenRecord, system, op string, limit float64) (grade.Grade, float64, error) {
		calls++
		if r.Expected != (grade.GoldenExpected{}) || r.Note != "" || r.Evidence != "" {
			t.Error("experiment sent reference labels to grader")
		}
		if system == grade.System {
			control++
			return grade.Grade{Verdict: grade.Absent}, .01, nil
		}
		candidate++
		if candidate == 1 {
			return grade.Grade{}, .01, errors.New("failed assessment")
		}
		return evaluationPositive(r), .01, nil
	}
	if err := w.RunExperiment(ctx, p.ID, a, false, assess); err != nil {
		t.Fatal(err)
	}
	report, err := w.ExperimentReport(ctx, p.ID)
	if err != nil || report.Total != 4 || report.Complete != 3 || report.Failed != 1 || report.Variants[1].Runs[0].Missing != 1 {
		t.Fatalf("partial report: %+v %v", report, err)
	}
	if err := w.RunExperiment(ctx, p.ID, a, false, assess); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || control != 2 {
		t.Fatalf("completed calls repeated: calls=%d control=%d", calls, control)
	}
	if err := w.RunExperiment(ctx, p.ID, a, true, assess); err != nil {
		t.Fatal(err)
	}
	report, err = w.ExperimentReport(ctx, p.ID)
	if err != nil || report.Complete != 4 || report.Failed != 0 || calls != 5 || control != 2 || report.Variants[1].Improved != 2 || report.Variants[1].Paired != 2 {
		t.Fatalf("resume changed measurements: %+v %v calls=%d", report, err, calls)
	}
	if len(ComparisonGate(report, 1)) == 0 {
		t.Fatal("small development example produced release acceptance")
	}
	a.Cfg.Grader.Model = "changed-model"
	if err := w.RunExperiment(ctx, p.ID, a, true, assess); err == nil {
		t.Fatal("resumed under another model")
	}
	if _, err := w.DB.Exec(`DELETE FROM evaluation_predictions WHERE experiment_id=? AND variant=1 AND repetition=0`, p.ID); err != nil {
		t.Fatal(err)
	}
	report, err = w.ExperimentReport(ctx, p.ID)
	if err != nil || report.Total != 4 || report.Complete != 3 || report.Variants[1].Runs[0].Missing != 1 {
		t.Fatal("missing stored row silently shrank the planned denominator", err)
	}
}

func TestHeldoutRejectsNearDuplicateAnswerUnderAnotherQuestion(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	a := evaluationExperimentAssessor()
	dev := evaluationExperimentDataset(t, w, "development", "")
	p := evaluationExperimentPlan(t, w, a, dev)
	if _, err := w.CreateExperiment(ctx, a, p, "operator"); err != nil {
		t.Fatal(err)
	}
	r := dev.Members[0].Record
	r.Answer += " Indeed."
	r.ID = grade.GoldenID(r.Answer)
	r.LabeledBy = "claude"
	r.Note = "Proposed synthetic fixture"
	found := false
	for i := 0; i < 100; i++ {
		r.Prompt = fmt.Sprintf("Which software fits a different buyer %d?", i)
		id := caseID(r)
		if grouping([]Case{{ID: id, Record: r}}).Cases[id].Partition == "held-out" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no heldout fixture")
	}
	imported, err := w.ImportJSONL(ctx, "near duplicate", evaluationJSON(r))
	if err != nil {
		t.Fatal(err)
	}
	id := imported.Rows[0].CaseID
	yes := true
	if _, err := w.Save(ctx, id, 0, "judgment", Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{r.Answer}}, "human", Rubric()); err != nil {
		t.Fatal(err)
	}
	d, err := w.Approve(ctx, Approval{ImportID: imported.ID, Name: "near duplicate", Members: map[string]int{id: 1}}, "approver")
	if err != nil {
		t.Fatal(err)
	}
	later := evaluationExperimentPlan(t, w, a, d)
	if _, err := w.CreateExperiment(ctx, a, later, "operator"); err == nil || !strings.Contains(err.Error(), "near-duplicate") {
		t.Fatalf("near duplicate became fresh validation: %v", err)
	}
}

func TestHeldoutCannotReusePreviouslyExposedQuestions(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	a := evaluationExperimentAssessor()
	dev := evaluationExperimentDataset(t, w, "development", "")
	p := evaluationExperimentPlan(t, w, a, dev)
	if _, err := w.CreateExperiment(ctx, a, p, "operator"); err != nil {
		t.Fatal(err)
	}
	held := evaluationExperimentDataset(t, w, "held-out", dev.Members[0].Record.Prompt)
	if _, err := w.PrepareExperiment(ctx, a, held.ID, "held-out", p.Variants[1:], 3, false); err == nil {
		t.Fatal("unfrozen heldout comparison allowed")
	}
	later := evaluationExperimentPlan(t, w, a, held)
	if _, err := w.CreateExperiment(ctx, a, later, "operator"); err == nil || !strings.Contains(err.Error(), "already exposed") {
		t.Fatalf("related question became fresh holdout: %v", err)
	}
	var count int
	w.DB.QueryRow(`SELECT count(*) FROM evaluation_experiments`).Scan(&count)
	if count != 1 {
		t.Fatal("failed heldout creation left a partial experiment")
	}
}

func TestPredictionLeaseFencesLateWorker(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	a := evaluationExperimentAssessor()
	d := evaluationExperimentDataset(t, w, "development", "")
	p := evaluationExperimentPlan(t, w, a, d)
	if _, err := w.CreateExperiment(ctx, a, p, "operator"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var count atomic.Int32
	go func() {
		done <- w.RunExperiment(ctx, p.ID, a, false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
			count.Add(1)
			close(entered)
			<-release
			return grade.Grade{Verdict: grade.Absent}, 0, nil
		})
	}()
	<-entered
	if _, err := w.DB.Exec(`UPDATE evaluation_predictions SET lease_until='2000-01-01T00:00:00Z' WHERE experiment_id=? AND state='running'`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.RunExperiment(ctx, p.ID, a, true, func(_ context.Context, r grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
		count.Add(1)
		return evaluationPositive(r), 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrConflict) {
		t.Fatalf("late worker wrote result: %v", err)
	}
	report, err := w.ExperimentReport(ctx, p.ID)
	if err != nil || report.Complete != 4 || report.Variants[0].Runs[0].VerdictCorrect != 1 || count.Load() != 5 {
		t.Fatalf("late worker changed prediction: %+v %v", report, err)
	}
}

func TestBlindedReviewHidesNamesAndReferenceUntilJudgment(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	a := evaluationExperimentAssessor()
	d := evaluationExperimentDataset(t, w, "development", "")
	p := evaluationExperimentPlan(t, w, a, d)
	if _, err := w.CreateExperiment(ctx, a, p, "operator"); err != nil {
		t.Fatal(err)
	}
	id := d.Members[0].CaseID
	b, err := w.BlindedReview(ctx, p.ID, id, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if b.Revealed || b.Member.Record.Expected != (grade.GoldenExpected{}) || b.Member.Record.Note != "" || b.Member.Judgment.Verdict != "" {
		t.Fatal("reference leaked before review")
	}
	for _, v := range b.Variants {
		if v.Name != "" {
			t.Fatal("candidate name leaked")
		}
	}
	if _, err := w.SaveExperimentReview(ctx, p.ID, id, "reviewer", 0, "unresolved", Judgment{Issue: "rubric", Note: "Need a clearer rule"}); err != nil {
		t.Fatal(err)
	}
	b, _ = w.BlindedReview(ctx, p.ID, id, "reviewer")
	if b.Revealed {
		t.Fatal("unresolved review revealed reference")
	}
	yes := true
	j := Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{d.Members[0].Record.Answer}, Note: "The answer explicitly endorses the intended product."}
	if _, err := w.SaveExperimentReview(ctx, p.ID, id, "reviewer", 1, "judgment", j); err != nil {
		t.Fatal(err)
	}
	b, _ = w.BlindedReview(ctx, p.ID, id, "reviewer")
	if !b.Revealed || b.Member.Record.Note != "HUMAN_REFERENCE_SENTINEL" {
		t.Fatal("committed review did not reveal reference")
	}
	other, _ := w.BlindedReview(ctx, p.ID, id, "another reviewer")
	if other.Revealed {
		t.Fatal("one reviewer unlocked another's reference")
	}
	frozen, _ := w.Dataset(ctx, d.ID)
	if frozen.Members[0].Revision != 1 || frozen.Members[0].Record.Expected != d.Members[0].Record.Expected {
		t.Fatal("comparison review edited frozen reference")
	}
	if _, err := w.SaveExperimentReview(ctx, p.ID, id, "reviewer", 1, "judgment", j); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale comparison review: %v", err)
	}
}
