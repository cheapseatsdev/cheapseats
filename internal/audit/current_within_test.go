package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A brand page that redirects to another host reads as unavailable when ReadCurrent is limited
// to the brand's sites: the other host's text is never returned under the brand's URL, and the
// other host is never fetched, robots.txt included. A redirect within the brand still works, and
// without Within the same redirect is followed as before.
func TestReadCurrentWithinRefusesARedirectOffTheBrand(t *testing.T) {
	restore := AllowLoopbackForTests()
	defer restore()
	page := strings.Repeat("Words that a competitor wrote on a page of its own, nothing to do with the brand. ", 5)
	var offHits atomic.Int32
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offHits.Add(1)
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nAllow: /\n"))
			return
		}
		w.Write([]byte(page))
	}))
	defer off.Close()
	own := strings.Repeat("The brand's own pricing page, as it reads today for small teams. ", 5)
	var robotsOff atomic.Bool
	brand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			if robotsOff.Load() {
				http.Redirect(w, r, off.URL+"/robots.txt", http.StatusFound)
				return
			}
			http.NotFound(w, r)
		case "/moved":
			http.Redirect(w, r, off.URL+"/page", http.StatusMovedPermanently)
		case "/old-pricing":
			http.Redirect(w, r, "/pricing", http.StatusMovedPermanently)
		case "/pricing":
			w.Write([]byte(own))
		default:
			http.NotFound(w, r)
		}
	}))
	defer brand.Close()
	onBrand := func(u string) bool { return u == brand.URL || strings.HasPrefix(u, brand.URL+"/") }
	ctx := context.Background()

	text, _, err := ReadCurrent(ctx, brand.URL+"/moved", CiteOptions{Within: onBrand})
	if err == nil || text != "" || offHits.Load() != 0 {
		t.Fatalf("an off-brand redirect was followed: %d hits off the brand, text %q, err %v", offHits.Load(), text, err)
	}
	if !strings.Contains(err.Error(), "outside the allowed sites") {
		t.Errorf("the error doesn't say why: %v", err)
	}
	if text, _, err := ReadCurrent(ctx, brand.URL+"/old-pricing", CiteOptions{Within: onBrand}); err != nil || !strings.Contains(text, "own pricing page") {
		t.Fatalf("a redirect within the brand was refused: %q %v", text, err)
	}
	if text, _, err := ReadCurrent(ctx, off.URL+"/page", CiteOptions{Within: onBrand}); err == nil || text != "" || offHits.Load() != 0 {
		t.Fatalf("an off-brand page was read: %q %v", text, err)
	}

	// robots.txt redirecting off the brand: that hop is refused too, so the other host is never
	// fetched; the page itself, on the brand, is read as with no robots.txt.
	robotsOff.Store(true)
	if text, _, err := ReadCurrent(ctx, brand.URL+"/pricing", CiteOptions{Within: onBrand}); err != nil || !strings.Contains(text, "own pricing page") || offHits.Load() != 0 {
		t.Fatalf("robots.txt redirect: %d hits off the brand, text %q, err %v", offHits.Load(), text, err)
	}
	robotsOff.Store(false)

	// Without Within (the review's reads, the citation check), the redirect is followed as before.
	if text, _, err := ReadCurrent(ctx, brand.URL+"/moved", CiteOptions{}); err != nil || !strings.Contains(text, "competitor wrote") {
		t.Fatalf("the unrestricted read changed: %q %v", text, err)
	}
}
