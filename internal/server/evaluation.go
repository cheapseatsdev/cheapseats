package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func (s *Server) evaluationStore() *evaluation.Workbench { return evaluation.New(s.App.DB.DB) }

func (s *Server) evaluationActor(r *http.Request) string {
	if actor := s.who(r, ""); actor != "" {
		return actor
	}
	// Attribute token sessions without ever storing or displaying the credential.
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if c, err := r.Cookie(s.cookie); err == nil && token == "" {
		token = c.Value
	}
	if token != "" {
		h := sha256.Sum256([]byte(token))
		return "token reviewer " + hex.EncodeToString(h[:8])
	}
	return "local reviewer"
}

func (s *Server) evaluationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /evaluation", s.evaluationPage)
	mux.HandleFunc("POST /evaluation/import", s.evaluationImport)
	mux.HandleFunc("POST /evaluation/sessions", s.evaluationStart)
	mux.HandleFunc("GET /evaluation/sessions/{id}", s.evaluationSession)
	mux.HandleFunc("GET /evaluation/cases/{id}", s.evaluationCase)
	mux.HandleFunc("POST /evaluation/cases/{id}", s.evaluationSave)
	mux.HandleFunc("POST /api/evaluation/cases/{id}/draft", s.evaluationDraft)
	s.evaluationDatasetRoutes(mux)
	s.evaluationCheckRoutes(mux)
	s.evaluationExperimentRoutes(mux)
	s.evaluationBatchRoutes(mux)
}

type evaluationRow struct {
	ID, Prompt, Set, Answerer, Track, State string
	Pending                                 bool
}

type evaluationHome struct {
	Imports                      []evaluation.Import
	Import                       *evaluation.Import
	Rows                         []evaluationRow
	Reviewed, Unresolved, Drafts int
	Pending                      int
	Flow                         evaluationFlow
	Resume                       *evaluation.Session
	ResumePosition               int
	Error                        string
	ReviewFinished               bool
	Confirmation                 evaluationConfirmation
	Datasets                     []evaluation.Dataset
	BatchOffer                   *evaluation.CheckBatchOffer
	Batches                      []evaluation.CheckBatch
}

func (s *Server) evaluationHomeData(r *http.Request) (evaluationHome, error) {
	w := s.evaluationStore()
	d := evaluationHome{Rows: []evaluationRow{}, ReviewFinished: r.URL.Query().Get("review") == "finished"}
	d.Confirmation = s.evaluationConfirmation(r)
	var err error
	d.Imports, err = w.Imports(r.Context())
	if err != nil || len(d.Imports) == 0 {
		return d, err
	}
	id := r.URL.Query().Get("import")
	if id == "" {
		id = d.Imports[0].ID
	}
	a, err := w.Import(r.Context(), id)
	if err != nil {
		return d, err
	}
	d.Import = &a
	d.Datasets, err = w.Datasets(r.Context(), id)
	if err != nil {
		return d, err
	}
	cs, err := w.ImportCases(r.Context(), id)
	if err != nil {
		return d, err
	}
	for _, c := range cs {
		if c.FirstReviewed {
			d.Reviewed++
		}
		if c.Review.State == "unresolved" {
			d.Unresolved++
		}
		if c.Review.State == "draft" {
			d.Drafts++
		}
		row := evaluationRow{ID: c.ID, Prompt: c.Record.Prompt, Set: c.Record.Set, State: c.Review.State}
		if c.FirstReviewed {
			views, err := w.CheckViews(r.Context(), c)
			if err != nil {
				return d, err
			}
			for _, v := range views {
				if v.State == evaluation.CheckDone && !v.Stale && !v.Resolved && len(v.Fields) > 0 {
					row.Pending = true
				}
			}
			d.Flow.add(c, views, s.evaluationAssessor().CheckPlan(c.ID, 0).ID, id)
			if row.Pending {
				d.Pending++
			}
		}
		if c.Record.Source != nil {
			row.Answerer, row.Track = c.Record.Source.Answerer, c.Record.Source.Track
		}
		d.Rows = append(d.Rows, row)
	}
	if d.Flow.Eligible > 0 && d.Flow.Eligible <= 500 {
		offer, e := w.PrepareCheckBatch(r.Context(), s.evaluationAssessor(), id, "", s.evaluationActor(r))
		if e != nil {
			return d, e
		}
		d.BatchOffer = &offer
	}
	d.Batches, err = w.CheckBatches(r.Context(), id, s.evaluationActor(r))
	if err != nil {
		return d, err
	}
	d.Resume, err = w.Resume(r.Context(), id, s.evaluationActor(r))
	if d.Resume != nil {
		d.ResumePosition = d.Resume.Cursor + 1
	}
	return d, err
}

