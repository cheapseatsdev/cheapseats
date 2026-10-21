// Package server is popcorn's web UI (server-rendered, no build step) and JSON API.
//
// Auth: when POPCORN_TOKENS is set (comma-separated, so one can be rotated at a time), every
// request needs `Authorization: Bearer <token>`, or the cookie /login sets. Behind a proxy that
// signs people in and passes their email in a header (exe.dev's X-ExeDev-Email, an
// oauth2-proxy's X-Forwarded-Email), POPCORN_AUTH_HEADER names that header and a request carrying
// it is signed in, optionally limited to POPCORN_ALLOWED_EMAILS; tokens still work alongside it,
// for API and MCP clients. With neither set it's open, which is only safe because `serve` binds
// to 127.0.0.1 by default.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/metcalfc/popcorn/internal/analysis"
	"github.com/metcalfc/popcorn/internal/app"
	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/probe"
	"github.com/metcalfc/popcorn/internal/report"
	"github.com/metcalfc/popcorn/internal/run"
	"github.com/metcalfc/popcorn/internal/sets"
	"github.com/metcalfc/popcorn/internal/store"
	"github.com/metcalfc/popcorn/internal/version"
	"github.com/yuin/goldmark"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Server struct {
	App    *app.App
	tokens []string
	pages  map[string]*template.Template
	// A header a signing-in proxy sets to the user's email, and the emails or @domains allowed
	// (any, when empty). Only set this when nothing can reach Popcorn except through that proxy.
	authHeader string
	allowed    []string
	// The login cookie's name. Cookies ignore the port, so two deployments on one host need
	// different names or each sign-in signs the other out.
	cookie string
}

func New(a *app.App) (*Server, error) {
	s := &Server{App: a, cookie: "popcorn_token_" + a.Cfg.Brand.Slug(), authHeader: strings.TrimSpace(os.Getenv("POPCORN_AUTH_HEADER"))}
	for _, t := range strings.Split(os.Getenv("POPCORN_TOKENS"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			s.tokens = append(s.tokens, t)
		}
	}
	for _, e := range strings.Split(os.Getenv("POPCORN_ALLOWED_EMAILS"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			s.allowed = append(s.allowed, e)
		}
	}
	funcs := template.FuncMap{
		"icon":  icon,
		"sicon": statusIcon,
		"pct":   func(p float64) string { return fmt.Sprintf("%.0f%%", 100*p) },
		"neg":   func(n int) int { return -n },
		// A commit, abbreviated.
		"short": func(rev string) string {
			if len(rev) > 10 {
				return rev[:10]
			}
			return rev
		},
		// A change in a rate, in percentage points.
		"pts": func(d float64) string {
			if math.Abs(d) < 0.005 {
				return "±0 pts"
			}
			return fmt.Sprintf("%+.0f pts", 100*d)
		},
		// A history cell's shade: none, some or most of its answers named the brand.
		"shade": func(c app.Cell) string {
			switch {
			case !c.Asked:
				return "h-na"
			case c.N == 0 || c.Visible == 0:
				return "h-0"
			case 2*c.Visible < c.N:
				return "h-1"
			case c.Visible < c.N:
				return "h-2"
			}
			return "h-3"
		},
		"rate":  fmtRate,
		"money": func(v float64) string { return fmt.Sprintf("$%.2f", v) },
		"json":  func(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) },
		"bar":   func(p float64) string { return fmt.Sprintf("%.1f", math.Max(1, 100*p)) },
		"deref": func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		},
		"add":   func(a, b int) int { return a + b },
		"day":   func(s string) string { return strings.Replace(strings.TrimSuffix(s, "Z"), "T", " ", 1) },
		"brand": func() string { return a.Cfg.Brand.Name },
		"aname": a.Cfg.AnswererName,
		"asset": asset,
		"trend": trend,
		// The search probe's domain placeholder: the brand's own.
		"domains": func() string { return strings.Join(a.Cfg.Brand.Domains, ", ") },
		"isbrand": func(n string) bool { return strings.EqualFold(n, a.Cfg.Brand.Name) },
		"isfirst": a.Cfg.Brand.FirstParty,
		"first": func(v []string) string {
			if len(v) == 0 {
				return ""
			}
			return v[0]
		},
		// prompt is the text of a 1-based prompt number, as fix lists cite them.
		"prompt": func(set sets.Set, n int) string {
			if n >= 1 && n <= len(set.Prompts) {
				return set.Prompts[n-1].Text
			}
			return fmt.Sprintf("#%d", n)
		},
		"markdown": func(md string) template.HTML {
			var b strings.Builder
			if goldmark.Convert([]byte(md), &b) != nil {
				return template.HTML(template.HTMLEscapeString(md))
			}
			return template.HTML(b.String()) // goldmark escapes raw HTML unless told otherwise
		},
		"fixmd": func(set sets.Set, f []analysis.Fix) string { return analysis.Markdown(set, f) },
	}
	s.pages = map[string]*template.Template{}
	for _, p := range []string{"home", "set", "run", "rankings", "quickwins", "search", "login"} {
		t, err := template.New(p).Funcs(funcs).ParseFS(assets, "templates/layout.html", "templates/"+p+".html")
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", p, err)
		}
		s.pages[p] = t
	}
	return s, nil
}

