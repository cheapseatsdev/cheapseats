package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
)

const (
	shellHTML    = `<!doctype html><html lang="en"><head><title>Roundup</title></head><body><div id="root"></div><script src="/app.js"></script></body></html>`
	plainHTML    = `<!doctype html><html lang="en"><head><title>Roundup</title></head><body><p>Cheap Seats is the budget pick for tracking AI answers: it runs on your laptop, on the subscription you already pay for, and tells you which pages to write next. That is the whole of it, and the whole point of it.</p></body></html>`
	renderedText = "Profound tops this roundup of AI visibility tools; Peec AI is close behind, and the rest of the field trails both by some distance, the reviewer says, over several paragraphs that only a browser renders."
)

// fakeExa is Exa's contents endpoint: it answers every URL with renderedText, counts its calls
// and remembers the URLs asked. It returns no cost estimate, so the configured price applies.
// text, set before the first call, replaces renderedText.
type fakeExa struct {
	calls atomic.Int32
	mu    sync.Mutex
	asked []string
	text  string
}

func serveFakeExa(t *testing.T) (*fakeExa, *httptest.Server) {
	f := &fakeExa{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/contents" {
			t.Errorf("unexpected Exa call %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["text"] != true {
			t.Errorf("contents asked without text: %v", req)
		}
		f.calls.Add(1)
		var results []any
		for _, u := range req["urls"].([]any) {
			f.mu.Lock()
			f.asked = append(f.asked, u.(string))
			f.mu.Unlock()
			text := renderedText
			if f.text != "" {
				text = f.text
			}
			results = append(results, map[string]any{"url": u, "title": "Rendered roundup", "text": text})
		}
		json.NewEncoder(w).Encode(map[string]any{"requestId": "r", "results": results})
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// fallbackApp is providersApp with the contents fallback priced and Exa pointed at the fake, and
// a fake third-party site with a JS shell at /shell and a normal page at /plain.
func fallbackApp(t *testing.T) (*App, string, *httptest.Server, *fakeExa) {
	t.Helper()
	a, id := providersApp(t)
	a.Cfg.Pricing[exa.ContentsModel] = config.Pricing{PagePer1K: 1}
	t.Cleanup(audit.AllowLoopbackForTests())
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/shell":
			fmt.Fprint(w, shellHTML)
		case "/plain":
			fmt.Fprint(w, plainHTML)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	f, exaSrv := serveFakeExa(t)
	t.Setenv(exa.KeyVar, "k")
	t.Setenv(config.ExaBaseURLVar, exaSrv.URL)
	return a, id, site, f
}

// cite makes the run's first answer cite the URLs, so they are the citation check's targets.
func cite(t *testing.T, a *App, id string, urls ...string) {
	t.Helper()
	ans := answer.Answer{Text: "See the roundups."}
	for _, u := range urls {
		ans.Citations = append(ans.Citations, answer.Link{URL: u})
	}
	js, _ := json.Marshal(ans)
	if _, err := a.DB.Exec(`UPDATE results SET answer=? WHERE run_id=? AND prompt_idx=0 AND track='search'`, string(js), id); err != nil {
		t.Fatal(err)
	}
}

func TestContentsFallbackReadsAShellOnceADayInTheCitationCheck(t *testing.T) {
	a, id, site, f := fallbackApp(t)
	shellURL, plainURL := site.URL+"/shell", site.URL+"/plain"
	cite(t, a, id, shellURL, plainURL)
	ctx := context.Background()
	check := func(o audit.CiteOptions) map[string]audit.CitedPage {
		t.Helper()
		c, err := a.CheckCitations(ctx, id, 10, o)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Pages) != 2 {
			t.Fatalf("pages: %+v", c.Pages)
		}
		out := map[string]audit.CitedPage{}
		for _, p := range c.Pages {
			out[p.URL] = p
		}
		return out
	}
	pages := check(audit.CiteOptions{})
	if p := pages[shellURL]; p.Via != audit.ViaExa || p.Skipped != "" || p.NamesBrand || fmt.Sprint(p.Competitors) != "[Profound]" || !p.Outreach {
		t.Errorf("the shell, read through Exa, names the competitor: %+v", p)
	}
	if p := pages[plainURL]; p.Via != "" || !p.NamesBrand || len(p.Competitors) != 0 {
		t.Errorf("the normal page is read over plain HTTP: %+v", p)
	}
	if f.calls.Load() != 1 || fmt.Sprint(f.asked) != fmt.Sprint([]string{shellURL}) {
		t.Fatalf("Exa was asked %d times for %v; want once, for the shell", f.calls.Load(), f.asked)
	}
	// The same day: the plain-HTTP cache says the shell is a shell, exa_contents has its text.
	pages = check(audit.CiteOptions{})
	if p := pages[shellURL]; p.Via != audit.ViaExa || fmt.Sprint(p.Competitors) != "[Profound]" {
		t.Errorf("second check: %+v", p)
	}
	if f.calls.Load() != 1 {
		t.Errorf("a second check the same day called Exa again: %d calls", f.calls.Load())
	}
	var spent float64
	a.DB.QueryRow(`SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE model=? AND state='settled' AND kind='provider-attempt/contents'`, exa.ContentsModel).Scan(&spent)
	if fmt.Sprintf("%.3f", spent) != "0.001" {
		t.Errorf("one page at $1 per thousand settled under exa-contents: %f", spent)
	}
	var kept int
	var cost float64
	a.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(cost_usd),0) FROM exa_contents`).Scan(&kept, &cost)
	if kept != 1 || fmt.Sprintf("%.3f", cost) != "0.001" {
		t.Errorf("exa_contents: %d rows, cost %f", kept, cost)
	}

	// The switch off: on a later day (so the plain-HTTP cache doesn't answer) the shell reads as
	// it did before the fallback, a page naming nobody, and Exa isn't called.
	off := false
	a.Cfg.Exa.ContentsFallback = &off
	pages = check(audit.CiteOptions{Now: func() time.Time { return time.Now().Add(48 * time.Hour) }})
	if p := pages[shellURL]; p.Via != "" || p.Skipped != "" || p.NamesBrand || len(p.Competitors) != 0 || p.Outreach {
		t.Errorf("switched off, the shell names nobody as today: %+v", p)
	}
	// No price: no call, the same behaviour.
	a.Cfg.Exa.ContentsFallback = nil
	delete(a.Cfg.Pricing, exa.ContentsModel)
	if a.ContentsReader() != nil {
		t.Error("with no [pricing.exa-contents] there is no reader")
	}
	pages = check(audit.CiteOptions{Now: func() time.Time { return time.Now().Add(96 * time.Hour) }})
	if p := pages[shellURL]; p.Via != "" || len(p.Competitors) != 0 {
		t.Errorf("unpriced, the shell names nobody: %+v", p)
	}
	if f.calls.Load() != 1 {
		t.Errorf("switched off or unpriced, Exa was still called: %d calls", f.calls.Load())
	}
}

func TestContentsFallbackKeepsAPageClipped(t *testing.T) {
	a, _, site, f := fallbackApp(t)
	f.text = strings.Repeat("Profound tops this roundup. ", 20_000) // 560,000 bytes
	read := a.ContentsReader()
	text, _, ok := read(context.Background(), site.URL+"/shell")
	if !ok || len(text) > 200_000 || len(text) < 199_000 {
		t.Fatalf("read %d bytes, ok %v; want the page clipped to 200,000", len(text), ok)
	}
	var kept int
	a.DB.QueryRow(`SELECT length(CAST(text AS BLOB)) FROM exa_contents WHERE url=?`, site.URL+"/shell").Scan(&kept)
	if kept != len(text) {
		t.Errorf("exa_contents kept %d bytes; want the clipped %d", kept, len(text))
	}
}

func TestContentsFallbackInTheSiteAudit(t *testing.T) {
	a, _, site, f := fallbackApp(t)
	ctx := context.Background()
	run := func() (audit.Page, map[string]audit.Finding) {
		t.Helper()
		res, err := a.Audit(ctx, []string{site.URL}, audit.Options{Pages: -1})
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]audit.Finding{}
		for _, x := range res.Findings {
			found[x.Check] = x
		}
		if len(res.Sites) != 1 || len(res.Sites[0].Pages) != 1 {
			t.Fatalf("sites %+v", res.Sites)
		}
		return res.Sites[0].Pages[0], found
	}
	page, found := run()
	if page.Via != audit.ViaExa || page.TextChars != utf8.RuneCountInString(renderedText) || page.Title != "Roundup" {
		t.Errorf("the shell's text is what Exa read: %+v", page)
	}
	if x, ok := found["page.needs-js"]; !ok || x.Severity != audit.Info || x.Via != audit.ViaExa {
		t.Errorf("no empty-page error, an info finding that says it was read through Exa: %+v", x)
	}
	if _, ok := found["page.thin"]; ok {
		t.Error("no thin-page finding either")
	}
	if f.calls.Load() != 1 || fmt.Sprint(f.asked) != fmt.Sprint([]string{site.URL + "/"}) {
		t.Fatalf("Exa was asked %d times for %v", f.calls.Load(), f.asked)
	}
	// Again the same day: exa_contents answers.
	page, _ = run()
	if page.Via != audit.ViaExa || f.calls.Load() != 1 {
		t.Errorf("second audit: via %q, %d Exa calls", page.Via, f.calls.Load())
	}
	// Switched off: the error as today.
	off := false
	a.Cfg.Exa.ContentsFallback = &off
	page, found = run()
	if page.Via != "" || found["page.needs-js"].Severity != audit.Error || found["page.needs-js"].Via != "" || f.calls.Load() != 1 {
		t.Errorf("switched off: page %+v, finding %+v, %d Exa calls", page, found["page.needs-js"], f.calls.Load())
	}
}
