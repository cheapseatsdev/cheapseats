package audit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

const (
	shellPage     = `<!doctype html><html lang="en"><head><title>App</title></head><body><div id="root"></div><script src="/app.js"></script></body></html>`
	challengePage = `<html><head><title>Just a moment...</title></head><body>checking your browser</body></html>`
	renderedText  = "This site renders its pages in the browser. Its roundup of AI visibility tools puts Profound first and Peec AI second, with a long paragraph on each so the text is well past the threshold that reads as empty."
)

// recorder is a Reader that answers every page with renderedText and remembers what it was asked.
type recorder struct {
	mu    sync.Mutex
	asked []string
}

func (r *recorder) read(ctx context.Context, u string) (string, string, bool) {
	r.mu.Lock()
	r.asked = append(r.asked, u)
	r.mu.Unlock()
	return renderedText, "Rendered", true
}

func (r *recorder) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, u := range r.asked {
		out = append(out, u[strings.LastIndex(u, "/"):])
	}
	sort.Strings(out)
	return out
}

// The audit reads an app shell and a challenge page through the Reader: their text is what it
// read, the empty-page finding becomes an info one that says so, and a page plain HTTP could read
// is never sent to it. Without a Reader, the findings are the errors they always were.
func TestAuditReadsShellsThroughTheReader(t *testing.T) {
	defer AllowLoopbackForTests()()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	srv := site{files: map[string]string{
		"/":            shellPage,
		"/robots.txt":  "User-agent: *\nAllow: /\nSitemap: BASE/sitemap.xml\n",
		"/sitemap.xml": `<urlset><url><loc>BASE/docs</loc><lastmod>2026-10-01</lastmod></url><url><loc>BASE/gate</loc><lastmod>2026-10-01</lastmod></url></urlset>`,
		"/docs":        strings.Replace(goodPage, "BASE/", "BASE/docs", 1),
		"/gate":        challengePage,
	}}.serve(t)
	defer srv.Close()
	rec := &recorder{}
	res := Run(context.Background(), []string{srv.URL}, Options{Reader: rec.read, Now: func() time.Time { return now }})
	if got := rec.paths(); fmt.Sprint(got) != "[/ /gate]" {
		t.Errorf("the reader was asked for %v; want the shell and the challenge, never the page plain HTTP read", got)
	}
	via := 0
	for _, f := range res.Findings {
		switch f.Check {
		case "page.needs-js", "page.challenge":
			if f.Severity != Info || f.Via != ViaExa || !strings.Contains(f.Message, "read through Exa") {
				t.Errorf("read through the reader, the finding is an info one that says so: %+v", f)
			}
			via++
		case "page.thin":
			t.Errorf("no thin-page finding for a page the reader filled in: %+v", f)
		}
	}
	if via != 2 {
		t.Errorf("%d findings carry via; want the shell's and the challenge's", via)
	}
	pages := map[string]Page{}
	for _, p := range res.Sites[0].Pages {
		pages[p.URL[strings.LastIndex(p.URL, "/"):]] = p
	}
	if p := pages["/"]; p.Via != ViaExa || p.TextChars != runes(renderedText) || p.Title != "App" {
		t.Errorf("the shell's text is what was read, its title what the HTML said: %+v", p)
	}
	if p := pages["/gate"]; p.Via != ViaExa || p.TextChars != runes(renderedText) || p.Title != "Rendered" {
		t.Errorf("the challenge page's text and title are what was read: %+v", p)
	}
	if p := pages["/docs"]; p.Via != "" || p.TextChars < emptyText {
		t.Errorf("a page plain HTTP read is untouched: %+v", p)
	}

	// Without a Reader: the errors as they always were.
	plain := Run(context.Background(), []string{srv.URL}, Options{Now: func() time.Time { return now }})
	got := map[string]Severity{}
	for _, f := range plain.Findings {
		got[f.Check] = f.Severity
		if f.Via != "" {
			t.Errorf("nothing was read through anything: %+v", f)
		}
	}
	if got["page.needs-js"] != Error || got["page.challenge"] != Error {
		t.Errorf("without a reader the shell and the challenge are errors: %v", got)
	}
}

// The citation check reads a shell and a challenge page through the Reader and marks the row via;
// the same day's second check fetches nothing and asks the Reader again (its own cache answers);
// and a check with no Reader reads the same cached day as plain HTTP did.
func TestCheckCitationsReadsShellsThroughTheReader(t *testing.T) {
	defer AllowLoopbackForTests()()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/shell":
			fmt.Fprint(w, shellPage)
		case "/plain":
			fmt.Fprint(w, `<html><body><p>Cheap Seats is free.</p></body></html>`) // short, but no script: just a short page
		case "/gate":
			fmt.Fprint(w, challengePage)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	comps := []sets.Competitor{{Name: "Profound"}, {Name: "Peec AI"}}
	brand := []string{"Cheap Seats"}
	targets := []Target{{URL: srv.URL + "/shell", Cited: 3}, {URL: srv.URL + "/plain", Cited: 2}, {URL: srv.URL + "/gate", Cited: 1}}
	rec := &recorder{}
	ctx := context.Background()
	c := CheckCitations(ctx, targets, brand, comps, CiteOptions{DB: db.DB, Reader: rec.read})
	shell, plain, gate := c.Pages[0], c.Pages[1], c.Pages[2]
	if shell.Via != ViaExa || shell.Skipped != "" || shell.NamesBrand || fmt.Sprint(shell.Competitors) != "[Profound Peec AI]" || !shell.Outreach {
		t.Errorf("the shell, read through the reader, names the competitors and not the brand: %+v", shell)
	}
	if plain.Via != "" || !plain.NamesBrand {
		t.Errorf("a short page with no script is read as it is: %+v", plain)
	}
	if gate.Via != ViaExa || gate.Skipped != "" || !gate.Outreach {
		t.Errorf("the challenge page, read through the reader: %+v", gate)
	}
	if got := rec.paths(); fmt.Sprint(got) != "[/gate /shell]" {
		t.Errorf("the reader was asked for %v", got)
	}
	before := hits.Load()
	again := CheckCitations(ctx, targets, brand, comps, CiteOptions{DB: db.DB, Reader: rec.read})
	if hits.Load() != before || fmt.Sprint(again.Pages) != fmt.Sprint(c.Pages) || len(rec.asked) != 4 {
		t.Errorf("the same day: nothing fetched again (%d), the reader asked for the same two (%d), the rows the same", hits.Load()-before, len(rec.asked))
	}
	// No Reader, the same day: what plain HTTP got, as before the fallback existed.
	none := CheckCitations(ctx, targets, brand, comps, CiteOptions{DB: db.DB})
	shell, gate = none.Pages[0], none.Pages[2]
	if shell.Via != "" || shell.Skipped != "" || shell.NamesBrand || len(shell.Competitors) != 0 || shell.Outreach {
		t.Errorf("with no reader the shell is a page naming nobody: %+v", shell)
	}
	if gate.Via != "" || gate.Skipped != challengeReason {
		t.Errorf("with no reader the challenge is skipped: %+v", gate)
	}
	if hits.Load() != before {
		t.Errorf("fetched %d more", hits.Load()-before)
	}
}