func fmtRate(r report.Rate) string {
	if r.N == 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f%% (%.0f–%.0f)", 100*r.P, 100*r.Lo, 100*r.Hi)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "static")
	files := http.StripPrefix("/static/", http.FileServerFS(static))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pages link assets as ?v=<hash of the file> (see asset), so a versioned URL never
		// changes content and can be kept; anything else is checked every time.
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("cache-control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("cache-control", "no-cache")
		}
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /api/status", s.apiStatus)
	mux.HandleFunc("GET /api/version", s.apiVersion)
	// For a container's liveness probe: open without a token, and says nothing but that it's up.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /api/live", s.apiLive)
	mux.HandleFunc("GET /api/progress", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.App.Progress(r.Context())
		if err != nil {
			httpErr(w, err, 500)
			return
		}
		if p == nil {
			p = []app.Progress{}
		}
		writeJSON(w, p)
	})
	mux.HandleFunc("GET /sets/{id}", s.setPage)
	mux.HandleFunc("POST /sets/{id}/run", s.startRunForm)
	mux.HandleFunc("GET /runs/{id}", s.runPage)
	mux.HandleFunc("POST /runs/{id}/fixes", s.rebuildFixesForm)
	mux.HandleFunc("POST /runs/{id}/cancel", s.cancelForm)
	mux.HandleFunc("POST /runs/{id}/retry", s.retryForm)
	mux.HandleFunc("GET /runs/{id}/export.csv", s.exportCSV)
	mux.HandleFunc("GET /runs/{id}/export.json", s.exportJSON)
	mux.HandleFunc("GET /rankings", s.rankingsPage)
	mux.HandleFunc("GET /quick-wins", s.quickWinsPage)
	mux.HandleFunc("GET /api/sets", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.App.Sets()) })
	mux.HandleFunc("GET /api/sets/{id}", s.apiSet)
	mux.HandleFunc("GET /api/runs", s.apiRuns)
	mux.HandleFunc("POST /api/runs", s.apiStartRun)
	mux.HandleFunc("GET /api/runs/{id}", s.apiRun)
	mux.HandleFunc("GET /api/runs/{id}/fixes", s.apiFixes)
	mux.HandleFunc("POST /api/runs/{id}/fixes", s.apiRebuildFixes)
	mux.HandleFunc("POST /api/runs/{id}/cancel", s.apiCancel)
	mux.HandleFunc("POST /api/runs/{id}/retry", s.apiRetry)
	mux.HandleFunc("GET /api/quick-wins", s.apiQuickWins)
	mux.HandleFunc("GET /api/rankings", s.apiRankings)
	mux.HandleFunc("GET /search", s.searchPage)
	mux.HandleFunc("POST /search", s.searchForm)
	mux.HandleFunc("POST /api/search-probe", s.apiSearchProbe)
	mux.HandleFunc("GET /api/citations", s.apiCitations)
	mux.Handle("/api/mcp", s.mcpHandler())
	return s.auth(logRequests(mux))
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			slog.Debug("http", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
		}
	})
}

