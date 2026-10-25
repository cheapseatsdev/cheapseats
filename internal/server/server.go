// Package server is cheapseats's web UI (server-rendered, no build step) and JSON API.
//
// Auth: when CHEAPSEATS_TOKENS is set (comma-separated, so one can be rotated at a time), every
// request needs `Authorization: Bearer <token>`, or the cookie /login sets. Behind a proxy that
// signs people in and passes their email in a header (exe.dev's X-ExeDev-Email, an
// oauth2-proxy's X-Forwarded-Email), CHEAPSEATS_AUTH_HEADER names that header and a request carrying
// it is signed in, optionally limited to CHEAPSEATS_ALLOWED_EMAILS; tokens still work alongside it,
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
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/env"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/opinion"
	"github.com/cheapseatsdev/cheapseats/internal/probe"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/version"
	"github.com/yuin/goldmark"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Server struct {
	App    *app.App
	tokens []string
	pages  map[string]*template.Template
	// A header a signing-in proxy sets to the user's email, and the emails or @domains allowed
	// (any, when empty). Only set this when nothing can reach Cheap Seats except through that proxy.
	authHeader string
	allowed    []string
	// Host names a proxy on the same machine serves Cheap Seats as (CHEAPSEATS_HOSTS). A request that
	// arrives on loopback under any other non-local name is a DNS-rebinding page's: MCP refuses it,
	// and so does everything else when there's no sign-in to stop the page (see refuseForeign).
	hosts []string
	// The login cookie's name. Cookies ignore the port, so two deployments on one host need
	// different names or each sign-in signs the other out.
	cookie string
	// The name a sign-in before the rename set, honored and carried over to cookie.
	// Where links to this Cheap Seats point (CHEAPSEATS_PUBLIC_URL), for share links; see linkBase.
	publicURL string
	// A read-only copy (CHEAPSEATS_READONLY), such as a public demo: it serves pages and the JSON
	// API, refuses every change and MCP, and hides the controls that would make one.
	readOnly bool
	// The shared-report page, which has its own layout (no navigation to pages behind auth), and
	// the limit on unknown tokens asked for.
	share      *template.Template
	shareLimit shareLimiter
}

