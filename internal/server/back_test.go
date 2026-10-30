package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Exercise the actual form decode -> return helper -> Redirect path. Browser
// URL parsing strips controls and treats backslashes as path separators, after
// Redirect cleans dot segments, so validation must precede both transformations.
func TestBackKeepsFormRedirectsOnThisSite(t *testing.T) {
	for _, value := range []string{
		"https://evil.example/", "//evil.example/", "/\\evil.example/",
		"/a/../\\evil.example/", "/./\\evil.example/",
		"/\t/evil.example/", "/\n/evil.example/", "/\r/evil.example/",
		"/\x00/evil.example/", "/bad%escape", "", "next-steps",
		"/%2e/../\\evil.example/",
		"/#a/../\t/evil.example/", "/#a/../\n/evil.example/", "/#a/../\\evil.example/", "/#\x7f",
	} {
		t.Run(value, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://dashboard.example/attempts/1/state", strings.NewReader(url.Values{"back": {value}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			http.Redirect(w, r, back(r, "/next-steps"), http.StatusSeeOther)
			if got := w.Header().Get("Location"); got != "/next-steps" {
				t.Fatalf("unsafe return %q produced Location %q", value, got)
			}
		})
	}
	for _, value := range []string{
		"/next-steps", "/next-steps?site=docs.example&state=open", "/runs/r-1#fix-2",
		"/quick-wins?site=docs.example", "/%2f%2fevil.example/", "/next-steps?q=https://example.org/",
	} {
		r := httptest.NewRequest("POST", "/attempts/1/state", strings.NewReader(url.Values{"back": {value}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := back(r, "/next-steps"); got != value {
			t.Errorf("legitimate return %q became %q", value, got)
		}
	}
}
