package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func TestCheckPausesPreserveRemainingAttemptAndResumeWithoutRetry(t *testing.T) {
	for _, pause := range []error{context.Canceled, context.DeadlineExceeded, budget.ErrExhausted} {
		t.Run(pause.Error(), func(t *testing.T) {
			w := evaluationWorkbench(t)
			ctx := context.Background()
			id, _ := evaluationReviewedCase(t, w)
			p := evaluationCheckPlan(id)
			// A genuine failure still uses an attempt and requires explicit retry.
			failed, err := w.RunCheck(ctx, p, "human", false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
				return grade.Grade{}, 0, errors.New("invalid provider response")
			})
			if err == nil || failed.State != CheckFailed || failed.Attempt != 1 {
				t.Fatalf("failure: %+v %v", failed, err)
			}
			for i := 0; i < 3; i++ {
				runCtx, stop := context.WithCancel(ctx)
				paused, err := w.RunCheck(runCtx, p, "human", i == 0, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
					if !errors.Is(pause, budget.ErrExhausted) {
						stop()
					}
					return grade.Grade{}, .02, fmt.Errorf("transport stopped: %w", pause)
				})
				stop()
				if !errors.Is(err, pause) || paused.State != CheckReady || paused.Attempt != 1 || paused.Grade != nil {
					t.Fatalf("pause %d: %+v %v", i, paused, err)
				}
			}
			done, err := w.RunCheck(ctx, p, "human", false, func(_ context.Context, r grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
				return evaluationPositive(r), .03, nil
			})
			if err != nil || done.State != CheckDone || done.Attempt != 2 {
				t.Fatalf("resume: %+v %v", done, err)
			}
		})
	}
}

func TestExperimentPausesResumeWithoutRetryAndKeepRecordedCost(t *testing.T) {
	for _, pause := range []error{context.Canceled, context.DeadlineExceeded, budget.ErrExhausted} {
		t.Run(pause.Error(), func(t *testing.T) {
			w := evaluationWorkbench(t)
			ctx := context.Background()
			d := evaluationExperimentDataset(t, w, "development", "")
			a := evaluationExperimentAssessor()
			p := evaluationExperimentPlan(t, w, a, d)
			if _, err := w.CreateExperiment(ctx, a, p, "human"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				runCtx, stop := context.WithCancel(ctx)
				err := w.RunExperiment(runCtx, p.ID, a, false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
					if !errors.Is(pause, budget.ErrExhausted) {
						stop()
					}
					return grade.Grade{}, .02, pause
				})
				stop()
				if !errors.Is(err, pause) {
					t.Fatalf("pause: %v", err)
				}
				rows, err := w.Predictions(ctx, p.ID)
				if err != nil || rows[0].State != CheckReady || rows[0].Attempt != 0 || rows[0].Grade != nil || rows[0].Cost < .02*float64(i+1)-1e-9 {
					t.Fatalf("pause consumed attempts or lost spending: %+v %v", rows, err)
				}
			}
			if err := w.RunExperiment(ctx, p.ID, a, false, func(_ context.Context, r grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
				return evaluationPositive(r), .01, nil
			}); err != nil {
				t.Fatal(err)
			}
			report, err := w.ExperimentReport(ctx, p.ID)
			if err != nil || report.Complete != report.Total || report.Failed != 0 {
				t.Fatalf("resume: %+v %v", report, err)
			}
		})
	}
}

func TestCheckDifferencesRequireResolutionEvenForOlderEvaluator(t *testing.T) {
	for _, older := range []string{"current", "rubric", "evaluator"} {
		t.Run(older, func(t *testing.T) {
			w := evaluationWorkbench(t)
			ctx := context.Background()
			id, _ := evaluationReviewedCase(t, w)
			p := evaluationCheckPlan(id)
			if _, err := w.RunCheck(ctx, p, "human", false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
				return grade.Grade{Verdict: grade.Absent}, 0, nil
			}); err != nil {
				t.Fatal(err)
			}
			if older == "rubric" {
				p.Rubric = "previous-rubric"
			} else if older == "evaluator" {
				p.Evaluator = "previous-build"
			}
			raw, _ := json.Marshal(p)
			if _, err := w.DB.Exec(`UPDATE evaluation_checks SET plan=? WHERE id=?`, string(raw), p.ID); err != nil {
				t.Fatal(err)
			}
			j := Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true}
			for _, kind := range []string{"judgment", "draft", "unresolved"} {
				if _, err := w.Save(ctx, id, 1, kind, j, "human", Rubric()); err == nil {
					t.Fatalf("%s bypassed the resolution record", kind)
				}
			}
			imports, _ := w.Imports(ctx)
			approval := Approval{ImportID: imports[0].ID, Name: "reference", Members: map[string]int{id: 1}}
			if _, err := w.Approve(ctx, approval, "approver"); err == nil {
				t.Fatal("unresolved older check stopped gating approval")
			}
			review, err := w.Resolve(ctx, id, 1, j, "human", Rubric(), Resolution{Checks: []string{p.ID}, Action: "change", Explanation: "The wording only echoes the product name; it does not describe it."})
			if err != nil {
				t.Fatal(err)
			}
			approval.Members[id] = review.Revision
			d, err := w.Approve(ctx, approval, "approver")
			if err != nil || len(d.Members[0].Resolutions) != 1 || d.Members[0].Resolutions[0].Action != "change" {
				t.Fatalf("resolution not retained: %+v %v", d, err)
			}
		})
	}
}