func New(a *app.App) (*Server, error) {
	s := &Server{App: a, cookie: "cheapseats_token_" + a.Cfg.Brand.Slug(), authHeader: strings.TrimSpace(env.Get("AUTH_HEADER")),
		publicURL: strings.TrimRight(strings.TrimSpace(env.Get("PUBLIC_URL")), "/"), readOnly: env.Get("READONLY") != ""}
	for _, t := range strings.Split(env.Get("TOKENS"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			s.tokens = append(s.tokens, t)
		}
	}
	for _, h := range strings.Split(env.Get("HOSTS"), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			s.hosts = append(s.hosts, h)
		}
	}
	for _, e := range strings.Split(env.Get("ALLOWED_EMAILS"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			s.allowed = append(s.allowed, e)
		}
	}
	funcs := template.FuncMap{
		"icon":  icon,
		"logo":  logo,
		"sicon": statusIcon,
		"seats": seats,
		"pct":   func(p float64) string { return fmt.Sprintf("%.0f%%", 100*p) },
		"neg":   func(n int) int { return -n },
		// An answer's grade key for the seat meter (see gradeKey), and its words.
		"gradekey": func(g *grade.Grade) string { return gradeKey(g, a.Cfg.Brand.Name) },
		"glabel":   func(k string) string { return strings.ReplaceAll(k, "-", " ") },
		"readonly": func() bool { return s.readOnly },
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
		// A change's 95% margin, in points.
		"margin": func(m float64) string { return fmt.Sprintf("±%.0f pts", 100*m) },
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
		"rate": fmtRate,
		// A rate's proportion alone, or a dash when nothing was counted.
		"prate": func(r report.Rate) string {
			if r.N == 0 {
				return "–"
			}
			return fmt.Sprintf("%.0f%%", 100*r.P)
		},
		// An average place in a list of names, or a dash if never placed.
		"place": func(p float64) string {
			if p == 0 {
				return "–"
			}
			return fmt.Sprintf("%.1f", p)
		},
		"money": func(v float64) string { return fmt.Sprintf("$%.2f", v) },
		"json":  func(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) },
		"bar":   func(p float64) string { return fmt.Sprintf("%.1f", math.Max(1, 100*p)) },
		"deref": func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		},
		// A 0–100 score, or a dash when there isn't one.
		"score": func(p *float64) string {
			if p == nil {
				return "–"
			}
			return fmt.Sprintf("%.0f", *p)
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
		// An answer split around its grade's evidence, to highlight it (see grade.Mark).
		"mark": grade.Mark,
		"evidence": func(x run.Result) string {
			if x.Grade == nil {
				return ""
			}
			return x.Grade.Evidence
		},
		"fixmd": func(set sets.Set, f []analysis.Fix) string { return analysis.Markdown(set, f) },
		"work": func(runID string, rank int, status string, on, earlier []app.Attempt, back string) fixWork {
			return fixWork{runID, rank, status, on, earlier, back}
		},
	}
	s.pages = map[string]*template.Template{}
	for _, p := range []string{"home", "set", "run", "rankings", "quickwins", "search", "login", "searches", "audit", "crawlers", "regrade", "review"} {
		t, err := template.New(p).Funcs(funcs).ParseFS(assets, "templates/layout.html", "templates/"+p+".html")
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", p, err)
		}
		s.pages[p] = t
	}
	t, err := template.New("share").Funcs(funcs).ParseFS(assets, "templates/share.html")
	if err != nil {
		return nil, fmt.Errorf("template share: %w", err)
	}
	s.share = t
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
	mux.HandleFunc("GET /sets/{id}/regrade", s.regradePage)
	mux.HandleFunc("POST /sets/{id}/regrade", s.regradeForm)
	mux.HandleFunc("GET /sets/{id}/review", s.reviewPage)
	mux.HandleFunc("POST /sets/{id}/review", s.reviewSuggestForm)
	mux.HandleFunc("GET /runs/{id}", s.runPage)
	mux.HandleFunc("POST /runs/{id}/fixes", s.rebuildFixesForm)
	mux.HandleFunc("POST /runs/{id}/fixes/{rank}/attempts", s.markFixForm)
	mux.HandleFunc("POST /attempts/{aid}/state", s.attemptStateForm)
	mux.HandleFunc("GET /api/attempts", s.apiAttempts)
	mux.HandleFunc("POST /api/runs/{id}/fixes/{rank}/attempts", s.apiMarkFix)
	mux.HandleFunc("POST /api/attempts/{aid}", s.apiAttemptState)
	mux.HandleFunc("POST /runs/{id}/cancel", s.cancelForm)
	mux.HandleFunc("POST /runs/{id}/retry", s.retryForm)
	mux.HandleFunc("GET /runs/{id}/export.csv", s.exportCSV)
	mux.HandleFunc("GET /runs/{id}/export.json", s.exportJSON)
	mux.HandleFunc("POST /runs/{id}/shares", s.createShareForm)
	mux.HandleFunc("POST /shares/{sid}/revoke", s.revokeShareForm)
	mux.HandleFunc("GET /api/runs/{id}/shares", s.apiShares)
	mux.HandleFunc("POST /api/runs/{id}/shares", s.apiCreateShare)
	mux.HandleFunc("DELETE /api/shares/{sid}", s.apiRevokeShare)
	// Open without auth (see auth): a shared run's report, and nothing else.
	mux.HandleFunc("GET /share/{token}", s.sharePage)
	mux.HandleFunc("GET /rankings", s.rankingsPage)
	mux.HandleFunc("GET /quick-wins", s.quickWinsPage)
	mux.HandleFunc("GET /api/sets", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.App.Sets()) })
	mux.HandleFunc("GET /api/sets/{id}", s.apiSet)
	mux.HandleFunc("GET /api/sets/{id}/citation-stability", s.apiCitationStability)
	mux.HandleFunc("GET /api/sets/{id}/review", s.apiReview)
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
	mux.HandleFunc("GET /runs/{id}/searches", s.searchesPage)
	mux.HandleFunc("GET /sets/{id}/searches", s.setSearchesPage)
	mux.HandleFunc("GET /api/runs/{id}/searches", s.apiSearches)
	mux.HandleFunc("GET /audit", s.auditPage)
	mux.HandleFunc("POST /audit", s.auditForm)
	mux.HandleFunc("POST /audit/citations", s.checkCitationsForm)
	mux.HandleFunc("GET /crawlers", s.crawlersPage)
	mux.HandleFunc("GET /api/audit", s.apiAudit)
	mux.HandleFunc("POST /api/audit", s.apiRunAudit)
	mux.HandleFunc("GET /api/runs/{id}/citation-check", s.apiCitationCheck)
	mux.HandleFunc("POST /api/runs/{id}/citation-check", s.apiRunCitationCheck)
	mux.HandleFunc("GET /api/crawlers", s.apiCrawlers)
	// Competitor discovery (discover.go): read the stored suggestions, dismiss one.
	mux.HandleFunc("GET /api/competitors/suggestions", s.apiCompetitorSuggestions)
	mux.HandleFunc("POST /api/competitors/suggestions/{host}/dismiss", s.apiDismissSuggestion)
	mux.Handle("/api/mcp", s.mcpHandler())
	return s.refuseForeign(s.auth(logRequests(s.refuseChanges(mux))))
}