func evaluationCode(err error) int {
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	if errors.Is(err, evaluation.ErrConflict) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func (s *Server) evaluationPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationHomeData(r)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation", d)
}

func (s *Server) evaluationImport(w http.ResponseWriter, r *http.Request) {
	data, err := grade.BundledGolden()
	if err == nil {
		var a evaluation.Import
		a, err = s.evaluationStore().ImportJSONL(r.Context(), "bundled golden fixtures", data)
		if err == nil {
			http.Redirect(w, r, "/evaluation?import="+url.QueryEscape(a.ID), http.StatusSeeOther)
			return
		}
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

func (s *Server) evaluationStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	session, err := s.evaluationStore().Start(r.Context(), r.FormValue("import"), s.evaluationActor(r), r.FormValue("queue"))
	if err != nil {
		r.URL.RawQuery = url.Values{"import": {r.FormValue("import")}}.Encode()
		d, loadErr := s.evaluationHomeData(r)
		if loadErr != nil {
			http.Error(w, loadErr.Error(), evaluationCode(loadErr))
			return
		}
		d.Error = err.Error()
		w.WriteHeader(evaluationCode(err))
		s.render(w, "evaluation", d)
		return
	}
	http.Redirect(w, r, "/evaluation/sessions/"+session.ID, http.StatusSeeOther)
}

type evaluationCaseData struct {
	ID, Prompt, Brand, About, Intent, Answer, Actor, Rubric string
	Aliases                                                 []string
	Judgment                                                evaluation.Judgment
	Revision                                                int
	State                                                   string
	FirstReviewed                                           bool
	Original                                                *grade.GoldenRecord
	History                                                 []evaluation.Event
	Session                                                 *evaluation.Session
	ImportID                                                string
	Error                                                   string
	Saved                                                   bool
	Position                                                int
	Passages                                                []evaluationPassage
	TopPickValue                                            string
	Rules                                                   []evaluationRule
	Checks                                                  []evaluation.CheckView
	Pending                                                 []string
	Resolutions                                             []evaluation.ResolutionRecord
	Resolution                                              bool
	ResolutionNote                                          string
	RequireNote                                             bool
	CheckQueue                                              bool
	CheckBatchID                                            string
	SavedJudgment                                           evaluation.Judgment
	NextCheckURL, NextCheckAction                           string
	CheckPosition, CheckTotal                               int
	CheckDone, CheckPending                                 int
	Confirmation                                            evaluationConfirmation
}

type evaluationPassage struct {
	Index int
	Text  string
}
type evaluationRule struct{ Name, Definition string }

func (d *evaluationCaseData) fields() {
	d.Passages = nil
	n := len(d.Judgment.Evidence)
	if n < 3 {
		n = 3
	}
	for i := 0; i < n; i++ {
		p := ""
		if i < len(d.Judgment.Evidence) {
			p = d.Judgment.Evidence[i]
		}
		d.Passages = append(d.Passages, evaluationPassage{i + 1, p})
	}
	d.TopPickValue = ""
	if d.Judgment.TopPick != nil {
		d.TopPickValue = strconv.FormatBool(*d.Judgment.TopPick)
	}
	for _, v := range grade.Verdicts {
		d.Rules = append(d.Rules, evaluationRule{string(v), grade.Definitions[v]})
	}
}

func (s *Server) evaluationCaseData(r *http.Request, id string, session *evaluation.Session) (evaluationCaseData, error) {
	c, err := s.evaluationStore().Case(r.Context(), id)
	if err != nil {
		return evaluationCaseData{}, err
	}
	d := evaluationCaseData{ID: c.ID, Prompt: c.Record.Prompt, Brand: c.Record.Brand.Name, About: c.Record.Brand.About, Aliases: c.Record.Brand.Aliases, Intent: c.Record.Intent, Answer: c.Record.Answer, Actor: s.evaluationActor(r), Rubric: evaluation.Rubric(), Judgment: c.Review.Judgment, Revision: c.Review.Revision, State: c.Review.State, FirstReviewed: c.FirstReviewed, Session: session, ImportID: r.URL.Query().Get("import")}
	d.CheckQueue = r.URL.Query().Get("checks") == "1" || r.FormValue("checks") == "1"
	d.CheckBatchID = r.FormValue("batch")
	d.SavedJudgment = c.Review.Judgment
	d.Confirmation = s.evaluationConfirmation(r)
	if d.Intent == "" {
		d.Intent = "choice"
	}
	if session != nil {
		d.ImportID = session.ImportID
		d.Position = session.Cursor + 1
	}
	// Never send the previous model label, quote or note in a first-review page,
	// even inside a closed disclosure or a hidden input.
	if c.FirstReviewed {
		d.Original = &c.Record
		d.Checks, err = s.evaluationStore().CheckViews(r.Context(), c)
		if err != nil {
			return d, err
		}
		d.Pending, err = s.evaluationStore().PendingChecks(r.Context(), c)
		if err != nil {
			return d, err
		}
		d.Resolutions, err = s.evaluationStore().ResolutionHistory(r.Context(), id)
		if err != nil {
			return d, err
		}
	}
	d.History, err = s.evaluationStore().History(r.Context(), id)
	d.fields()
	return d, err
}

func (s *Server) evaluationCase(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationCaseData(r, r.PathValue("id"), nil)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	if d.State != "reviewed" {
		d.CheckQueue = false
	}
	if d.CheckQueue || d.State == "reviewed" && len(d.Checks) > 0 {
		d.CheckQueue = true
		d.Resolution = len(d.Pending) > 0
		if err := s.evaluationCheckNavigation(r, &d); err != nil {
			http.Error(w, err.Error(), evaluationCode(err))
			return
		}
	}
	s.render(w, "evaluation-case", d)
}

func (s *Server) evaluationSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.evaluationStore().Session(r.Context(), r.PathValue("id"), s.evaluationActor(r))
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	if session.Cursor >= len(session.CaseIDs) {
		s.evaluationSessionFinished(w, r, session)
		return
	}
	d, err := s.evaluationCaseData(r, session.CaseIDs[session.Cursor], &session)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation-case", d)
}

