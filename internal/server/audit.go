package server

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/crawl"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// The site audit, the citation check and the crawler counts: free checks over plain HTTP and
// imported logs, beside the runs.

// doneRuns is the recent finished runs, newest first, for the citation check's run picker.
func (s *Server) doneRuns(ctx context.Context) []run.Run {
	all, _ := s.App.Runs.List(ctx, "", 50)
	var out []run.Run
	for _, r := range all {
		if r.Status == run.Done {
			out = append(out, r)
		}
	}
	return out
}

func (s *Server) auditPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	latest, err := s.App.LatestAudit(ctx)
	data := map[string]any{"Audit": latest, "Running": s.App.Checking("audit"), "Sites": s.App.AuditSites()}
	if err != nil {
		data["Error"] = err.Error()
	}
	runs := s.doneRuns(ctx)
	data["Runs"] = runs
	id := r.URL.Query().Get("run")
	if id == "" && len(runs) > 0 {
		id = runs[0].ID
	}
	if id != "" {
		data["RunID"] = id
		data["Checking"] = s.App.Checking("cite/" + id)
		if c, err := s.App.CitationCheck(ctx, id); err == nil && c != nil {
			data["Check"] = c
		}
	}
	s.render(w, "audit", data)
}

// background runs a check that outlives the request, with a limit of its own.
func background(name string, f func(context.Context) error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := f(ctx); err != nil {
			slog.Error(name, "err", err)
		}
	}()
}

func (s *Server) auditForm(w http.ResponseWriter, r *http.Request) {
	if !s.App.Checking("audit") {
		background("site audit", func(ctx context.Context) error {
			_, err := s.App.Audit(ctx, nil, audit.Options{})
			return err
		})
	}
	http.Redirect(w, r, "/audit", http.StatusSeeOther)
}

func (s *Server) checkCitationsForm(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("run")
	if _, err := s.App.Runs.Get(r.Context(), id); err != nil {
		http.Error(w, "no run "+id, http.StatusNotFound)
		return
	}
	if !s.App.Checking("cite/" + id) {
		background("citation check", func(ctx context.Context) error {
			_, err := s.App.CheckCitations(ctx, id, 0, audit.CiteOptions{})
			return err
		})
	}
	http.Redirect(w, r, "/audit?run="+id+"#citations", http.StatusSeeOther)
}

// crawlersPage lists pages by crawler fetches; ?days= sets the window, ?flag= keeps one kind.
func (s *Server) crawlersPage(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	rep, err := s.App.Crawlers(r.Context(), days)
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	flag := r.URL.Query().Get("flag")
	var pages []crawl.PageRow
	for _, p := range rep.Pages {
		if flag == "" || p.Flag == flag {
			pages = append(pages, p)
		}
	}
	const rows = 200
	more := 0
	if len(pages) > rows {
		pages, more = pages[:rows], len(pages)-rows
	}
	s.render(w, "crawlers", map[string]any{"Report": rep, "Pages": pages, "More": more, "Days": days, "Flag": flag, "DayOptions": []int{7, 30, 90}})
}

func (s *Server) apiAudit(w http.ResponseWriter, r *http.Request) {
	a, err := s.App.LatestAudit(r.Context())
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	writeJSON(w, a)
}

// apiRunAudit audits the brand's sites and waits for it.
func (s *Server) apiRunAudit(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.Audit(r.Context(), nil, audit.Options{})
	if err != nil {
		httpErr(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, res)
}

func (s *Server) apiCitationCheck(w http.ResponseWriter, r *http.Request) {
	c, err := s.App.CitationCheck(r.Context(), r.PathValue("id"))
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	writeJSON(w, c)
}

// apiRunCitationCheck checks a run's most-cited pages (?n=, default 25) and waits for it.
func (s *Server) apiRunCitationCheck(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	c, err := s.App.CheckCitations(r.Context(), r.PathValue("id"), n, audit.CiteOptions{})
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, c)
}

func (s *Server) apiCrawlers(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	rep, err := s.App.Crawlers(r.Context(), days)
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	writeJSON(w, rep)
}
