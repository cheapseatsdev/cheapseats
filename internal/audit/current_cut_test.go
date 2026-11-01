package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// ReadCurrent says when its text may stop short of the page, at every limit on the way: the raw
// response (MaxBody) and the visible text (maxText), from a fresh fetch, from the day cache, and
// through a Reader. A whole page, fresh or cached, is not cut.
func TestReadCurrentReportsCutAtEveryLimit(t *testing.T) {
	restore := AllowLoopbackForTests()
	defer restore()
	para := strings.Repeat("A readable public product description with a concrete starter workflow. ", 5)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/whole":
			w.Write([]byte("<html><body><p>" + para + "</p></body></html>"))
		case "/raw-cut": // short readable text, then script past MaxBody, then the rest
			w.Write([]byte("<html><body><p>" + para + "</p><script>" + strings.Repeat("var x = 1;\n", MaxBody/10) + "</script><p>The rest of the page.</p></body></html>"))
		case "/text-cut":
			w.Write([]byte("<html><body><p>" + strings.Repeat(para, maxText/len(para)+2) + "</p></body></html>"))
		case "/shell":
			w.Write([]byte(`<html><body><div id="root"></div><script src="/app.js"></script></body></html>`))
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
	ctx := context.Background()
	for _, o := range []CiteOptions{{}, {DB: db.DB}, {DB: db.DB}} { // no cache, then fetched into it, then read from it
		for path, want := range map[string]bool{"/whole": false, "/raw-cut": true, "/text-cut": true} {
			text, cut, err := ReadCurrent(ctx, srv.URL+path, o)
			if err != nil || cut != want || !strings.Contains(text, "starter workflow") {
				t.Errorf("%s (db %v): cut=%v, want %v (%v)", path, o.DB != nil, cut, want, err)
			}
		}
	}
	// A row cached before cut was recorded can't say, so it reads as cut.
	day := time.Now().UTC().Format("2006-01-02")
	if _, err := db.Exec(`UPDATE page_fetches SET cut=NULL WHERE url=? AND day=?`, srv.URL+"/whole", day); err != nil {
		t.Fatal(err)
	}
	if _, cut, err := ReadCurrent(ctx, srv.URL+"/whole", CiteOptions{DB: db.DB}); err != nil || !cut {
		t.Errorf("a cached row of unknown completeness read as whole: %v", err)
	}
	// A Reader's text at the text limit (the app's reader cuts there before caching) reads as cut;
	// shorter text doesn't.
	for _, tc := range []struct {
		text string
		want bool
	}{{para, false}, {ClipText(strings.Repeat(para, maxText/len(para)+2)), true}} {
		reader := func(context.Context, string) (string, string, bool) { return tc.text, "", true }
		if _, cut, err := ReadCurrent(ctx, srv.URL+"/shell", CiteOptions{Reader: reader}); err != nil || cut != tc.want {
			t.Errorf("reader text of %d bytes: cut=%v, want %v (%v)", len(tc.text), cut, tc.want, err)
		}
	}
}