// assetHashes is each static file's content hash, so a deploy that changes a file changes its URL.
var assetHashes = func() map[string]string {
	out := map[string]string{}
	entries, _ := assets.ReadDir("static")
	for _, e := range entries {
		b, _ := assets.ReadFile("static/" + e.Name())
		sum := sha256.Sum256(b)
		out[e.Name()] = hex.EncodeToString(sum[:])[:12]
	}
	return out
}()

// asset is a static file's URL with its version, for cache busting.
func asset(name string) string { return "/static/" + name + "?v=" + assetHashes[name] }

// emailAllowed reports whether a proxy-signed-in email may use Popcorn: anyone when no list is
// set, else an exact address or an @domain on it.
func (s *Server) emailAllowed(email string) bool {
	if len(s.allowed) == 0 {
		return true
	}
	email = strings.ToLower(email)
	for _, a := range s.allowed {
		if email == a || strings.HasPrefix(a, "@") && strings.HasSuffix(email, a) {
			return true
		}
	}
	return false
}

func (s *Server) validToken(t string) bool {
	for _, k := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(k)) == 1 {
			return true
		}
	}
	return false
}

func (s *Server) auth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.tokens) == 0 && s.authHeader == "" || r.URL.Path == "/login" || r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") {
			h.ServeHTTP(w, r)
			return
		}
		if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.validToken(t) {
			h.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(s.cookie); err == nil && s.validToken(c.Value) {
			// Forms are same-origin only: the cookie is SameSite=Strict.
			h.ServeHTTP(w, r)
			return
		}
		if s.authHeader != "" {
			if email := strings.TrimSpace(r.Header.Get(s.authHeader)); email != "" {
				if s.emailAllowed(email) {
					h.ServeHTTP(w, r)
					return
				}
				http.Error(w, email+" isn't allowed to use this Popcorn.", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if len(s.tokens) == 0 {
			http.Error(w, "Sign in through the proxy in front of this Popcorn.", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) { s.render(w, "login", nil) }

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	t := r.FormValue("token")
	if !s.validToken(t) {
		s.render(w, "login", map[string]any{"Error": "That token isn't valid."})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookie, Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 30 * 86400})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	if err := s.pages[page].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("render", "page", page, "err", err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func httpErr(w http.ResponseWriter, err error, code int) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// --- data assembly shared by pages and API ---

type SetCard struct {
	Set      sets.Set
	Running  *app.Progress // the set's run in progress, if any
	Latest   *run.Run
	Baseline *run.Run
	Summary  *report.Summary
	Rank     int
	// The baseline asked different prompts than the set does today (added, removed or reworded).
	Older bool
}

func (s *Server) cards(ctx context.Context, progress []app.Progress) []SetCard {
	var out []SetCard
	for _, st := range s.App.Sets() {
		c := SetCard{Set: st}
		for i := range progress {
			if progress[i].SetID == st.ID {
				c.Running = &progress[i]
			}
		}
		runs, _ := s.App.Runs.List(ctx, st.ID, 50)
		if len(runs) > 0 {
			c.Latest = &runs[0]
		}
		for i := range runs {
			r := &runs[i]
			if r.Status != "done" || r.Profile == "" {
				continue
			}
			res, err := s.App.Runs.Results(ctx, r.ID)
			if err != nil {
				continue
			}
			complete := len(res) > 0
			for _, x := range res {
				if x.Status != "done" || x.Grade == nil {
					complete = false
				}
			}
			if !complete {
				continue
			}
			c.Baseline = r
			c.Older = askedDifferently(st, res)
			sum := s.App.Summary(r, res)
			c.Summary = &sum
			c.Rank = sum.BrandRank()
			break
		}
		out = append(out, c)
	}
	return out
}

// askedDifferently reports whether a run's prompts differ from the set's today.
func askedDifferently(st sets.Set, res []run.Result) bool {
	asked := map[string]string{}
	for _, x := range res {
		asked[x.PromptID] = x.PromptSHA // a prompt not backfilled has no id, so it reads as changed
	}
	if len(asked) != len(st.Prompts) {
		return true
	}
	for _, p := range st.Prompts {
		if asked[p.ID] != p.SHA() {
			return true
		}
	}
	return false
}

type RunData struct {
	Progress  *app.Progress
	Insights  string
	Run       *run.Run
	Active    bool
	Summary   report.Summary
	Results   []run.Result
	Fixes     []analysis.Fix
	FixError  *string
	Answerers []string

	// Each track's rates on their own: search and recall answer different questions.
	Tracks []TrackRates `json:"-"`
	// Every prompt against every answerer and track, one verdict per sample.
	Grid Grid `json:"-"`
	// Share of voice and pages, cut for the page: the brand always listed, and how many more.
	Voice     []report.Voice `json:"-"`
	VoiceMore int            `json:"-"`
	Pages     []report.Page  `json:"-"`
	PagesMore int            `json:"-"`
}

type TrackRates struct {
	Track string
	report.Rates
}

type Grid struct {
	Cols []GridCol
	Rows []GridRow
}

type GridCol struct{ Answerer, Track string }

type GridRow struct {
	N      int // 1-based, as fixes cite it
	Prompt string
	Cells  [][]string // per column, each sample's verdict, or its status if it has none
}

func (s *Server) runData(ctx context.Context, id string) (*RunData, error) {
	r, err := s.App.Runs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	res, err := s.App.Runs.Results(ctx, id)
	if err != nil {
		return nil, err
	}
	fx, fe, _ := s.App.Fixes(ctx, id)
	d := &RunData{Run: r, Active: s.App.Runs.IsActive(id), Summary: s.App.Summary(r, res), Results: res, Fixes: fx, FixError: fe}
	var insights sql.NullString
	s.App.DB.QueryRowContext(ctx, `SELECT insights FROM runs WHERE id=?`, id).Scan(&insights)
	d.Insights = insights.String
	d.Progress = s.App.RunProgress(ctx, id)
	for _, a := range r.Snapshot.Answerers {
		d.Answerers = append(d.Answerers, a.ID)
	}
	for _, t := range []string{"search", "recall"} {
		var slice []run.Result
		for _, x := range res {
			if string(x.Track) == t {
				slice = append(slice, x)
			}
		}
		if len(slice) > 0 {
			d.Tracks = append(d.Tracks, TrackRates{t, s.App.Summary(r, slice).Overall})
		}
	}
	col := map[GridCol]int{}
	for _, sl := range d.Summary.Slices {
		c := GridCol{sl.Answerer, string(sl.Track)}
		col[c] = len(d.Grid.Cols)
		d.Grid.Cols = append(d.Grid.Cols, c)
	}
	row := map[int]int{}
	for _, p := range d.Summary.Prompts {
		row[p.Idx] = len(d.Grid.Rows)
		d.Grid.Rows = append(d.Grid.Rows, GridRow{N: p.Idx + 1, Prompt: p.Prompt, Cells: make([][]string, len(d.Grid.Cols))})
	}
	for _, x := range res {
		i, ok1 := row[x.PromptIdx]
		j, ok2 := col[GridCol{x.Answerer, string(x.Track)}]
		if !ok1 || !ok2 {
			continue
		}
		v := x.Status
		if x.Grade != nil {
			v = string(x.Grade.Verdict)
		}
		d.Grid.Rows[i].Cells[j] = append(d.Grid.Rows[i].Cells[j], v)
	}
	const voiceRows, pageRows = 20, 60
	for i, v := range d.Summary.Voice {
		if i < voiceRows {
			d.Voice = append(d.Voice, v)
		} else if v.IsBrand {
			d.Voice = append(d.Voice, v) // the brand stays on the board however far down it is
		}
	}
	d.VoiceMore = len(d.Summary.Voice) - len(d.Voice)
	d.Pages = d.Summary.Pages
	if len(d.Pages) > pageRows {
		d.Pages, d.PagesMore = d.Pages[:pageRows], len(d.Pages)-pageRows
	}
	return d, nil
}

func (s *Server) spend(ctx context.Context) map[string]any {
	spent, _ := s.App.DB.SpentToday(ctx)
	var unsettled int
	s.App.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM spend_events WHERE state IN ('unknown','reserved') AND substr(created_at,1,10)=?`, time.Now().UTC().Format("2006-01-02")).Scan(&unsettled)
	return map[string]any{"unsettled_attempts": unsettled, "spent_today_usd": math.Round(spent*100) / 100, "daily_budget_usd": s.App.Cfg.Budget.DailyUSD}
}

// --- pages ---

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	qw, _ := s.App.QuickWins(ctx)
	if len(qw) > 5 {
		qw = qw[:5]
	}
	progress, _ := s.App.Progress(ctx)
	// Each set's latest day on each track, from the same series as the rankings, so the two
	// pages always agree.
	rk, _ := s.App.Rankings(ctx, 30, "")
	latest := map[string]*app.Day{}
	for i := range rk {
		if len(rk[i].Days) > 0 {
			latest[rk[i].SetID+"/"+rk[i].Track] = &rk[i].Days[0]
		}
	}
	var rows []HomeRow
	for _, c := range s.cards(ctx, progress) {
		rows = append(rows, HomeRow{SetCard: c, Search: latest[c.Set.ID+"/search"], Recall: latest[c.Set.ID+"/recall"]})
	}
	s.render(w, "home", map[string]any{"Progress": progress, "Rows": rows, "QuickWins": qw, "Spend": s.spend(ctx)})
}

// RunRow is one run in a set's runs table, with its search-track scores when it finished.
type RunRow struct {
	run.Run
	Scored             bool
	Named, Recommended float64
	Rank               int
}

// HomeRow is one set on the dashboard: its card, and its latest day on each track.
type HomeRow struct {
	SetCard
	Search, Recall *app.Day
}

func (s *Server) setPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.App.Set(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	runs, _ := s.App.Runs.List(r.Context(), st.ID, 30)
	// Each finished run's search-track scores, as the dashboard shows them.
	var rows []RunRow
	for _, rn := range runs {
		row := RunRow{Run: rn}
		if rn.Status == "done" {
			if res, err := s.App.Runs.Results(r.Context(), rn.ID); err == nil {
				var search []run.Result
				for _, x := range res {
					if x.Track == "search" {
						search = append(search, x)
					}
				}
				if len(search) > 0 {
					sum := s.App.Summary(&rn, search)
					row.Scored, row.Named, row.Recommended, row.Rank = true, sum.Overall.Visible.P, sum.Overall.Recommended.P, sum.BrandRank()
				}
			}
		}
		rows = append(rows, row)
	}
	track := "search"
	if r.URL.Query().Get("track") == "recall" {
		track = "recall"
	}
	hist, _ := s.App.History(r.Context(), st, track, 30)
	s.render(w, "set", map[string]any{"Set": st, "Runs": rows, "Spend": s.spend(r.Context()), "History": hist})
}

func (s *Server) startRunForm(w http.ResponseWriter, r *http.Request) {
	started, err := s.App.StartRun(r.Context(), r.PathValue("id"), "manual")
	if busy := (*run.BusyError)(nil); errors.As(err, &busy) {
		http.Redirect(w, r, "/runs/"+busy.RunID, http.StatusSeeOther) // take them to the one already going
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/runs/"+started.ID, http.StatusSeeOther)
}

func (s *Server) runPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.runData(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "run", d)
}

func (s *Server) rebuildFixesForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.App.Runs.Get(r.Context(), id)
	if err != nil || run.Status != "done" || s.App.Runs.IsActive(id) {
		http.Error(w, "complete idle run required", http.StatusConflict)
		return
	}
	go s.App.BuildFixes(context.Background(), id)
	http.Redirect(w, r, "/runs/"+id+"#fixes", http.StatusSeeOther)
}

func (s *Server) cancelForm(w http.ResponseWriter, r *http.Request) {
	s.App.Runs.Cancel(r.PathValue("id"))
	http.Redirect(w, r, "/runs/"+r.PathValue("id"), http.StatusSeeOther)
}

func (s *Server) retryForm(w http.ResponseWriter, r *http.Request) {
	s.App.Runs.Start(r.PathValue("id"))
	http.Redirect(w, r, "/runs/"+r.PathValue("id"), http.StatusSeeOther)
}

func (s *Server) rankingsPage(w http.ResponseWriter, r *http.Request) {
	rk, _ := s.App.Rankings(r.Context(), 30, "")
	s.render(w, "rankings", map[string]any{"Rankings": rk})
}

// filterRow is one row of filter links on the quick wins page.
type filterRow struct {
	Name    string
	Options []filterOption
}

type filterOption struct {
	Label string
	N     int
	On    bool
	Href  string
}

func (s *Server) quickWinsPage(w http.ResponseWriter, r *http.Request) {
	all, _ := s.App.QuickWins(r.Context())
	q := r.URL.Query()
	f := app.QuickWinFilter{Site: q.Get("site"), Gap: q.Get("gap"), Set: q.Get("set")}
	// A row per dimension. Each count applies the other rows' choices, so a link never leads to
	// an empty list; "all" clears just that row.
	href := func(param, value string) string {
		v := url.Values{}
		for _, p := range []string{"site", "gap", "set"} {
			if x := q.Get(p); x != "" && p != param {
				v.Set(p, x)
			}
		}
		if value != "" {
			v.Set(param, value)
		}
		if len(v) == 0 {
			return "/quick-wins"
		}
		return "/quick-wins?" + v.Encode()
	}
	row := func(name, param, current string, values, labels []string, with func(string) app.QuickWinFilter) filterRow {
		fr := filterRow{Name: name}
		fr.Options = append(fr.Options, filterOption{"all", len(s.App.FilterQuickWins(all, with(""))), current == "", href(param, "")})
		for i, v := range values {
			if n := len(s.App.FilterQuickWins(all, with(v))); n > 0 || v == current {
				fr.Options = append(fr.Options, filterOption{labels[i], n, v == current, href(param, v)})
			}
		}
		return fr
	}
	var siteV, siteL []string
	for _, st := range s.App.Cfg.Brand.AllSites() {
		siteV, siteL = append(siteV, st.URL), append(siteL, st.URL+" ("+st.Kind+")")
	}
	siteV, siteL = append(siteV, "elsewhere"), append(siteL, "elsewhere")
	var gapV []string
	for _, g := range []analysis.Gap{analysis.GapDocs, analysis.GapPositioning, analysis.GapThirdParty, analysis.GapProduct} {
		gapV = append(gapV, string(g))
	}
	var setV, setL []string
	for _, st := range s.App.Sets() {
		setV, setL = append(setV, st.ID), append(setL, st.Name)
	}
	rows := []filterRow{
		row("Site", "site", f.Site, siteV, siteL, func(v string) app.QuickWinFilter { x := f; x.Site = v; return x }),
		row("Gap", "gap", f.Gap, gapV, gapV, func(v string) app.QuickWinFilter { x := f; x.Gap = v; return x }),
		row("Set", "set", f.Set, setV, setL, func(v string) app.QuickWinFilter { x := f; x.Set = v; return x }),
	}
	s.render(w, "quickwins", map[string]any{"QuickWins": s.App.FilterQuickWins(all, f), "Rows": rows, "Total": len(all)})
}

// --- API ---

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	st := s.spend(r.Context())
	st["brand"] = s.App.Cfg.Brand.Name
	st["sets"] = len(s.App.Sets())
	writeJSON(w, st)
}

func (s *Server) apiLive(w http.ResponseWriter, r *http.Request) {
	l, err := s.App.Live(r.Context())
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	w.Header().Set("cache-control", "no-store")
	writeJSON(w, l)
}

func (s *Server) apiSet(w http.ResponseWriter, r *http.Request) {
	st, ok := s.App.Set(r.PathValue("id"))
	if !ok {
		httpErr(w, errors.New("no such set"), 404)
		return
	}
	writeJSON(w, st)
}

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	runs, err := s.App.Runs.List(r.Context(), r.URL.Query().Get("set"), limit)
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	type row struct {
		run.Run
		Rates report.Rates `json:"rates"`
		Rank  int          `json:"rank"`
	}
	var out []row
	for _, x := range runs {
		res, _ := s.App.Runs.Results(r.Context(), x.ID)
		sum := s.App.Summary(&x, res)
		x.Snapshot.Set.Prompts = nil
		out = append(out, row{Run: x, Rates: sum.Overall, Rank: sum.BrandRank()})
	}
	writeJSON(w, out)
}

func (s *Server) apiStartRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Set string `json:"set"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Set == "" {
		httpErr(w, errors.New(`body must be {"set": "<prompt set id>"}`), 400)
		return
	}
	started, err := s.App.StartRun(r.Context(), body.Set, "api")
	if busy := (*run.BusyError)(nil); errors.As(err, &busy) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "id": busy.RunID})
		return
	}
	if err != nil {
		httpErr(w, err, 400)
		return
	}
	writeJSON(w, map[string]string{"id": started.ID, "status": started.Status})
}

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) {
	d, err := s.runData(r.Context(), r.PathValue("id"))
	if err != nil {
		httpErr(w, err, 404)
		return
	}
	writeJSON(w, d)
}

func (s *Server) apiFixes(w http.ResponseWriter, r *http.Request) {
	fx, fe, err := s.App.Fixes(r.Context(), r.PathValue("id"))
	if err != nil {
		httpErr(w, err, 404)
		return
	}
	writeJSON(w, map[string]any{"fixes": fx, "error": fe})
}

func (s *Server) apiRebuildFixes(w http.ResponseWriter, r *http.Request) {
	run, err := s.App.Runs.Get(r.Context(), r.PathValue("id"))
	if err != nil || run.Status != "done" || s.App.Runs.IsActive(run.ID) {
		httpErr(w, fmt.Errorf("complete idle run required"), 409)
		return
	}
	if err := s.App.BuildFixes(r.Context(), r.PathValue("id")); err != nil {
		httpErr(w, err, 500)
		return
	}
	s.apiFixes(w, r)
}

func (s *Server) apiCancel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"cancelled": s.App.Runs.Cancel(r.PathValue("id"))})
}

