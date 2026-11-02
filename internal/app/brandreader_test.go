package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/config"
)

// The analyst's page reader keeps to the brand's configured sites: a brand URL redirecting to
// another host reads as unavailable, while a page on the brand reads as usual.
func TestBrandPageReaderRefusesARedirectOffTheBrand(t *testing.T) {
	restore := audit.AllowLoopbackForTests()
	defer restore()
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("Another host's page, written by someone other than the brand. ", 5)))
	}))
	defer off.Close()
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/moved":
			http.Redirect(w, r, off.URL+"/page", http.StatusMovedPermanently)
		case "/pricing":
			w.Write([]byte(strings.Repeat("The brand's pricing page as it reads today for small teams. ", 5)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer own.Close()
	read := brandPageReader(config.Brand{Name: "Acme", Sites: []config.Site{{URL: own.URL, Kind: "marketing"}}}, audit.CiteOptions{})
	ctx := context.Background()
	if text, _, err := read(ctx, own.URL+"/moved"); err == nil || text != "" {
		t.Fatalf("an off-brand redirect was read as the brand page: %q %v", text, err)
	}
	if text, _, err := read(ctx, own.URL+"/pricing"); err != nil || !strings.Contains(text, "pricing page") {
		t.Fatalf("a brand page was not read: %q %v", text, err)
	}
}