func evaluationForm(r *http.Request) (evaluation.Judgment, int, error) {
	if err := r.ParseForm(); err != nil {
		return evaluation.Judgment{}, 0, err
	}
	revision, err := strconv.Atoi(r.FormValue("revision"))
	if err != nil || revision < 0 {
		return evaluation.Judgment{}, 0, errors.New("the saved revision is missing; reload the case")
	}
	j := evaluation.Judgment{Verdict: grade.Verdict(r.FormValue("verdict")), Tone: grade.Tone(r.FormValue("tone")), AbsenceReason: r.FormValue("absence_reason"), FullAnswer: r.FormValue("full_answer") == "on", Incomplete: r.FormValue("incomplete") == "on", Issue: r.FormValue("issue"), Note: r.FormValue("note")}
	if r.FormValue("top_pick") != "" {
		b, err := strconv.ParseBool(r.FormValue("top_pick"))
		if err != nil {
			return j, revision, errors.New("choose yes or no for top pick")
		}
		j.TopPick = &b
	}
	for _, p := range r.Form["evidence"] {
		if strings.TrimSpace(p) != "" {
			j.Evidence = append(j.Evidence, p)
		}
	}
	return j, revision, nil
}

func (s *Server) evaluationDraft(w http.ResponseWriter, r *http.Request) {
	j, revision, err := evaluationForm(r)
	if err == nil {
		var saved evaluation.Review
		saved, err = s.evaluationStore().Save(r.Context(), r.PathValue("id"), revision, "draft", j, s.evaluationActor(r), r.FormValue("rubric"))
		if err == nil {
			writeJSON(w, saved)
			return
		}
	}
	httpErr(w, err, evaluationCode(err))
}