func (s *Server) apiVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"version": version.String(), "config_rev": s.App.Cfg.Rev, "schema": store.Schema()})
}

func (s *Server) apiRetry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"started": s.App.Runs.Start(r.PathValue("id"))})
}

func (s *Server) apiQuickWins(w http.ResponseWriter, r *http.Request) {
	qw, err := s.App.QuickWins(r.Context())
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	q := r.URL.Query()
	writeJSON(w, s.App.FilterQuickWins(qw, app.QuickWinFilter{Gap: q.Get("gap"), Set: q.Get("set"), Site: q.Get("site")}))
}

func (s *Server) apiRankings(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("lastDays"))
	if days <= 0 {
		days = 30
	}
	rk, err := s.App.Rankings(r.Context(), days, r.URL.Query().Get("set"))
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	writeJSON(w, rk)
}

// --- search probe and citations ---

func (s *Server) probe(ctx context.Context, in probe.Input) (probe.Result, error) {
	ctx = llm.WithOperation(llm.WithAccounting(ctx, s.App.Runs.Budget), "search-probe")
	return probe.Run(ctx, s.App.Cfg, in)
}

func (s *Server) searchPage(w http.ResponseWriter, r *http.Request) {
	model, _ := probe.Model(s.App.Cfg)
	s.render(w, "search", map[string]any{"Model": model})
}

