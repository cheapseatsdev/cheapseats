package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// No page can be framed (clickjacking the Run, Cancel and share buttons) or load an image from
// elsewhere, whichever route served it: pages, the API, an error, a shared report.
func TestPagesRefuseFramesAndOutsideImages(t *testing.T) {
	t.Setenv("CHEAPSEATS_TOKENS", "")
	t.Setenv("CHEAPSEATS_AUTH_HEADER", "")
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	s, err := New(newApp(t, testConfig(t), d))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for _, path := range []string{"/", "/search", "/api/sets", "/runs/missing", "/share/nope"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		csp := rec.Header().Get("Content-Security-Policy")
		for _, want := range []string{"frame-ancestors 'none'", "img-src 'self'", "object-src 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", path, csp, want)
			}
		}
		if rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: X-Frame-Options %q", path, rec.Header().Get("X-Frame-Options"))
		}
	}
}

// The analyst's Markdown keeps its formatting and links but loads no image: an image becomes its
// alt text.
func TestAnalystMarkdownDropsImages(t *testing.T) {
	out := string(analystMarkdown("Read **this** ![the chart](https://evil.example/x.png?d=secret) and [the guide](https://example.com/g). <img src=https://evil.example/y.png>"))
	if strings.Contains(out, "<img") || strings.Contains(out, "evil.example") {
		t.Errorf("an image survived: %s", out)
	}
	for _, want := range []string{"<strong>this</strong>", "the chart", `<a href="https://example.com/g">the guide</a>`} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q: %s", want, out)
		}
	}
}
