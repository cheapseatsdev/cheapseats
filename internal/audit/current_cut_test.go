package audit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/store"
)

const cutTestPara = "A readable public product description with a concrete starter workflow. "

// cutTestServer serves a whole page, one cut at the raw cap (short readable text, then script
// past MaxBody, then the rest), one whose visible text passes maxText, and an app shell. It
// counts the page requests it answers.
func cutTestServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	para := strings.Repeat(cutTestPara, 5)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/whole":
			hits.Add(1)
			w.Write([]byte("<html><body><p>" + para + "</p></body></html>"))
		case "/raw-cut":
			hits.Add(1)
			w.Write([]byte("<html><body><p>" + para + "</p><script>" + strings.Repeat("var x = 1;\n", MaxBody/10) + "</script><p>The rest of the page.</p></body></html>"))
		case "/text-cut":
			hits.Add(1)
			w.Write([]byte("<html><body><p>" + strings.Repeat(para, maxText/len(para)+2) + "</p></body></html>"))
		case "/short-body": // declares more than it sends, then closes
			hits.Add(1)
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			prefix := "<html><body><p>" + para + "</p>"
			fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: %d\r\n\r\n%s", len(prefix)+5000, prefix)
			buf.Flush()
		case "/shell":
			hits.Add(1)
			w.Write([]byte(`<html><body><div id="root"></div><script src="/app.js"></script></body></html>`))
		case "/shell-words": // a shell with some words: past ReadCurrent's 20-word floor, under emptyText
			hits.Add(1)
			w.Write([]byte(`<html><body><div id="root"><p>` + cutTestPara + cutTestPara + `</p></div><script src="/app.js"></script></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// ReadCurrent says when its text may stop short of the page, at every limit on the way: the raw
// response (MaxBody), a body that ends before its declared length, the visible text (maxText), a
// shell no Reader rendered, and a Reader's text at maxText (the app's reader cuts there before
// caching). A whole page, or a shell a Reader rendered whole, is not cut.
func TestReadCurrentReportsCutAtEveryLimit(t *testing.T) {
	restore := AllowLoopbackForTests()
	defer restore()
	srv, _ := cutTestServer(t)
	ctx := context.Background()
	for path, want := range map[string]bool{"/whole": false, "/raw-cut": true, "/short-body": true, "/text-cut": true, "/shell-words": true} {
		text, cut, err := ReadCurrent(ctx, srv.URL+path, CiteOptions{})
		if err != nil || cut != want || !strings.Contains(text, "starter workflow") {
			t.Errorf("%s: cut=%v, want %v (%v)", path, cut, want, err)
		}
	}
	para := strings.Repeat(cutTestPara, 5)
	for _, tc := range []struct {
		text string
		want bool
	}{{para, false}, {ClipText(strings.Repeat(para, maxText/len(para)+2)), true}} {
		reader := func(context.Context, string) (string, string, bool) { return tc.text, "", true }
		if _, cut, err := ReadCurrent(ctx, srv.URL+"/shell", CiteOptions{Reader: reader}); err != nil || cut != tc.want {
			t.Errorf("reader text of %d bytes: cut=%v, want %v (%v)", len(tc.text), cut, tc.want, err)
		}
	}
	// A Reader that can't render the shell leaves plain HTTP's text, which is not the page.
	failing := func(context.Context, string) (string, string, bool) { return "", "", false }
	if _, cut, err := ReadCurrent(ctx, srv.URL+"/shell-words", CiteOptions{Reader: failing}); err != nil || !cut {
		t.Errorf("an unrendered shell read as whole: cut=%v (%v)", cut, err)
	}
}

// The day cache doesn't record whether a reading was cut, so verification never reads it: with
// today's row cached for each page, ReadCurrent still fetches, returns the fresh text and gets
// the cut from the fetch. Other callers still read the cache.
func TestReadCurrentSkipsTheDayCache(t *testing.T) {
	restore := AllowLoopbackForTests()
	defer restore()
	srv, hits := cutTestServer(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	day := time.Now().UTC().Format("2006-01-02")
	cached := strings.Repeat("Cached text from earlier today, which may or may not be the whole page. ", 3)
	ctx := context.Background()
	for path, want := range map[string]bool{"/whole": false, "/raw-cut": true} {
		u := srv.URL + path
		if _, err := db.Exec(`INSERT INTO page_fetches (url, day, status, error, text, fetched_at) VALUES (?,?,200,'',?,?)`, u, day, cached, time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		before := hits.Load()
		text, cut, err := ReadCurrent(ctx, u, CiteOptions{DB: db.DB})
		if err != nil || hits.Load() != before+1 || strings.Contains(text, "Cached text") || !strings.Contains(text, "starter workflow") || cut != want {
			t.Errorf("%s read the day cache or lost the cut: fetched=%v cut=%v want %v text=%.40q (%v)", path, hits.Load() != before, cut, want, text, err)
		}
	}
	// The citation check still reads today's cached row without fetching.
	before := hits.Load()
	CheckCitations(ctx, []Target{{URL: srv.URL + "/whole"}}, []string{"Acme"}, nil, CiteOptions{DB: db.DB})
	if hits.Load() != before {
		t.Error("the citation check stopped using the day cache")
	}
	// Cached text can't say whether it was cut, so any reader of it gets cut=true.
	robots := &robotsCache{f: NewFetcher(nil, 4), by: map[string]*robotsEntry{}}
	if _, _, text, _, cut := fetchText(ctx, CiteOptions{DB: db.DB}, robots, srv.URL+"/whole", day); !strings.Contains(text, "Cached text") || !cut {
		t.Errorf("cached text read as whole: cut=%v text=%.40q", cut, text)
	}
}
