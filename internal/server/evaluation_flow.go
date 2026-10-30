package server

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
)

// Called only after an explicit save/continue POST. Rejoining an active session
// must acknowledge the just-saved case if it is still the session's current item.
func (s *Server) evaluationNextReview(r *http.Request, importID, caseID string) (string, error) {
	wb, actor := s.evaluationStore(), s.evaluationActor(r)
	session, err := wb.Start(r.Context(), importID, actor, "first")
	if errors.Is(err, evaluation.ErrNoReviewCases) {
		return "/evaluation?" + url.Values{"import": {importID}, "review": {"finished"}}.Encode(), nil
	}
	if err != nil {
		return "", err
	}
	if session.Cursor < len(session.CaseIDs) && session.CaseIDs[session.Cursor] == caseID {
		if err := wb.Advance(r.Context(), session.ID, actor, caseID, session.Cursor, false); err != nil {
			return "", err
		}
	}
	return "/evaluation/sessions/" + session.ID + "?advanced=1", nil
}

type evaluationCheckRow struct {
	ID, Prompt, Status, Action, URL string
}

type evaluationFlow struct {
	Rows                                       []evaluationCheckRow
	Eligible, Unchecked, Failed, Running, Done int
	Pending                                    int
	NextURL, NextAction                        string
	nextPriority                               int
}

// The queue reflects the current grader configuration. Other current-evaluator
// disagreements still need a human resolution before approval.
func (f *evaluationFlow) add(c evaluation.Case, views []evaluation.CheckView, currentID, importID string) {
	if c.Review.State != "reviewed" {
		return
	}
	f.Eligible++
	q := url.Values{"import": {importID}, "checks": {"1"}}.Encode()
	row := evaluationCheckRow{ID: c.ID, Prompt: c.Record.Prompt, Status: "not checked", Action: "Preview check", URL: "/evaluation/checks/" + c.ID + "?" + q}
	priority := 2
	found, pending, otherRunning := false, false, false
	for _, v := range views {
		if v.State == evaluation.CheckDone && !v.Stale && !v.Resolved && len(v.Fields) > 0 {
			pending = true
		}
		if v.Plan.ID != currentID {
			otherRunning = otherRunning || !v.Stale && v.State == evaluation.CheckRunning
			continue
		}
		found = true
		switch v.State {
		case evaluation.CheckDone:
			f.Done++
			row.Status, row.Action = "check complete", "View result"
			row.URL = "/evaluation/cases/" + c.ID + "/resolve?" + q
			priority = 5
		case evaluation.CheckRunning:
			f.Running++
			row.Status, row.Action, priority = "check running", "View check status", 4
		case evaluation.CheckFailed:
			f.Failed++
			row.Status, row.Action, priority = "check failed", "View failed check", 3
		default:
			f.Unchecked++
		}
	}
	if !found {
		f.Unchecked++
	}
	if otherRunning {
		row.Status += "; another assessment running"
		if priority == 5 {
			row.Action, row.URL, priority = "View running assessment", "/evaluation/cases/"+c.ID+"?"+q, 4
		}
	}
	if pending {
		f.Pending++
		row.Status, row.Action, priority = "needs your decision", "Resolve differences", 1
		row.URL = "/evaluation/cases/" + c.ID + "/resolve?" + q
	}
	f.Rows = append(f.Rows, row)
	if priority < 5 && (f.nextPriority == 0 || priority < f.nextPriority) {
		f.NextURL, f.NextAction, f.nextPriority = row.URL, row.Action, priority
	}
}

func (s *Server) evaluationChecksPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.evaluationHomeData(r)
	if err != nil {
		http.Error(w, err.Error(), evaluationCode(err))
		return
	}
	s.render(w, "evaluation-checks", d)
}

type evaluationSessionSummary struct {
	ImportID                            string
	Total, Reviewed, Unresolved, Drafts int
}

func (s *Server) evaluationSessionFinished(w http.ResponseWriter, r *http.Request, session evaluation.Session) {
	d := evaluationSessionSummary{ImportID: session.ImportID, Total: len(session.CaseIDs)}
	for _, id := range session.CaseIDs {
		c, err := s.evaluationStore().Case(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), evaluationCode(err))
			return
		}
		switch c.Review.State {
		case "reviewed":
			d.Reviewed++
		case "unresolved":
			d.Unresolved++
		case "draft":
			d.Drafts++
		}
	}
	s.render(w, "evaluation-session", d)
}