func TestExpiredFinalCheckDoesNotBlockApprovalOrAcceptLateResult(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	id, _ := evaluationReviewedCase(t, w)
	p := evaluationCheckPlan(id)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	finished := make(chan error, 1)
	go func() {
		_, err := w.RunCheck(ctx, p, "human", false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
			close(started)
			<-release
			return grade.Grade{Verdict: grade.Absent}, 0, nil
		})
		finished <- err
	}()
	<-started
	if _, err := w.DB.Exec(`UPDATE evaluation_checks SET attempt=2,lease_until='2000-01-01T00:00:00Z' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	c, _ := w.Check(ctx, p.ID)
	if c.State != CheckFailed || c.Attempt != 2 || !strings.Contains(c.Error, "unknown") {
		t.Fatalf("expired final check: %+v", c)
	}
	imports, _ := w.Imports(ctx)
	if _, err := w.Approve(ctx, Approval{ImportID: imports[0].ID, Name: "unknown assessment", Members: map[string]int{id: 1}}, "approver"); err != nil {
		t.Fatal("expired final worker permanently blocked approval", err)
	}
	release <- struct{}{}
	if err := <-finished; !errors.Is(err, ErrConflict) {
		t.Fatalf("expired worker saved after approval: %v", err)
	}
}

func TestSimultaneousSessionStartsLeaveOnlyOneSession(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	a, err := w.ImportJSONL(ctx, "double start", evaluationJSON(evaluationRecord()))
	if err != nil {
		t.Fatal(err)
	}
	id := a.Rows[0].CaseID
	if _, err := w.Save(ctx, id, 0, "draft", Judgment{}, "human", Rubric()); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan Session, 12)
	errorsCh := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := w.Start(ctx, a.ID, "human", "attention")
			results <- s
			errorsCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var sessionID string
	for s := range results {
		if sessionID != "" && sessionID != s.ID {
			t.Fatal("simultaneous starts returned different sessions")
		}
		sessionID = s.ID
	}
	if err := w.Advance(ctx, sessionID, "human", id, 0, false); err != nil {
		t.Fatal(err)
	}
	if leftover, err := w.Resume(ctx, a.ID, "human"); err != nil || leftover != nil {
		t.Fatalf("a duplicate session returned after finishing: %+v %v", leftover, err)
	}
}

func TestDuplicateApprovalDoesNotClaimAnUnrecordedApprover(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	id, _ := evaluationReviewedCase(t, w)
	imports, _ := w.Imports(ctx)
	p := Approval{ImportID: imports[0].ID, Name: "reference", Members: map[string]int{id: 1}}
	d, err := w.Approve(ctx, p, "first approver")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Approve(ctx, p, "second approver"); err == nil || !strings.Contains(err.Error(), "already approved by first approver") {
		t.Fatalf("unrecorded second approval appeared successful: %v", err)
	}
	again, err := w.Approve(ctx, p, "first approver")
	if err != nil || again.ID != d.ID || again.CreatedAt != d.CreatedAt || again.Actor != d.Actor {
		t.Fatalf("original approval was changed: %+v %v", again, err)
	}
}

func TestBatchDifferencesAndKeepStayWithinDisplayedAssessments(t *testing.T) {
	w, a, imported, p := evaluationBatchFixture(t, 2)
	ctx := context.Background()
	b, err := w.CreateCheckBatch(ctx, a, p, "human")
	if err != nil {
		t.Fatal(err)
	}
	otherCfg := *a.Cfg
	otherCfg.Grader.Model = "other-grader"
	other := Assessor{Cfg: &otherCfg}
	absent := func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
		return grade.Grade{Verdict: grade.Absent}, 0, nil
	}
	for _, item := range p.Cases {
		if _, err := w.RunCheck(ctx, other.CheckPlan(item.CaseID, 3), "human", false, absent); err != nil {
			t.Fatal(err)
		}
	}
	differingID := p.Cases[1].CaseID
	if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, false, func(_ context.Context, r grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
		if caseID(r) == differingID {
			return grade.Grade{Verdict: grade.Absent}, 0, nil
		}
		return evaluationPositive(r), 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	report, err := w.CheckBatchReport(ctx, b.Plan.ID, "human", a)
	if err != nil || report.Agreements != 1 || report.Differences != 1 || len(report.Rows[0].Pending) != 0 || len(report.Rows[1].Pending) != 1 {
		t.Fatalf("unrelated checks changed batch counts: %+v %v", report, err)
	}
	checks := []string{p.Cases[1].CheckID, other.CheckPlan(differingID, 3).ID}
	if _, err := w.KeepBatchJudgment(ctx, b.Plan.ID, differingID, 1, "human", Rubric(), checks, "The answer endorses the brand."); !errors.Is(err, ErrConflict) {
		t.Fatalf("batch keep accepted an unrelated check: %v", err)
	}
	review, err := w.KeepBatchJudgment(ctx, b.Plan.ID, differingID, 1, "human", Rubric(), checks[:1], "The answer endorses the brand.")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := w.Case(ctx, differingID)
	pending, err := w.PendingChecks(ctx, c)
	if err != nil || len(pending) != 1 || pending[0] != checks[1] {
		t.Fatalf("batch keep resolved another grader: %v %v", pending, err)
	}
	history, _ := w.ResolutionHistory(ctx, differingID)
	if len(history) != 1 || history[0].CheckID != checks[0] || history[0].Revision != review.Revision {
		t.Fatalf("resolution history includes unseen checks: %+v", history)
	}
	if _, err := w.Approve(ctx, Approval{ImportID: imported.ID, Name: "still pending", Members: map[string]int{p.Cases[0].CaseID: 1, differingID: review.Revision}}, "approver"); err == nil {
		t.Fatal("unrelated differences no longer gate approval")
	}
	// Deliberately resolve the other assessment. Keeping the same judgment must
	// preserve the first batch's already recorded decision at the new revision.
	if _, err := w.Resolve(ctx, differingID, review.Revision, Judgment{}, "human", Rubric(), Resolution{Checks: pending, Action: "keep", Explanation: "I also inspected the other grader; the saved answer endorses the brand."}); err != nil {
		t.Fatal(err)
	}
	c, _ = w.Case(ctx, differingID)
	if pending, err := w.PendingChecks(ctx, c); err != nil || len(pending) != 0 {
		t.Fatalf("keeping another check reopened the first batch: %v %v", pending, err)
	}
}

func TestBatchPauseAndRealFailureHaveDifferentResumeRules(t *testing.T) {
	for _, interruption := range []error{context.DeadlineExceeded, budget.ErrExhausted, errors.New("provider response invalid")} {
		t.Run(interruption.Error(), func(t *testing.T) {
			w, a, _, p := evaluationBatchFixture(t, 1)
			ctx := context.Background()
			b, err := w.CreateCheckBatch(ctx, a, p, "human")
			if err != nil {
				t.Fatal(err)
			}
			err = w.RunCheckBatch(ctx, b.Plan.ID, "human", a, false, func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error) {
				return grade.Grade{}, 0, interruption
			})
			paused := errors.Is(interruption, budget.ErrExhausted)
			if paused && !errors.Is(err, interruption) {
				t.Fatal(err)
			}
			check, err := w.Check(ctx, p.Cases[0].CheckID)
			if err != nil || paused && (check.State != CheckReady || check.Attempt != 0) || !paused && (check.State != CheckFailed || check.Attempt != 1) {
				t.Fatalf("resume classification: %+v %v", check, err)
			}
			calls := 0
			positive := func(_ context.Context, r grade.GoldenRecord, _, _ string, _ float64) (grade.Grade, float64, error) {
				calls++
				return evaluationPositive(r), 0, nil
			}
			if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, false, positive); err != nil {
				t.Fatal(err)
			}
			if paused && calls != 1 || !paused && calls != 0 {
				t.Fatalf("resume calls=%d paused=%v", calls, paused)
			}
			if err := w.RunCheckBatch(ctx, b.Plan.ID, "human", a, true, positive); err != nil || calls != 1 {
				t.Fatalf("explicit retry: calls=%d %v", calls, err)
			}
		})
	}
}