func (s *Server) searchForm(w http.ResponseWriter, r *http.Request) {
	in := probe.Input{Query: r.FormValue("query")}
	if d := probe.Domains(r.FormValue("domains")); len(d) > 0 {
		if r.FormValue("mode") == "block" {
			in.BlockedDomains = d
		} else {
			in.AllowedDomains = d
		}
	}
	res, err := s.probe(r.Context(), in)
	data := map[string]any{"Form": r.Form, "Result": res}
	if err != nil {
		data["Error"] = err.Error()
	}
	data["Model"], _ = probe.Model(s.App.Cfg)
	s.render(w, "search", data)
}

func (s *Server) apiSearchProbe(w http.ResponseWriter, r *http.Request) {
	var in probe.Input
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&in); err != nil {
		httpErr(w, errors.New(`body must be {"query": "...", "allowed_domains"?: [...], "blocked_domains"?: [...]}`), 400)
		return
	}
	res, err := s.probe(r.Context(), in)
	if err != nil {
		httpErr(w, err, 400)
		return
	}
	writeJSON(w, res)
}

func (s *Server) apiCitations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days, _ := strconv.Atoi(q.Get("lastDays"))
	c, err := s.App.Citations(r.Context(), app.CitationQuery{URLContains: q.Get("urlContains"), LastDays: days, SetID: q.Get("set")})
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	writeJSON(w, c)
}

