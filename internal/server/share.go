package server

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/opinion"
	"github.com/cheapseatsdev/cheapseats/internal/report"
)

// Shared reports: /share/<token> is a run's report for someone without a sign-in, open without
// auth but serving only the run its token names, and only a digest of it: headline rates, share
// of voice, the strongest and weakest prompts, themes and the fixes' titles and targets. Never the
// fixes' Claude prompts, the config or snapshot, result ids, or links to anything behind auth;
// answers' text only when the share was made with include_answers.

// shareToken is what may follow /share/: a token as app.CreateShare makes them (base64url). Only
// such a path skips auth.
func shareToken(path string) (string, bool) {
	t, ok := strings.CutPrefix(path, "/share/")
	if !ok || t == "" || len(t) > 64 {
		return "", false
	}
	for _, c := range t {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", false
		}
	}
	return t, true
}

// The host and scheme a request came in on, as the MCP handler passes them to tools (the SDK
// passes headers but not the Host). Set by Cheap Seats, overwriting anything a client sent.
const (
	hostHeader   = "X-CheapSeats-Request-Host"
	schemeHeader = "X-CheapSeats-Request-Scheme"
)

// linkBase is where links to this Cheap Seats point: CHEAPSEATS_PUBLIC_URL when set, else the first of
// CHEAPSEATS_HOSTS over https, else the host and scheme the request came in on.
func (s *Server) linkBase(host, scheme string) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	if len(s.hosts) > 0 {
		return "https://" + s.hosts[0]
	}
	if scheme != "https" {
		scheme = "http"
	}
	return scheme + "://" + host
}

// requestHost is the host a request was sent to: X-Forwarded-Host's first entry when a proxy
// set it (as X-Forwarded-Proto is honoured for the scheme), else the Host header.
func requestHost(r *http.Request) string {
	if f := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); f != "" {
		return f
	}
	return r.Host
}

// linkWarning is what to tell the person making a link that points at a loopback address, which
// nobody else can open: usually a proxy that rewrote the Host.
func linkWarning(base string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	if !loopback(host) {
		return ""
	}
	return "This link points at " + base + ", which only this machine can open. Set CHEAPSEATS_PUBLIC_URL to the address people reach Cheap Seats at."
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return "https"
	}
	return "http"
}

// shareURL is a share's link.
func (s *Server) shareURL(base, token string) string { return base + "/share/" + token }

// shareLimiter refuses a client that has asked for too many unknown tokens lately: a token is 128
// random bits, so guessing is hopeless anyway, but a scan shouldn't cost the server a query each.
// Behind a proxy every client is the proxy, so the limit is shared; links that work aren't counted.
type shareLimiter struct {
	mu     sync.Mutex
	misses map[string][]time.Time
}

const (
	shareMisses = 20
	shareWindow = time.Minute
)

func (l *shareLimiter) allowed(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.misses[ip][:0]
	for _, t := range l.misses[ip] {
		if now.Sub(t) < shareWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(l.misses, ip)
	} else {
		l.misses[ip] = recent
	}
	return len(recent) < shareMisses
}

func (l *shareLimiter) miss(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.misses == nil || len(l.misses) > 10000 {
		l.misses = map[string][]time.Time{}
	}
	l.misses[ip] = append(l.misses[ip], now)
}

func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// ShareReport is what a shared link shows.
type ShareReport struct {
	Brand, Set, Run string
	Finished        string
	Answerers       []string
	Answers         int
	Overall         report.Rates
	// Without the prompts that name the brand: the headline. Branded is how many did.
	Unbranded report.Rates
	Branded   int
	Voice     []report.Voice
	Strongest []SharePrompt
	Weakest   []SharePrompt
	Themes    []opinion.Theme
	Fixes     []ShareFix
	Expires   string
	// The answers' text is in Strongest and Weakest.
	IncludeAnswers bool
}

type SharePrompt struct {
	N           int
	Prompt      string
	Recommended report.Rate
	Named       report.Rate
	TopPicks    []report.Count
	Answers     []ShareAnswer
}

type ShareAnswer struct{ Answerer, Track, Verdict, Text string }

type ShareFix struct{ Title, Target string }

// shareAnswerChars is how much of an answer a share shows.
const shareAnswerChars = 1500

