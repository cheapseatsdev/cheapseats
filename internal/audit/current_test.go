package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadCurrentKeepsUnavailableEvidenceUnknown(t *testing.T) {
	restore := AllowLoopbackForTests()
	defer restore()
	page := strings.Repeat("A readable public product description with a concrete starter workflow. ", 5)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.Write([]byte("User-agent: *\nDisallow: /private\n"))
		case "/ok":
			w.Write([]byte(page))
		case "/private":
			t.Error("robots-disallowed page was fetched")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	text, err := ReadCurrent(ctx, srv.URL+"/ok", CiteOptions{})
	if err != nil || !strings.Contains(text, "starter workflow") {
		t.Fatalf("readable evidence: %q %v", text, err)
	}
	for _, path := range []string{"/missing", "/private"} {
		text, err := ReadCurrent(ctx, srv.URL+path, CiteOptions{})
		if err == nil || text != "" {
			t.Fatalf("%s must be unknown, not missing content evidence: %q %v", path, text, err)
		}
	}
}