// --- export ---

func (s *Server) exportJSON(w http.ResponseWriter, r *http.Request) {
	d, err := s.runData(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("content-disposition", `attachment; filename="`+s.App.Cfg.Brand.Slug()+"-"+d.Run.ID+`.json"`)
	writeJSON(w, d)
}

// exportCSV is one row per answer, for a spreadsheet.
func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	d, err := s.runData(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("content-type", "text/csv; charset=utf-8")
	w.Header().Set("content-disposition", `attachment; filename="`+s.App.Cfg.Brand.Slug()+"-"+d.Run.ID+`.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"result_id", "prompt_n", "prompt", "answerer", "model", "track", "status", "verdict", "position", "top_pick", "named", "why_not_top", "objections", "searches", "cited", "cost_usd", "answer", "prompt_id", "config_rev", "app_version"})
	for _, x := range d.Results {
		row := []string{strconv.FormatInt(x.ID, 10), strconv.Itoa(x.PromptIdx + 1), x.Prompt, x.Answerer, x.Model, string(x.Track), x.Status, "", "", "", "", "", "", "", "", fmt.Sprintf("%.4f", x.CostUSD), ""}
		if g := x.Grade; g != nil {
			row[7] = string(g.Verdict)
			if g.Position != nil {
				row[8] = strconv.Itoa(*g.Position)
			}
			if g.TopPick != nil {
				row[9] = *g.TopPick
			}
			row[10] = strings.Join(g.Named, "; ")
			if g.WhyNotTop != nil {
				row[11] = *g.WhyNotTop
			}
			row[12] = strings.Join(g.Objections, "; ")
		}
		if a := x.Answer; a != nil {
			var qs, cs []string
			for _, q := range a.Searches {
				qs = append(qs, q.Query)
			}
			for _, c := range a.Citations {
				cs = append(cs, c.URL)
			}
			row[13], row[14], row[16] = strings.Join(qs, "; "), strings.Join(cs, " "), a.Text
		}
		// New columns go at the end, so scripts that read the export by position keep working.
		row = append(row, x.PromptID, d.Run.ConfigRev, d.Run.AppVersion)
		cw.Write(row)
	}
	cw.Flush()
}
