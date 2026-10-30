package server

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
)

func (s *Server) evaluationAssessor() evaluation.Assessor {
	return evaluation.Assessor{Cfg: s.App.Cfg, Budget: s.App.Runs.Budget}
}
func (s *Server) evaluationCheckRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /evaluation/checks", s.evaluationChecksPage)
	mux.HandleFunc("GET /evaluation/checks/{id}", s.evaluationCheckPreview)
	mux.HandleFunc("POST /evaluation/checks/{id}", s.evaluationCheckRun)
	mux.HandleFunc("GET /evaluation/cases/{id}/resolve", s.evaluationResolve)
}

type evaluationCheckData struct {
	ID, Question, ReadyError, Error, ImportID string
	Plan                                      evaluation.CheckPlan
	Check                                     *evaluation.Check
	Estimate                                  float64
	Reservation                               float64
	Subscription                              bool
	Confirmation                              evaluationConfirmation
}

func (s *Server) evaluationCheckData(r *http.Request) (evaluationCheckData, error) {
	wb, a := s.evaluationStore(), s.evaluationAssessor()
	c, err := wb.Case(r.Context(), r.PathValue("id"))
	if err != nil {
		return evaluationCheckData{}, err
	}
	if !c.FirstReviewed || c.Review.State != "reviewed" {
		return evaluationCheckData{}, errors.New("save a human judgment before requesting a check; drafts and unresolved reviews remain editable from the case page")
	}
	d := evaluationCheckData{ID: c.ID, Question: c.Record.Prompt, Reservation: a.Budget.Allowance(), Subscription: a.Cfg.Grader.Via() == "claude-code"}
	d.ImportID, err = s.evaluationCheckImport(r.Context(), r.FormValue("import"), c.ID)
	if err != nil {
		return d, err
	}
	d.Confirmation = s.evaluationConfirmation(r)
	d.Plan = a.CheckPlan(c.ID, 5*d.Reservation)
	d.Estimate = a.Estimate(c.Record, d.Plan.Prompt)
	if err := a.Ready(); err != nil {
		d.ReadyError = err.Error()
	}
	check, err := wb.Check(r.Context(), d.Plan.ID)
	if err == nil {
		d.Check = &check
		d.Plan = check.Plan
	} else if !errors.Is(err, sql.ErrNoRows) {
		return d, err
	}
	return d, nil
}
func (s *Server) evaluationCheckPreview(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationCheckData(r)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation-check", d)
}
func (s *Server) evaluationCheckRun(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationCheckData(r)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	if r.FormValue("plan") != d.Plan.ID {
		http.Error(w, "the check configuration changed; reload its preview", 409)
		return
	}
	if d.ReadyError != "" {
		d.Error = d.ReadyError
		w.WriteHeader(400)
		s.render(w, "evaluation-check", d)
		return
	}
	if d.Check == nil {
		amount, e := strconv.ParseFloat(r.FormValue("max_usd"), 64)
		if e != nil {
			http.Error(w, "choose an admission budget", 400)
			return
		}
		d.Plan.MaxUSD = amount
	}
	checked, err := s.evaluationStore().RunCheck(r.Context(), d.Plan, s.evaluationActor(r), r.FormValue("retry") == "1", s.evaluationAssessor().Assess)
	if err != nil {
		d.Error = err.Error()
		if checked.Plan.ID != "" {
			d.Check = &checked
		}
		w.WriteHeader(evaluationCode(err))
		s.render(w, "evaluation-check", d)
		return
	}
	q := url.Values{"import": {d.ImportID}, "checks": {"1"}}.Encode()
	http.Redirect(w, r, "/evaluation/cases/"+d.ID+"/resolve?"+q, http.StatusSeeOther)
}
func (s *Server) evaluationResolve(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationCaseData(r, r.PathValue("id"), nil)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	if !d.FirstReviewed {
		http.Error(w, "save your own judgment before viewing independent assessments", 400)
		return
	}
	if d.State != "reviewed" {
		http.Error(w, "finish your draft or unresolved judgment on the case page before resolving check differences", 400)
		return
	}
	d.Resolution = len(d.Pending) > 0
	d.CheckQueue = true
	if err := s.evaluationCheckNavigation(r, &d); err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation-case", d)
}

func (s *Server) evaluationCheckNavigation(r *http.Request, d *evaluationCaseData) error {
	var err error
	d.ImportID, err = s.evaluationCheckImport(r.Context(), d.ImportID, d.ID)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	q.Set("import", d.ImportID)
	r.URL.RawQuery = q.Encode()
	home, err := s.evaluationHomeData(r)
	if err != nil {
		return err
	}
	d.CheckTotal = len(home.Flow.Rows)
	d.CheckDone, d.CheckPending = home.Flow.Done, home.Flow.Pending
	for i, row := range home.Flow.Rows {
		if row.ID == d.ID {
			d.CheckPosition = i + 1
		}
	}
	d.NextCheckURL, err = s.evaluationNextCheck(r, d.ImportID, d.ID)
	if err != nil {
		return err
	}
	d.NextCheckAction = "Continue to next check"
	if strings.HasPrefix(d.NextCheckURL, "/evaluation/checks?") {
		d.NextCheckAction = "Finish checking"
	}
	return nil
}
