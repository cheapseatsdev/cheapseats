package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func (s *Server) evaluationExperimentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /evaluation/experiments/new", s.evaluationExperimentNew)
	mux.HandleFunc("POST /evaluation/experiments/preview", s.evaluationExperimentPreview)
	mux.HandleFunc("POST /evaluation/experiments", s.evaluationExperimentCreate)
	mux.HandleFunc("GET /evaluation/experiments/{id}", s.evaluationExperiment)
	mux.HandleFunc("POST /evaluation/experiments/{id}/run", s.evaluationExperimentRun)
	mux.HandleFunc("POST /evaluation/experiments/{id}/stop", s.evaluationExperimentStop)
	mux.HandleFunc("GET /evaluation/experiments/{id}/report.json", s.evaluationExperimentJSON)
	mux.HandleFunc("GET /evaluation/experiments/{id}/review/{case}", s.evaluationExperimentReview)
	mux.HandleFunc("POST /evaluation/experiments/{id}/review/{case}", s.evaluationExperimentReviewSave)
}

type evaluationExperimentSetup struct {
	Dataset                                                                evaluation.Dataset
	Partition, Name1, Prompt1, Name2, Prompt2, Error, ReadyError, PlanJSON string
	Budget                                                                 float64
	Frozen                                                                 bool
	Plan                                                                   *evaluation.ExperimentPlan
	Estimate                                                               float64
}

func (s *Server) evaluationExperimentSetup(r *http.Request) (evaluationExperimentSetup, error) {
	d := evaluationExperimentSetup{Partition: "development", Budget: 10, Name1: "Candidate 1"}
	dataset := r.FormValue("dataset")
	var err error
	d.Dataset, err = s.evaluationStore().Dataset(r.Context(), dataset)
	if err != nil {
		return d, err
	}
	if err := s.evaluationAssessor().Ready(); err != nil {
		d.ReadyError = err.Error()
	}
	return d, nil
}
func (s *Server) evaluationExperimentNew(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationExperimentSetup(r)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation-experiment-new", d)
}
func (s *Server) evaluationExperimentPreview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	d, err := s.evaluationExperimentSetup(r)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	d.Partition, d.Name1, d.Prompt1, d.Name2, d.Prompt2 = r.FormValue("partition"), r.FormValue("name1"), r.FormValue("prompt1"), r.FormValue("name2"), r.FormValue("prompt2")
	d.Frozen = r.FormValue("frozen") == "on"
	d.Budget, err = strconv.ParseFloat(r.FormValue("budget"), 64)
	if err != nil {
		d.Error = "choose an admission budget"
		w.WriteHeader(400)
		s.render(w, "evaluation-experiment-new", d)
		return
	}
	variants := []evaluation.Variant{{Name: d.Name1, Prompt: d.Prompt1}}
	if d.Prompt2 != "" || d.Name2 != "" {
		variants = append(variants, evaluation.Variant{Name: d.Name2, Prompt: d.Prompt2})
	}
	p, err := s.evaluationStore().PrepareExperiment(r.Context(), s.evaluationAssessor(), d.Dataset.ID, d.Partition, variants, d.Budget, d.Frozen)
	if err != nil {
		d.Error = err.Error()
		w.WriteHeader(evaluationCode(err))
	} else {
		d.Plan = &p
		b, _ := json.Marshal(p)
		d.PlanJSON = string(b)
		for _, m := range d.Dataset.Members {
			if m.Group.Partition == p.Partition {
				for _, v := range p.Variants {
					d.Estimate += 2 * s.evaluationAssessor().Estimate(m.Record, v.Prompt)
				}
			}
		}
	}
	s.render(w, "evaluation-experiment-new", d)
}
func (s *Server) evaluationExperimentCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var p evaluation.ExperimentPlan
	if err := json.Unmarshal([]byte(r.FormValue("plan")), &p); err != nil {
		http.Error(w, "invalid comparison preview", 400)
		return
	}
	e, err := s.evaluationStore().CreateExperiment(r.Context(), s.evaluationAssessor(), p, s.evaluationActor(r))
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	http.Redirect(w, r, "/evaluation/experiments/"+e.Plan.ID, http.StatusSeeOther)
}

type evaluationExperimentPage struct {
	Report     evaluation.ExperimentReport
	Running    bool
	ReadyError string
}

