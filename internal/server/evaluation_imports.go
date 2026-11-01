package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

func (s *Server) evaluationImportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /evaluation/runs", s.evaluationRuns)
	mux.HandleFunc("GET /evaluation/runs/{id}", s.evaluationRunPreview)
	mux.HandleFunc("POST /evaluation/runs/{id}", s.evaluationRunImport)
}

func (s *Server) evaluationRuns(w http.ResponseWriter, r *http.Request) {
	// Read-only browsing. Opening the list creates no evaluation evidence/jobs.
	rs, err := s.App.Runs.List(r.Context(), "", 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	finished := []run.Run{}
	for _, rn := range rs {
		if rn.Status == run.Done {
			finished = append(finished, rn)
		}
	}
	s.render(w, "evaluation-runs", finished)
}

type evaluationRunImportRow struct {
	ID                      int64
	Prompt, Answerer, Track string
	Problem                 string
	Selected                bool
}

type evaluationRunImportData struct {
	ID, Name, Brand, Digest string
	Error                   string
	Rows                    []evaluationRunImportRow
	Eligible, Limit         int
}

func (s *Server) evaluationRunImportData(r *http.Request, selected map[int64]bool) (evaluationRunImportData, error) {
	p, err := s.App.EvaluationRun(r.Context(), r.PathValue("id"))
	if err != nil {
		return evaluationRunImportData{}, err
	}
	d := evaluationRunImportData{ID: p.ID, Name: p.Name, Brand: p.Brand, Digest: p.Digest, Limit: app.MaxEvaluationRunAnswers}
	// Deliberately omit model proposals and evidence, including hidden fields.
	for _, row := range p.Answers {
		d.Rows = append(d.Rows, evaluationRunImportRow{ID: row.ID, Prompt: row.Prompt, Answerer: row.Answerer, Track: row.Track, Problem: row.Problem, Selected: selected[row.ID] && row.Record != nil})
		if row.Record != nil {
			d.Eligible++
		}
	}
	return d, nil
}

func (s *Server) evaluationRunPreview(w http.ResponseWriter, r *http.Request) {
	selected := map[int64]bool{}
	if v := r.URL.Query().Get("result"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid saved answer", http.StatusBadRequest)
			return
		}
		selected[id] = true
	}
	d, err := s.evaluationRunImportData(r, selected)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation-run-import", d)
}

func (s *Server) evaluationRunImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the answer selection could not be read", http.StatusBadRequest)
		return
	}
	var ids []int64
	selected := map[int64]bool{}
	for _, v := range r.PostForm["result"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid saved answer selection", http.StatusBadRequest)
			return
		}
		ids = append(ids, id)
		selected[id] = true
	}
	a, err := s.App.ImportEvaluationRun(r.Context(), r.PathValue("id"), ids, r.PostForm.Get("digest"))
	if err == nil {
		dest := "/evaluation?import=" + url.QueryEscape(a.ID)
		if a.Cases == 1 {
			dest = fmt.Sprintf("/evaluation/cases/%s?import=%s", a.Rows[0].CaseID, url.QueryEscape(a.ID))
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	d, previewErr := s.evaluationRunImportData(r, selected)
	if previewErr != nil {
		http.Error(w, previewErr.Error(), evaluationCode(previewErr))
		return
	}
	d.Error = err.Error()
	code := http.StatusBadRequest
	if errors.Is(err, app.ErrEvaluationRunChanged) {
		code = http.StatusConflict
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	s.render(w, "evaluation-run-import", d)
}