// shareReport cuts a run down to what a share shows.
func (s *Server) shareReport(d *RunData, sh app.Share) ShareReport {
	sum := d.Summary
	brand := d.Run.Snapshot.Brand.Name
	rep := ShareReport{Brand: brand, Set: d.Run.Snapshot.Set.Name, Run: d.Run.Name, Answers: sum.Done, Overall: sum.Overall,
		Unbranded: sum.Unbranded, Branded: sum.BrandedPrompts, Expires: sh.ExpiresAt, IncludeAnswers: sh.IncludeAnswers}
	if d.Run.FinishedAt != nil {
		rep.Finished = *d.Run.FinishedAt
	}
	for _, a := range d.Run.Snapshot.Answerers {
		rep.Answerers = append(rep.Answerers, s.App.Cfg.AnswererName(a.ID))
	}
	for i, v := range sum.Voice {
		if i < 10 || v.IsBrand {
			rep.Voice = append(rep.Voice, v)
		}
	}
	strong, weak := pickPrompts(sum.Prompts, brand)
	for _, p := range strong {
		rep.Strongest = append(rep.Strongest, s.sharePrompt(d, p, sh.IncludeAnswers))
	}
	for _, p := range weak {
		rep.Weakest = append(rep.Weakest, s.sharePrompt(d, p, sh.IncludeAnswers))
	}
	if t := d.Opinion.Themes; t != nil {
		for _, th := range head(t.Themes, 5) {
			th.Quote = "" // a grader's phrase from one answer: left with the answers
			rep.Themes = append(rep.Themes, th)
		}
	}
	for _, f := range head(d.Fixes, 5) {
		rep.Fixes = append(rep.Fixes, ShareFix{f.Title, f.Target})
	}
	return rep
}

func (s *Server) sharePrompt(d *RunData, p report.PromptRow, answers bool) SharePrompt {
	sp := SharePrompt{N: p.Idx + 1, Prompt: p.Prompt, Recommended: p.Rates.Recommended, Named: p.Rates.Visible, TopPicks: head(p.TopPicks, 3)}
	if !answers {
		return sp
	}
	// Two graded answers, search track first: what the assistants say when they look it up.
	var picked []ShareAnswer
	for _, track := range []string{"search", "recall"} {
		for _, x := range d.Results {
			if len(picked) == 2 || x.PromptIdx != p.Idx || string(x.Track) != track || x.Answer == nil || x.Grade == nil {
				continue
			}
			text := x.Answer.Text
			if utf8.RuneCountInString(text) > shareAnswerChars {
				text = string([]rune(text)[:shareAnswerChars]) + "…"
			}
			picked = append(picked, ShareAnswer{s.App.Cfg.AnswererName(x.Answerer), track, string(x.Grade.Verdict), text})
		}
	}
	sp.Answers = picked
	return sp
}

// pickPrompts is a run's two strongest and two weakest prompts, leaving out info prompts (scored
// on named alone) and branded ones (which name the brand themselves). Strongest: the highest
// recommended rate over at least two answers. Weakest: prompts whose answers made a competitor
// the top pick while leaving the brand out of some, lowest named rate first, at most one of them
// a prompt the brand was never named in, so a share shows a near miss as well as a loss.
func pickPrompts(rows []report.PromptRow, brand string) (strong, weak []report.PromptRow) {
	var choice []report.PromptRow
	for _, p := range rows {
		if !p.Info && !p.Branded {
			choice = append(choice, p)
		}
	}
	byRec := append([]report.PromptRow(nil), choice...)
	sort.SliceStable(byRec, func(i, j int) bool {
		a, b := byRec[i].Rates.Recommended, byRec[j].Rates.Recommended
		if a.P != b.P {
			return a.P > b.P
		}
		return a.N > b.N
	})
	used := map[int]bool{}
	for _, p := range byRec {
		if len(strong) < 2 && p.Rates.Recommended.N >= 2 && p.Rates.Recommended.Hits > 0 {
			strong, used[p.Idx] = append(strong, p), true
		}
	}
	rival := func(p report.PromptRow) int {
		n := 0
		for _, c := range p.TopPicks {
			if !strings.EqualFold(c.Name, brand) {
				n += c.N
			}
		}
		return n
	}
	var losing []report.PromptRow
	for _, p := range choice {
		if !used[p.Idx] && rival(p) > 0 && p.Rates.Visible.Hits < p.Rates.Visible.N {
			losing = append(losing, p)
		}
	}
	sort.SliceStable(losing, func(i, j int) bool {
		a, b := losing[i], losing[j]
		if a.Rates.Visible.P != b.Rates.Visible.P {
			return a.Rates.Visible.P < b.Rates.Visible.P
		}
		return rival(a) > rival(b)
	})
	zero := false
	for _, p := range losing {
		if len(weak) == 2 {
			break
		}
		if p.Rates.Visible.Hits == 0 {
			if zero {
				continue
			}
			zero = true
		}
		weak = append(weak, p)
	}
	return strong, weak
}