func (s *Server) evaluationExperiment(w http.ResponseWriter, r *http.Request) {
	report, err := s.evaluationStore().ExperimentReport(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	_, running := s.evaluationJobs.Load(report.Experiment.Plan.ID)
	d := evaluationExperimentPage{Report: report, Running: running}
	if err := s.evaluationAssessor().Ready(); err != nil {
		d.ReadyError = err.Error()
	}
	s.render(w, "evaluation-experiment", d)
}

type evaluationJob struct{ Cancel context.CancelFunc }

func (s *Server) evaluationExperimentRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, err := s.evaluationStore().Experiment(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	a := s.evaluationAssessor()
	if e.Plan.Model != a.Cfg.Grader.Ref() || e.Plan.Transport != a.Transport() || e.Plan.Evaluator != grade.EvaluatorDigest() || e.Plan.Rubric != evaluation.Rubric() {
		http.Error(w, "resume requires the original model, transport, evaluator build and rules", 409)
		return
	}
	if err := s.evaluationAssessor().Ready(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	job := &evaluationJob{Cancel: cancel}
	if _, loaded := s.evaluationJobs.LoadOrStore(id, job); loaded {
		cancel()
	} else {
		retry := r.FormValue("retry") == "on"
		go func() {
			defer cancel()
			defer s.evaluationJobs.Delete(id)
			if err := s.evaluationStore().RunExperiment(ctx, id, s.evaluationAssessor(), retry, s.evaluationAssessor().Assess); err != nil {
				slog.Info("evaluation comparison paused", "experiment", id, "error", err)
			}
		}()
	}
	http.Redirect(w, r, "/evaluation/experiments/"+id, http.StatusSeeOther)
}
func (s *Server) evaluationExperimentStop(w http.ResponseWriter, r *http.Request) {
	if job, ok := s.evaluationJobs.Load(r.PathValue("id")); ok {
		job.(*evaluationJob).Cancel()
	}
	http.Redirect(w, r, "/evaluation/experiments/"+r.PathValue("id"), http.StatusSeeOther)
}
func (s *Server) StopEvaluationJobs() {
	s.evaluationJobs.Range(func(_, v any) bool { v.(*evaluationJob).Cancel(); return true })
}
func (s *Server) evaluationExperimentJSON(w http.ResponseWriter, r *http.Request) {
	report, err := s.evaluationStore().ExperimentReport(r.Context(), r.PathValue("id"))
	if err != nil {
		httpErr(w, err, evaluationCode(err))
		return
	}
	writeJSON(w, report)
}

type evaluationExperimentReviewData struct {
	Form                                evaluationCaseData
	Blinded                             evaluation.BlindedReview
	ExperimentID, CaseID, Action, Error string
}

func (s *Server) evaluationExperimentReviewData(r *http.Request) (evaluationExperimentReviewData, error) {
	id, caseID := r.PathValue("id"), r.PathValue("case")
	b, err := s.evaluationStore().BlindedReview(r.Context(), id, caseID, s.evaluationActor(r))
	if err != nil {
		return evaluationExperimentReviewData{}, err
	}
	m := b.Member
	f := evaluationCaseData{ID: caseID, Prompt: m.Record.Prompt, Brand: m.Record.Brand.Name, About: m.Record.Brand.About, Aliases: m.Record.Brand.Aliases, Answer: m.Record.Answer, Intent: m.Record.Intent, Judgment: b.Review.Judgment, Revision: b.Review.Revision, Rubric: evaluation.Rubric(), Actor: s.evaluationActor(r), RequireNote: true}
	f.fields()
	return evaluationExperimentReviewData{Form: f, Blinded: b, ExperimentID: id, CaseID: caseID, Action: "/evaluation/experiments/" + url.PathEscape(id) + "/review/" + url.PathEscape(caseID)}, nil
}
func (s *Server) evaluationExperimentReview(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationExperimentReviewData(r)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation-experiment-review", d)
}
func (s *Server) evaluationExperimentReviewSave(w http.ResponseWriter, r *http.Request) {
	j, revision, err := evaluationForm(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.FormValue("rubric") != evaluation.Rubric() {
		http.Error(w, "review rules changed; reload", 409)
		return
	}
	saved, err := s.evaluationStore().SaveExperimentReview(r.Context(), r.PathValue("id"), r.PathValue("case"), s.evaluationActor(r), revision, r.FormValue("action"), j)
	if err != nil {
		d, e := s.evaluationExperimentReviewData(r)
		if e != nil {
			http.Error(w, e.Error(), evaluationCode(e))
			return
		}
		d.Error = err.Error()
		d.Form.Judgment = j
		d.Form.Revision = revision
		d.Form.Rules = nil
		d.Form.fields()
		w.WriteHeader(evaluationCode(err))
		s.render(w, "evaluation-experiment-review", d)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/evaluation/experiments/%s/review/%s?resolved_revision=%d", r.PathValue("id"), r.PathValue("case"), saved.Revision), http.StatusSeeOther)
}
