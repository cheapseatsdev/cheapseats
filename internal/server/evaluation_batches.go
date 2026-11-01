package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func (s *Server) evaluationBatchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /evaluation/check-batches", s.evaluationBatchCreate)
	mux.HandleFunc("GET /evaluation/check-batches/{id}", s.evaluationBatchPage)
	mux.HandleFunc("GET /evaluation/check-batches/{id}/progress", s.evaluationBatchProgress)
	mux.HandleFunc("POST /evaluation/check-batches/{id}/run", s.evaluationBatchRun)
	mux.HandleFunc("POST /evaluation/check-batches/{id}/stop", s.evaluationBatchStop)
	mux.HandleFunc("POST /evaluation/check-batches/{id}/keep", s.evaluationBatchKeep)
}
func (s *Server) evaluationBatchCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var plan evaluation.CheckBatchPlan
	if err := json.Unmarshal([]byte(r.FormValue("plan")), &plan); err != nil {
		http.Error(w, "reload the batch offer before starting", 400)
		return
	}
	amount, err := strconv.ParseFloat(r.FormValue("budget"), 64)
	if err != nil {
		http.Error(w, "choose a batch admission budget", 400)
		return
	}
	plan.MaxUSD = amount
	batch, err := s.evaluationStore().CreateCheckBatch(r.Context(), s.evaluationAssessor(), plan, s.evaluationActor(r))
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	if r.FormValue("run") != "0" {
		if err = s.startEvaluationBatch(r, batch, r.FormValue("retry") == "on"); err != nil {
			slog.Info("independent check batch needs attention", "batch", batch.Plan.ID, "error", err)
		}
	}
	http.Redirect(w, r, "/evaluation/check-batches/"+batch.Plan.ID, http.StatusSeeOther)
}
func (s *Server) startEvaluationBatch(r *http.Request, b evaluation.CheckBatch, retry bool) error {
	a := s.evaluationAssessor()
	if b.Plan.Model != a.Cfg.Grader.Ref() || b.Plan.Transport != a.Transport() || b.Plan.Evaluator != grade.EvaluatorDigest() || b.Plan.Rubric != evaluation.Rubric() {
		return errors.New("resume requires the original model, transport, evaluator and rules")
	}
	// Cached results remain available without provider access. Missing access never
	// starts a sweep that would manufacture failed predictions for every case.
	for _, c := range b.Plan.Cases {
		check, err := s.evaluationStore().Check(r.Context(), c.CheckID)
		if err != nil || check.State != evaluation.CheckDone {
			if err := a.Ready(); err != nil {
				return err
			}
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	job, claimed, err := s.claimEvaluationJob(b.Plan.ID, cancel)
	if err != nil || !claimed {
		return err
	}
	go func() {
		defer cancel()
		defer s.finishEvaluationJob(b.Plan.ID, job)
		if err := s.evaluationStore().RunCheckBatch(ctx, b.Plan.ID, b.Actor, a, retry, nil); err != nil {
			slog.Info("independent check batch paused", "batch", b.Plan.ID, "error", err)
		}
	}()
	return nil
}

type evaluationBatchRow struct {
	evaluation.CheckBatchRow
	CanKeep       bool
	Issue         string
	ModelExpected grade.GoldenExpected
}
type evaluationBatchPage struct {
	Report                  evaluation.CheckBatchReport
	Rows                    []evaluationBatchRow
	Running                 bool
	Percent                 float64
	ReadyError, Error, Note string
	Rubric                  string
	Saved                   int
	Confirmation            evaluationConfirmation
}

func (s *Server) evaluationBatchData(r *http.Request) (evaluationBatchPage, error) {
	report, err := s.evaluationStore().CheckBatchReport(r.Context(), r.PathValue("id"), s.evaluationActor(r), s.evaluationAssessor())
	out := evaluationBatchPage{Report: report, Rows: []evaluationBatchRow{}, Confirmation: s.evaluationConfirmation(r), Rubric: evaluation.Rubric()}
	if err != nil {
		return out, err
	}
	_, local := s.evaluationJobs.Load(report.Batch.Plan.ID)
	out.Running = local || report.Active
	if report.Total > 0 {
		out.Percent = 100 * float64(report.Done+report.Failed) / float64(report.Total)
	}
	for _, row := range report.Rows {
		v := evaluationBatchRow{CheckBatchRow: row, CanKeep: row.State == evaluation.CheckDone && row.Check != nil && row.Case.Review.State == "reviewed" && len(row.Pending) > 0}
		if row.Check != nil && row.Check.Grade != nil {
			v.ModelExpected = grade.GoldenExpectedOf(*row.Check.Grade, row.Case.Record.Brand.Name)
		}
		out.Rows = append(out.Rows, v)
	}
	if err := s.evaluationAssessor().Ready(); err != nil {
		out.ReadyError = err.Error()
	}
	out.Saved, _ = strconv.Atoi(r.URL.Query().Get("saved"))
	return out, nil
}
func (s *Server) evaluationBatchPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationBatchData(r)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation-batch", d)
}
func (s *Server) evaluationBatchProgress(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationBatchData(r)
	if err != nil {
		httpErr(w, err, evaluationCode(err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"total": d.Report.Total, "done": d.Report.Done, "failed": d.Report.Failed, "running": d.Running, "percent": d.Percent, "finished": !d.Running, "error": d.Report.Batch.Error})
}
func (s *Server) evaluationBatchRun(w http.ResponseWriter, r *http.Request) {
	b, err := s.evaluationStore().CheckBatch(r.Context(), r.PathValue("id"), s.evaluationActor(r))
	if err == nil {
		err = s.startEvaluationBatch(r, b, r.FormValue("retry") == "on")
	}
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	http.Redirect(w, r, "/evaluation/check-batches/"+b.Plan.ID, http.StatusSeeOther)
}
func (s *Server) evaluationBatchStop(w http.ResponseWriter, r *http.Request) {
	b, err := s.evaluationStore().CheckBatch(r.Context(), r.PathValue("id"), s.evaluationActor(r))
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	if job, ok := s.evaluationJobs.Load(b.Plan.ID); ok {
		job.(*evaluationJob).Cancel()
	}
	http.Redirect(w, r, "/evaluation/check-batches/"+b.Plan.ID, http.StatusSeeOther)
}
func (s *Server) evaluationBatchKeep(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid selection", 400)
		return
	}
	batch, err := s.evaluationStore().CheckBatch(r.Context(), r.PathValue("id"), s.evaluationActor(r))
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	selected := r.Form["case"]
	note := strings.TrimSpace(r.FormValue("note"))
	allowed := map[string]bool{}
	for _, c := range batch.Plan.Cases {
		allowed[c.CaseID] = true
	}
	seen := map[string]bool{}
	if len(selected) == 0 || len(selected) > len(batch.Plan.Cases) || note == "" || len([]rune(note)) > 4000 {
		err = errors.New("select the differences to keep and explain why your saved judgments fit those answers")
	}
	for _, id := range selected {
		if !allowed[id] || seen[id] {
			err = errors.New("selection does not belong to this batch")
		}
		seen[id] = true
	}
	issues := map[string]string{}
	saved := 0
	if err == nil {
		for _, id := range selected {
			revision, e := strconv.Atoi(r.FormValue("revision_" + id))
			if e == nil {
				_, e = s.evaluationStore().KeepBatchJudgment(r.Context(), batch.Plan.ID, id, revision, s.evaluationActor(r), r.FormValue("rubric"), r.Form["checks_"+id], note)
			}
			if e != nil {
				issues[id] = e.Error()
			} else {
				saved++
			}
		}
	}
	if err == nil && len(issues) == 0 {
		http.Redirect(w, r, fmt.Sprintf("/evaluation/check-batches/%s?saved=%d", batch.Plan.ID, saved), http.StatusSeeOther)
		return
	}
	d, e := s.evaluationBatchData(r)
	if e != nil {
		http.Error(w, e.Error(), evaluationCode(e))
		return
	}
	d.Saved, d.Note = saved, note
	if err != nil {
		d.Error = err.Error()
	} else {
		d.Error = "Some judgments changed while this page was open. Successful keeps are saved; review the current labels before selecting the remaining cases again."
	}
	for i := range d.Rows {
		d.Rows[i].Issue = issues[d.Rows[i].Case.ID]
	}
	w.WriteHeader(http.StatusConflict)
	s.render(w, "evaluation-batch", d)
}
func (s *Server) evaluationBatchCase(r *http.Request, id, caseID string) (evaluation.CheckBatch, error) {
	b, err := s.evaluationStore().CheckBatch(r.Context(), id, s.evaluationActor(r))
	if err != nil {
		return b, err
	}
	for _, c := range b.Plan.Cases {
		if c.CaseID == caseID {
			return b, nil
		}
	}
	return b, errors.New("this answer is outside the selected batch")
}
func evaluationBatchReturn(id, caseID string, revision int) string {
	return "/evaluation/check-batches/" + url.PathEscape(id) + "?" + url.Values{"confirmed_case": {caseID}, "confirmed_revision": {strconv.Itoa(revision)}}.Encode() + "#case-" + caseID
}