// refuseForeign refuses what a page on another site can make a browser send here, which the
// sign-in doesn't stop. A request that reaches loopback under a foreign name is DNS rebinding: a
// page whose name now resolves to 127.0.0.1, reading a local Cheap Seats that asks for no sign-in
// (one that does hands the foreign name no cookie or token, and a read-only copy is public anyway).
// A form or fetch from another origin that changes something is cross-site request forgery: with
// no sign-in there's no cookie for SameSite to hold back, and behind a signing-in proxy the proxy's
// own session would sign it in. A proxy on the same machine serves Cheap Seats under the names in
// CHEAPSEATS_HOSTS; MCP's guard against rebinding is the same one (see mcpHandler).
func (s *Server) refuseForeign(h http.Handler) http.Handler {
	open := len(s.tokens) == 0 && s.authHeader == "" && !s.readOnly
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if open && s.rebound(r) {
			http.Error(w, fmt.Sprintf("Forbidden: invalid Host header %q (add it to CHEAPSEATS_HOSTS if a proxy serves Cheap Seats under it)", r.Host), http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && s.crossSite(r) {
			http.Error(w, fmt.Sprintf("Forbidden: a request from another site (%s) can't change anything here (add its host to CHEAPSEATS_HOSTS if a proxy serves Cheap Seats under it)", r.Header.Get("Origin")), http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// rebound reports whether a request reached loopback under a name that is neither local nor a
// listed proxy host: a DNS-rebinding page's.
func (s *Server) rebound(r *http.Request) bool {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	return ok && loopback(local.String()) && !loopback(r.Host) && !slices.Contains(s.hosts, hostOnly(r.Host))
}

// crossSite reports whether a browser sent the request from another origin: a page whose host and
// port aren't the ones the request came to (so another local app's page can't post here either),
// nor a listed proxy host. A browser names its origin in Origin (and the site relation in
// Sec-Fetch-Site); other clients send neither.
func (s *Server) crossSite(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" {
			return true // "null" (a sandboxed page), or not an origin at all
		}
		oh := strings.ToLower(u.Host)
		return oh != strings.ToLower(requestHost(r)) && oh != strings.ToLower(r.Host) && !slices.Contains(s.hosts, hostOnly(oh))
	}
	switch strings.ToLower(r.Header.Get("Sec-Fetch-Site")) {
	case "", "same-origin", "none":
		return false
	}
	return true
}

// refuseChanges, on a read-only copy, answers 403 to anything but reading: every form and API call
// that would start, change or delete something, and MCP, whose tools include those.
func (s *Server) refuseChanges(h http.Handler) http.Handler {
	if !s.readOnly {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.URL.Path == "/api/mcp" || r.URL.Path == "/login" {
			http.Error(w, "This is a read-only copy of Cheap Seats: it can't change anything.", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			path := r.URL.Path
			if strings.HasPrefix(path, "/share/") {
				path = "/share/[redacted]" // share URLs carry bearer tokens, including invalid ones
			}
			slog.Debug("http", "method", r.Method, "path", path, "dur", time.Since(start))
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

// emailAllowed reports whether a proxy-signed-in email may use Cheap Seats: anyone when no list is
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
		// A shared report: its token is the credential, and the page serves only its run.
		if _, ok := shareToken(r.URL.Path); ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
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
				http.Error(w, email+" isn't allowed to use this Cheap Seats.", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if len(s.tokens) == 0 {
			http.Error(w, "Sign in through the proxy in front of this Cheap Seats.", http.StatusUnauthorized)
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
	s.setCookie(w, r, t)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: s.cookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 30 * 86400})
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
			if r.Status != run.Done || r.Profile == "" {
				continue
			}
			res, err := s.App.Runs.Results(ctx, r.ID)
			if err != nil {
				continue
			}
			complete := len(res) > 0
			for _, x := range res {
				if !x.Graded() {
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
	Progress *app.Progress
	Insights string
	// Tone, praise and objections over the answers naming the brand, and the theme pass.
	Opinion   opinion.Report `json:"opinion"`
	Run       *run.Run
	Active    bool
	Summary   report.Summary
	Results   []run.Result
	Fixes     []analysis.Fix
	FixError  *string
	Answerers []string
	// The coverage check's pages per prompt (index), when [exa] has checked this run.
	Coverage map[int]*app.CoverageRow `json:"coverage,omitempty"`
	// The neutral retrieval baseline (pages per prompt and the host table), when [exa] has
	// searched this run's prompts.
	Baseline *app.Baseline `json:"baseline,omitempty"`
	// Pages like the cited listicles that no answer cited (the prospects pass), when [exa] has
	// run it; gated at today's threshold.
	Prospects []app.Prospect `json:"prospects,omitempty"`
	// Jev's second opinion on each grade (by result id) and the run's agreement with the grader,
	// when [typesafe] has read this run (docs/optional-providers.md).
	Opinions      map[int64]*app.Opinion `json:"-"`
	SecondOpinion *app.Agreement         `json:"second_opinion,omitempty"`
	// The fact check: the facts answers naming the brand contradicted, when the run was checked.
	Misstatements *app.FactReport `json:"misstatements,omitempty"`
	// The cited pages whose reading changed since the set's previous citation check: started or
	// stopped naming the brand or a competitor. Present only when both runs were checked.
	CitedChanges []audit.PageFlip `json:"cited_page_changes,omitempty"`
	// What each submitted fix's target page on a brand site said when last read, by "run/rank":
	// evidence beside its done-when, never part of the outcome.
	Landings map[string]*audit.Landing `json:"-"`
	// The work submitted for this run's fixes, and for earlier fixes for the same pages, and when
	// the fix list was built (FixAttempts.Status).
	Attempts app.FixAttempts `json:"-"`
	FixesAt  string          `json:"-"`

	// Each track's rates on their own: search and recall answer different questions.
	Tracks []TrackRates `json:"-"`
	// Every prompt against every answerer and track, one verdict per sample.
	Grid Grid `json:"-"`
	// Share of voice and pages, cut for the page: the brand always listed, and how many more.
	Voice     []report.Voice `json:"-"`
	VoiceMore int            `json:"-"`
	Pages     []report.Page  `json:"-"`
	PagesMore int            `json:"-"`
	// The rest of the summary, cut the same way.
	HeadToHead []report.HeadToHead `json:"-"`
	Objections []report.Objection  `json:"-"`
	Hosts      []report.HostUse    `json:"-"`
	HostsMore  int                 `json:"-"`
	Outreach   []report.Outreach   `json:"-"`
	Uncited    []report.Page       `json:"-"`

	// The source opportunities, cut the same way.
	Opportunities []report.Opportunity `json:"-"`

	// The run's share links, and one just made, whose link is shown this once.
	Shares   []app.Share `json:"-"`
	NewShare *NewShare   `json:"-"`
	// Set when share links would point at a loopback address (see linkWarning).
	ShareWarning string `json:"-"`
}

type TrackRates struct {
	Track string
	report.Rates
	// Without branded prompts, the headline; Branded says whether any prompt was.
	Unbranded report.Rates
	Branded   bool
}

type Grid struct {
	Cols []GridCol
	Rows []GridRow
}

type GridCol struct{ Answerer, Track string }

type GridRow struct {
	N      int // 1-based, as fixes cite it
	Prompt string
	Cells  [][]string // per column, each sample's grade key (gradeKey), or its status if it has none
	Wrong  int        // answers that contradict a listed fact (the fact check)
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
	d := &RunData{Run: r, Active: s.App.Runs.IsActive(id), Summary: s.App.Summary(r, res), Results: res, Fixes: fx, FixError: fe, Attempts: s.App.FixAttempts(ctx)}
	var insights, fixesAt sql.NullString
	s.App.DB.QueryRowContext(ctx, `SELECT insights, fixes_at FROM runs WHERE id=?`, id).Scan(&insights, &fixesAt)
	d.Insights, d.FixesAt = insights.String, fixesAt.String
	d.Progress = s.App.RunProgress(ctx, id)
	d.Opinion = s.App.Opinion(ctx, id, res)
	d.Summary.Trace(&d.Opinion.Sentiment) // the pages behind each praise and objection
	d.Coverage = s.App.Coverage(ctx, id, res)
	d.Baseline = s.App.Baseline(ctx, id, res)
	d.Prospects = s.App.Prospects(ctx, id, res)
	d.Opinions = s.App.Opinions(ctx, id, res)
	d.SecondOpinion = app.Agree(d.Opinions, res)
	d.Misstatements = s.App.Misstatements(ctx, id, res)
	d.CitedChanges = s.App.CitationFlips(ctx, r)
	var submitted []shownFix
	for _, f := range fx {
		submitted = append(submitted, shownFix{id, f, d.Attempts.Status(id, f, d.FixesAt)})
	}
	d.Landings = s.landings(ctx, submitted)
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
			sum := s.App.Summary(r, slice)
			d.Tracks = append(d.Tracks, TrackRates{t, sum.Overall, sum.Unbranded, sum.BrandedPrompts > 0})
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
		if d.Misstatements != nil {
			d.Grid.Rows[len(d.Grid.Rows)-1].Wrong = d.Misstatements.ByPrompt[p.Idx]
		}
	}
	for _, x := range res {
		i, ok1 := row[x.PromptIdx]
		j, ok2 := col[GridCol{x.Answerer, string(x.Track)}]
		if !ok1 || !ok2 {
			continue
		}
		v := string(x.Status)
		if x.Grade != nil {
			v = gradeKey(x.Grade, s.App.Cfg.Brand.Name)
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
	const h2hRows, objectionRows, hostRows, outreachRows, uncitedRows = 10, 10, 30, 15, 20
	d.HeadToHead = head(d.Summary.HeadToHead, h2hRows)
	d.Objections = head(d.Summary.Objections, objectionRows)
	d.Hosts = head(d.Summary.Hosts, hostRows)
	d.HostsMore = len(d.Summary.Hosts) - len(d.Hosts)
	d.Outreach = head(d.Summary.Outreach, outreachRows)
	d.Opportunities = head(d.Summary.Opportunities, 15)
	d.Uncited = head(d.Summary.Uncited, uncitedRows)
	d.Shares, _ = s.App.Shares(ctx, id)
	return d, nil
}

// shownFix is a fix on a page, with its run and where its work stands.
type shownFix struct {
	RunID  string
	Fix    analysis.Fix
	Status string
}

// landings is what each submitted fix's target page said when last read, by "run/rank"; an open
// fix has nothing to have landed.
func (s *Server) landings(ctx context.Context, fixes []shownFix) map[string]*audit.Landing {
	out := map[string]*audit.Landing{}
	for _, x := range fixes {
		if x.Status == app.Open {
			continue
		}
		if l := s.App.Landing(ctx, x.Fix); l != nil {
			out[fmt.Sprintf("%s/%d", x.RunID, x.Fix.Rank)] = l
		}
	}
	return out
}

// head is the first n of xs.
func head[T any](xs []T, n int) []T {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
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
	rk, _ := s.App.Rankings(ctx, 30, 0, "")
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
	// How often Jev agreed with the grader on this run, when [typesafe] has read it.
	Agreement *app.Agreement
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
	agreement, _ := s.App.AgreementHistory(r.Context(), st.ID)
	// Each finished run's search-track scores, as the dashboard shows them.
	var rows []RunRow
	for _, rn := range runs {
		row := RunRow{Run: rn}
		if ag, ok := agreement[rn.ID]; ok {
			row.Agreement = &ag
		}
		if rn.Status == run.Done {
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
	window := app.Window(atoi(r.URL.Query().Get("window")))
	hist, _ := s.App.History(r.Context(), st, track, 30, window)
	stab, _ := s.App.CitationStability(r.Context(), st, track, 30)
	s.render(w, "set", map[string]any{"Set": st, "Runs": rows, "Spend": s.spend(r.Context()), "History": hist,
		"Advice": s.App.SampleAdvice(r.Context(), st), "Windows": windowLinks(r, window), "Stability": stab, "Regrading": s.App.Regrading(st.ID), "Agreement": len(agreement) > 0})
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

// regradeDays reads a re-grade's range of runs, in days: 28 unless given, 0 for all. Anything
// that isn't a number is -1, which the re-grade refuses.
func regradeDays(v string) int {
	if v == "" {
		return 28
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return n
}

// regradePage is a re-grade's confirmation: what it would grade and cost, and the button.
func (s *Server) regradePage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.App.Set(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	days := regradeDays(r.URL.Query().Get("since"))
	var opts []filterOption
	for _, d := range []int{7, 28, 90, 0} {
		label := fmt.Sprintf("the last %d days", d)
		if d == 0 {
			label = "all time"
		}
		q := r.URL.Query()
		q.Set("since", strconv.Itoa(d))
		q.Del("started")
		opts = append(opts, filterOption{Label: label, On: d == days, Href: "?" + q.Encode()})
	}
	data := map[string]any{"Set": st, "Since": days, "Options": opts, "Busy": s.App.Regrading(st.ID), "Started": r.URL.Query().Get("started") != "", "Spend": s.spend(r.Context())}
	if p, err := s.App.PlanRegrade(r.Context(), st.ID, days); err != nil {
		data["Err"] = err.Error()
	} else {
		data["Plan"] = p
	}
	s.render(w, "regrade", data)
}

func (s *Server) regradeForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.App.Set(id); !ok {
		http.NotFound(w, r)
		return
	}
	days := regradeDays(r.FormValue("since"))
	if _, err := s.App.StartRegrade(id, days); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/sets/%s/regrade?since=%d&started=1", id, days), http.StatusSeeOther)
}

func (s *Server) runPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.runData(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d.ShareWarning = linkWarning(s.linkBase(requestHost(r), requestScheme(r)))
	s.render(w, "run", d)
}

func (s *Server) rebuildFixesForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rn, err := s.App.Runs.Get(r.Context(), id)
	if err != nil || rn.Status != run.Done || s.App.Runs.IsActive(id) {
		http.Error(w, "complete idle run required", http.StatusConflict)
		return
	}
	go s.App.BuildFixes(context.Background(), id)
	http.Redirect(w, r, "/runs/"+id+"#fixes", http.StatusSeeOther)
}

// fixWork is a fix's submitted work for the "work" template: where it stands, the attempts on it,
// those on earlier fixes for the same page, and the page to come back to after a form.
type fixWork struct {
	RunID       string
	Rank        int
	Status      string
	On, Earlier []app.Attempt
	Back        string
}

// who names the person marking a fix: the email a signing-in proxy passed, else how they came in.
func (s *Server) who(r *http.Request, via string) string {
	if s.authHeader != "" {
		if e := strings.TrimSpace(r.Header.Get(s.authHeader)); e != "" {
			return e
		}
	}
	return via
}

// back is where a form returns to: a path on this Cheap Seats, never another site.
func back(r *http.Request, fallback string) string {
	if b := r.FormValue("back"); strings.HasPrefix(b, "/") && !strings.HasPrefix(b, "//") && !strings.HasPrefix(b, "/\\") {
		return b
	}
	return fallback
}

func (s *Server) markFixForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rank, _ := strconv.Atoi(r.PathValue("rank"))
	if _, err := s.App.MarkFix(r.Context(), id, rank, r.FormValue("url"), r.FormValue("state"), r.FormValue("note"), s.who(r, "web")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, back(r, fmt.Sprintf("/runs/%s#fix-%d", id, rank)), http.StatusSeeOther)
}

func (s *Server) attemptStateForm(w http.ResponseWriter, r *http.Request) {
	aid, _ := strconv.ParseInt(r.PathValue("aid"), 10, 64)
	if err := s.App.SetAttemptState(r.Context(), aid, r.FormValue("state")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, back(r, "/quick-wins"), http.StatusSeeOther)
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
	window := app.Window(atoi(r.URL.Query().Get("window")))
	metric := trendMetric(r.URL.Query().Get("metric"))
	rk, _ := s.App.Rankings(r.Context(), 30, window, "")
	var metrics []filterOption
	for _, m := range trendMetrics {
		q := r.URL.Query()
		q.Set("metric", m.key)
		metrics = append(metrics, filterOption{Label: m.label, On: m.key == metric.key, Href: "?" + q.Encode()})
	}
	s.render(w, "rankings", map[string]any{"Rankings": rk, "Metric": metric.key, "MetricName": metric.label, "Window": window,
		"Windows": windowLinks(r, window), "Metrics": metrics})
}

// windowLinks is a link to the page with each rolling window, the current one on.
func windowLinks(r *http.Request, current int) []filterOption {
	var out []filterOption
	for _, w := range app.Windows {
		q := r.URL.Query()
		q.Set("window", strconv.Itoa(w))
		out = append(out, filterOption{Label: fmt.Sprintf("%d days", w), On: w == current, Href: "?" + q.Encode()})
	}
	return out
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

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
	f := app.QuickWinFilter{Site: q.Get("site"), Gap: q.Get("gap"), Set: q.Get("set"), Status: q.Get("status"), Priority: q.Get("priority")}
	// A row per dimension. Each count applies the other rows' choices, so a link never leads to
	// an empty list; "all" clears just that row.
	href := func(param, value string) string {
		v := url.Values{}
		for _, p := range []string{"status", "priority", "site", "gap", "set"} {
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
	status := []string{app.Open, string(app.Proposed), string(app.Accepted)}
	priority := []string{"urgent", "high", "medium", "low"}
	rows := []filterRow{
		row("Status", "status", f.Status, status, status, func(v string) app.QuickWinFilter { x := f; x.Status = v; return x }),
		row("Priority", "priority", f.Priority, priority, priority, func(v string) app.QuickWinFilter { x := f; x.Priority = v; return x }),
		row("Site", "site", f.Site, siteV, siteL, func(v string) app.QuickWinFilter { x := f; x.Site = v; return x }),
		row("Gap", "gap", f.Gap, gapV, gapV, func(v string) app.QuickWinFilter { x := f; x.Gap = v; return x }),
		row("Set", "set", f.Set, setV, setL, func(v string) app.QuickWinFilter { x := f; x.Set = v; return x }),
	}
	shown := s.App.FilterQuickWins(all, f)
	var submitted []shownFix
	for _, q := range shown {
		submitted = append(submitted, shownFix{q.RunID, q.Fix, q.Status})
	}
	s.render(w, "quickwins", map[string]any{"QuickWins": shown, "Landings": s.landings(r.Context(), submitted), "Rows": rows, "Total": len(all), "Back": r.URL.RequestURI()})
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

// apiCitationStability is a set's citation stability on a track (search unless ?track=recall)
// over the last lastDays days (30 by default), with every transition in full.
func (s *Server) apiCitationStability(w http.ResponseWriter, r *http.Request) {
	st, ok := s.App.Set(r.PathValue("id"))
	if !ok {
		httpErr(w, errors.New("no such set"), 404)
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("lastDays"))
	if days <= 0 {
		days = 30
	}
	track := "search"
	if r.URL.Query().Get("track") == "recall" {
		track = "recall"
	}
	cs, err := s.App.CitationStability(r.Context(), st, track, days)
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	writeJSON(w, cs)
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
		Set    string `json:"set"`
		DryRun bool   `json:"dry_run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Set == "" {
		httpErr(w, errors.New(`body must be {"set": "<prompt set id>", "dry_run"?: true}`), 400)
		return
	}
	if body.DryRun {
		p, err := s.App.DryRun(r.Context(), body.Set)
		if err != nil {
			httpErr(w, err, 400)
			return
		}
		writeJSON(w, p)
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
	writeJSON(w, map[string]string{"id": started.ID, "status": string(started.Status)})
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
	rn, err := s.App.Runs.Get(r.Context(), r.PathValue("id"))
	if err != nil || rn.Status != run.Done || s.App.Runs.IsActive(rn.ID) {
		httpErr(w, fmt.Errorf("complete idle run required"), 409)
		return
	}
	if err := s.App.BuildFixes(r.Context(), r.PathValue("id")); err != nil {
		httpErr(w, err, 500)
		return
	}
	s.apiFixes(w, r)
}

func (s *Server) apiAttempts(w http.ResponseWriter, r *http.Request) {
	at, err := s.App.Attempts(r.Context(), r.URL.Query().Get("set"))
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	writeJSON(w, at)
}

func (s *Server) apiMarkFix(w http.ResponseWriter, r *http.Request) {
	var in struct{ URL, State, Note string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpErr(w, err, 400)
		return
	}
	rank, _ := strconv.Atoi(r.PathValue("rank"))
	at, err := s.App.MarkFix(r.Context(), r.PathValue("id"), rank, in.URL, in.State, in.Note, s.who(r, "api"))
	if err != nil {
		httpErr(w, err, 400)
		return
	}
	writeJSON(w, at)
}

func (s *Server) apiAttemptState(w http.ResponseWriter, r *http.Request) {
	var in struct{ State string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpErr(w, err, 400)
		return
	}
	aid, _ := strconv.ParseInt(r.PathValue("aid"), 10, 64)
	if err := s.App.SetAttemptState(r.Context(), aid, in.State); err != nil {
		httpErr(w, err, 400)
		return
	}
	writeJSON(w, map[string]any{"id": aid, "state": in.State})
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
	writeJSON(w, s.App.FilterQuickWins(qw, app.QuickWinFilter{Gap: q.Get("gap"), Set: q.Get("set"), Site: q.Get("site"), Status: q.Get("status"), Priority: q.Get("priority")}))
}

func (s *Server) apiRankings(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("lastDays"))
	if days <= 0 {
		days = 30
	}
	rk, err := s.App.Rankings(r.Context(), days, atoi(r.URL.Query().Get("window")), r.URL.Query().Get("set"))
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	writeJSON(w, rk)
}

// --- search probe and citations ---

// probe runs the input's engine: a probe.Result for claude (the default) and exa, a
// probe.Compare for both. The Exa client is the app's, so the call is budgeted like the passes.
func (s *Server) probe(ctx context.Context, in probe.Input) (any, error) {
	ctx = llm.WithOperation(llm.WithAccounting(ctx, s.App.Runs.Budget), "search-probe")
	return probe.Probe(ctx, probe.Deps{Cfg: s.App.Cfg, Exa: s.App.Exa()}, in)
}

func (s *Server) searchPage(w http.ResponseWriter, r *http.Request) {
	model, _ := probe.Model(s.App.Cfg)
	s.render(w, "search", map[string]any{"Model": model, "Exa": s.App.Cfg.Exa != nil})
}

func (s *Server) searchForm(w http.ResponseWriter, r *http.Request) {
	in := probe.Input{Query: r.FormValue("query"), Engine: r.FormValue("engine")}
	if d := probe.Domains(r.FormValue("domains")); len(d) > 0 {
		if r.FormValue("mode") == "block" {
			in.BlockedDomains = d
		} else {
			in.AllowedDomains = d
		}
	}
	out, err := s.probe(r.Context(), in)
	data := map[string]any{"Form": r.Form, "Exa": s.App.Cfg.Exa != nil}
	switch v := out.(type) {
	case probe.Result:
		data["Result"] = v
	case probe.Compare:
		data["Compare"] = v
	}
	if err != nil {
		data["Error"] = err.Error()
	}
	data["Model"], _ = probe.Model(s.App.Cfg)
	s.render(w, "search", data)
}

func (s *Server) apiSearchProbe(w http.ResponseWriter, r *http.Request) {
	var in probe.Input
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&in); err != nil {
		httpErr(w, errors.New(`body must be {"query": "...", "allowed_domains"?: [...], "blocked_domains"?: [...], "engine"?: "claude"|"exa"|"both"}`), 400)
		return
	}
	out, err := s.probe(r.Context(), in)
	if err != nil {
		httpErr(w, err, 400)
		return
	}
	writeJSON(w, out)
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
	cw.Write([]string{"result_id", "prompt_n", "prompt", "answerer", "model", "track", "status", "verdict", "position", "top_pick", "named", "why_not_top", "objections", "searches", "cited", "cost_usd", "answer", "prompt_id", "config_rev", "app_version", "prompt_branded", "evidence"})
	branded := map[int]bool{}
	for _, p := range d.Summary.Prompts {
		branded[p.Idx] = p.Branded
	}
	for _, x := range d.Results {
		row := []string{strconv.FormatInt(x.ID, 10), strconv.Itoa(x.PromptIdx + 1), x.Prompt, x.Answerer, x.Model, string(x.Track), string(x.Status), "", "", "", "", "", "", "", "", fmt.Sprintf("%.4f", x.CostUSD), ""}
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
		ev := ""
		if x.Grade != nil {
			ev = x.Grade.Evidence
		}
		row = append(row, x.PromptID, d.Run.ConfigRev, d.Run.AppVersion, strconv.FormatBool(branded[x.PromptIdx]), ev)
		// Every cell, not only the model's words: the prompt and answerer come from a config repo,
		// and the model name is whatever the provider said it was.
		for i := range row {
			row[i] = cell(row[i])
		}
		cw.Write(row)
	}
	cw.Flush()
}

// cell is a model's or a page's words as a spreadsheet cell. Text that starts like a formula
// (=, +, -, @, or a tab or return) gets a leading apostrophe, which spreadsheets read as "text
// follows" and hide, so an answer that says "=HYPERLINK(...)" stays words when the export is
// opened rather than run. The JSON export has the text as it was.
func cell(s string) string {
	if s != "" && strings.IndexByte("=+-@\t\r", s[0]) >= 0 {
		return "'" + s
	}
	return s
}