func (s *Server) evaluationSave(w http.ResponseWriter, r *http.Request) {
	j, revision, err := evaluationForm(r)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	wb := s.evaluationStore()
	id, actor := r.PathValue("id"), s.evaluationActor(r)
	var session *evaluation.Session
	if sid := r.FormValue("session"); sid != "" {
		loaded, err := wb.Session(r.Context(), sid, actor)
		cursor, e := strconv.Atoi(r.FormValue("cursor"))
		if err != nil || e != nil || cursor != loaded.Cursor || cursor < 0 || cursor >= len(loaded.CaseIDs) || loaded.CaseIDs[cursor] != id {
			http.Error(w, evaluation.ErrConflict.Error(), http.StatusConflict)
			return
		}
		session = &loaded
	}
	action := r.FormValue("action")
	kind := action
	continueReview := session == nil && (action == "judgment-next" || action == "unresolved-next" || action == "continue-review")
	if continueReview {
		// A queue continuation belongs to the import shown on this form.
		imported, e := wb.Import(r.Context(), r.FormValue("import"))
		found := false
		for _, row := range imported.Rows {
			found = found || row.CaseID == id
		}
		if e != nil || !found {
			err = errors.New("reload this answer from its review queue before continuing")
		}
	}
	if action == "judgment-next" {
		kind = "judgment"
	} else if action == "unresolved-next" {
		kind = "unresolved"
	}
	if action == "end" {
		kind = "draft"
	}
	batchID := r.FormValue("batch")
	if err == nil && batchID != "" {
		_, err = s.evaluationBatchCase(r, batchID, id)
	}
	checkImportID := ""
	if err == nil && r.FormValue("checks") == "1" && session == nil {
		checkImportID, err = s.evaluationCheckImport(r.Context(), r.FormValue("import"), id)
	}
	saved := false
	if err == nil && (action == "next" || action == "continue-review") {
		c, e := wb.Case(r.Context(), id)
		if e != nil {
			err = e
		} else if c.Review.Revision != revision || c.Review.State != "reviewed" && c.Review.State != "unresolved" {
			err = evaluation.ErrConflict
		} else {
			saved = true
		}
	} else if err == nil {
		var review evaluation.Review
		if action == "resolve-keep" || action == "resolve-change" {
			resolution := evaluation.Resolution{Checks: r.Form["check"], Action: strings.TrimPrefix(action, "resolve-"), Explanation: r.FormValue("resolution_note")}
			review, err = wb.Resolve(r.Context(), id, revision, j, actor, r.FormValue("rubric"), resolution)
		} else {
			review, err = wb.Save(r.Context(), id, revision, kind, j, actor, r.FormValue("rubric"))
		}
		if err == nil {
			revision = review.Revision
			saved = true
		}
	}
	if err == nil && session != nil {
		err = wb.Advance(r.Context(), session.ID, actor, id, session.Cursor, action == "end")
	}
	if err == nil && continueReview {
		var target string
		target, err = s.evaluationNextReview(r, r.FormValue("import"), id)
		if err == nil {
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
	}
	if err == nil && batchID != "" {
		http.Redirect(w, r, evaluationBatchReturn(batchID, id, revision), http.StatusSeeOther)
		return
	}
	if err == nil && r.FormValue("checks") == "1" && session == nil {
		var target string
		target, err = s.evaluationNextCheck(r, checkImportID, id)
		if err == nil {
			u, _ := url.Parse(target)
			q := u.Query()
			q.Set("confirmed_case", id)
			q.Set("confirmed_revision", strconv.Itoa(revision))
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.String(), http.StatusSeeOther)
			return
		}
	}
	if err != nil {
		d, loadErr := s.evaluationCaseData(r, id, session)
		if loadErr != nil {
			http.Error(w, loadErr.Error(), evaluationCode(loadErr))
			return
		}
		// Retain typed fields on failure. Keep a stale revision stale: silently
		// replacing it would let a retry overwrite another tab's saved work.
		d.Judgment, d.Revision, d.Error, d.Saved = j, revision, err.Error(), saved
		if session == nil {
			d.ImportID = r.FormValue("import")
		}
		d.Resolution = r.FormValue("resolving") == "1"
		d.ResolutionNote = r.FormValue("resolution_note")
		if d.Resolution {
			d.Pending = append([]string(nil), r.Form["check"]...)
		}
		d.Rules = nil
		d.fields()
		if d.CheckQueue {
			if e := s.evaluationCheckNavigation(r, &d); e != nil {
				d.CheckQueue = false
			}
		}
		w.WriteHeader(evaluationCode(err))
		s.render(w, "evaluation-case", d)
		return
	}
	if session != nil && action != "end" {
		http.Redirect(w, r, "/evaluation/sessions/"+session.ID+"?advanced=1", http.StatusSeeOther)
		return
	}
	if session != nil {
		http.Redirect(w, r, "/evaluation?import="+url.QueryEscape(session.ImportID), http.StatusSeeOther)
		return
	}
	target := fmt.Sprintf("/evaluation/cases/%s?import=%s&saved=1", id, url.QueryEscape(r.FormValue("import")))
	if action == "resolve-keep" || action == "resolve-change" {
		target += fmt.Sprintf("&resolved_revision=%d", revision)
	}
	if r.FormValue("checks") == "1" {
		target += "&checks=1"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