// --- handlers ---

func (s *Server) sharePage(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	// Looked up first, so a link that works always opens: behind a proxy every visitor shares one
	// address, and someone else's misses mustn't lock it out. Only misses are limited.
	token, _ := shareToken(r.URL.Path)
	sh, err := s.App.OpenShare(r.Context(), token)
	if err != nil {
		ip, now := clientIP(r), time.Now()
		if !s.shareLimit.allowed(ip, now) {
			http.Error(w, "Too many requests for unknown links; try again in a minute.", http.StatusTooManyRequests)
			return
		}
		s.shareLimit.miss(ip, now)
		http.Error(w, "This link doesn't open anything: it may have expired or been revoked.", http.StatusNotFound)
		return
	}
	d, err := s.runData(r.Context(), sh.RunID)
	if err != nil {
		http.Error(w, "This link doesn't open anything: it may have expired or been revoked.", http.StatusNotFound)
		return
	}
	h.Set("content-type", "text/html; charset=utf-8")
	if err := s.share.ExecuteTemplate(w, "share", s.shareReport(d, sh)); err != nil {
		http.Error(w, "rendering failed", 500)
	}
}

// createShareForm makes a share from the run page and shows the run page with its link, the only
// time the link is shown.
func (s *Server) createShareForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sh, token, err := s.App.CreateShare(r.Context(), id, atoi(r.FormValue("days")), r.FormValue("include_answers") != "", s.who(r, "web"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d, err := s.runData(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d.ShareWarning = linkWarning(s.linkBase(requestHost(r), requestScheme(r)))
	d.NewShare = s.newShare(sh, s.linkBase(requestHost(r), requestScheme(r)), token)
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "run", d)
}

func (s *Server) revokeShareForm(w http.ResponseWriter, r *http.Request) {
	sid, _ := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	sh, err := s.App.RevokeShare(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/runs/"+sh.RunID+"#share", http.StatusSeeOther)
}

// NewShare is a share just made, with its link, and a warning when nobody else could open it.
type NewShare struct {
	app.Share
	URL     string `json:"url"`
	Warning string `json:"warning,omitempty"`
}

func (s *Server) newShare(sh app.Share, base, token string) *NewShare {
	return &NewShare{Share: sh, URL: s.shareURL(base, token), Warning: linkWarning(base)}
}

func (s *Server) apiCreateShare(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Days           int  `json:"days"`
		IncludeAnswers bool `json:"include_answers"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		httpErr(w, errors.New(`body must be {"days"?: 30, "include_answers"?: false}`), 400)
		return
	}
	sh, token, err := s.App.CreateShare(r.Context(), r.PathValue("id"), in.Days, in.IncludeAnswers, s.who(r, "api"))
	if err != nil {
		httpErr(w, err, 400)
		return
	}
	writeJSON(w, s.newShare(sh, s.linkBase(requestHost(r), requestScheme(r)), token))
}

func (s *Server) apiShares(w http.ResponseWriter, r *http.Request) {
	sh, err := s.App.Shares(r.Context(), r.PathValue("id"))
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	writeJSON(w, sh)
}

func (s *Server) apiRevokeShare(w http.ResponseWriter, r *http.Request) {
	sid, _ := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	sh, err := s.App.RevokeShare(r.Context(), sid)
	if err != nil {
		httpErr(w, err, 404)
		return
	}
	writeJSON(w, sh)
}
