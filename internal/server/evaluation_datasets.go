package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
)

func (s *Server) evaluationDatasetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /evaluation/datasets/prepare", s.evaluationPrepareDataset)
	mux.HandleFunc("GET /evaluation/datasets/new", s.evaluationApproval)
	mux.HandleFunc("POST /evaluation/datasets", s.evaluationApproveDataset)
	mux.HandleFunc("GET /evaluation/datasets/{id}", s.evaluationDataset)
	mux.HandleFunc("GET /evaluation/datasets/{id}/export.jsonl", s.evaluationDatasetExport)
	mux.HandleFunc("GET /evaluation/datasets/{id}/manifest.json", s.evaluationDatasetManifest)
}

type evaluationApprovalRow struct {
	ID, Prompt, State, Partition, GroupID, Exclusion string
	Revision                                         int
	Included                                         bool
}
type evaluationApprovalData struct {
	Import      evaluation.Import
	Rows        []evaluationApprovalRow
	Grouping    string
	Name, Error string
}

func (s *Server) evaluationPrepareDataset(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("import")
	if _, err := s.evaluationStore().Grouping(r.Context(), id); err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	http.Redirect(w, r, "/evaluation/datasets/new?import="+url.QueryEscape(id), http.StatusSeeOther)
}
func (s *Server) evaluationApprovalData(r *http.Request, p *evaluation.Approval) (evaluationApprovalData, error) {
	wb := s.evaluationStore()
	id := r.URL.Query().Get("import")
	if p != nil {
		id = p.ImportID
	}
	d := evaluationApprovalData{Rows: []evaluationApprovalRow{}, Name: "Reviewed answers"}
	var err error
	d.Import, err = wb.Import(r.Context(), id)
	if err != nil {
		return d, err
	}
	cs, err := wb.ImportCases(r.Context(), id)
	if err != nil {
		return d, err
	}
	g, err := wb.StoredGrouping(r.Context(), id)
	if err != nil {
		return d, err
	}
	d.Grouping = g.Method
	for _, c := range cs {
		row := evaluationApprovalRow{ID: c.ID, Prompt: c.Record.Prompt, State: c.Review.State, Revision: c.Review.Revision, Included: c.Review.State == "reviewed", Partition: g.Cases[c.ID].Partition, GroupID: g.Cases[c.ID].ID}
		if p != nil {
			revision, included := p.Members[c.ID]
			row.Included = included
			if included {
				row.Revision = revision
			}
			row.Exclusion = p.Exclusions[c.ID]
		}
		d.Rows = append(d.Rows, row)
	}
	for _, row := range d.Import.Rows {
		if row.Error != "" {
			item := evaluationApprovalRow{ID: fmt.Sprintf("line:%d", row.Line), Prompt: row.Error, State: "blocked"}
			if p != nil {
				item.Exclusion = p.Exclusions[item.ID]
			}
			d.Rows = append(d.Rows, item)
		}
	}
	if p != nil {
		d.Name = p.Name
	}
	return d, nil
}
func (s *Server) evaluationApproval(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationApprovalData(r, nil)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation-approval", d)
}
func (s *Server) evaluationApproveDataset(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	p := evaluation.Approval{ImportID: r.FormValue("import"), Name: r.FormValue("name"), Members: map[string]int{}, Exclusions: map[string]string{}}
	for _, id := range r.Form["member"] {
		revision, err := strconv.Atoi(r.FormValue("revision_" + id))
		if err != nil {
			http.Error(w, "invalid label revision", 400)
			return
		}
		p.Members[id] = revision
	}
	for key, vs := range r.Form {
		if id, ok := strings.CutPrefix(key, "exclude_"); ok && strings.TrimSpace(vs[0]) != "" {
			p.Exclusions[id] = vs[0]
		}
	}
	d, err := s.evaluationStore().Approve(r.Context(), p, s.evaluationActor(r))
	if err != nil {
		page, e := s.evaluationApprovalData(r, &p)
		if e != nil {
			http.Error(w, e.Error(), evaluationCode(e))
			return
		}
		page.Error = err.Error()
		w.WriteHeader(evaluationCode(err))
		s.render(w, "evaluation-approval", page)
		return
	}
	http.Redirect(w, r, "/evaluation/datasets/"+d.ID, http.StatusSeeOther)
}
func (s *Server) evaluationDataset(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationStore().Dataset(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation-dataset", struct {
		Dataset   evaluation.Dataset
		Baselines []evaluation.Baseline
	}{d, evaluation.Baselines(d)})
}
func (s *Server) evaluationDatasetExport(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationStore().Dataset(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	b, err := d.JSONL()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+d.ID+`.jsonl"`)
	w.Write(b)
}
func (s *Server) evaluationDatasetManifest(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationStore().Dataset(r.Context(), r.PathValue("id"))
	if err != nil {
		httpErr(w, err, evaluationCode(err))
		return
	}
	writeJSON(w, d)
}
