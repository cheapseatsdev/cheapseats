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

// A page that redirects to a Location Go can't parse: Go's error quotes the Location whole,
// token and all. The fetch error says what kind of failure it was and names the page without
// its query, so the token is in neither the day cache's error nor the verification read's.
func TestARedirectsTokenStaysOutOfFetchErrors(t *testing.T) {
	restore := AllowLoopbackForTests()
	defer restore()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Location", "https://login.example/%zz?access_token=opaqueSecret")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	u := srv.URL + "/pricing?ref=a"
	robots := &robotsCache{f: NewFetcher(nil, 4), by: map[string]*robotsEntry{}}
	fetchText(ctx, CiteOptions{DB: db.DB, Fetcher: robots.f}, robots, u, time.Now().UTC().Format("2006-01-02"))
	var stored string
	if err := db.QueryRow(`SELECT error FROM page_fetches WHERE url=?`, u).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	_, _, rerr := ReadCurrent(ctx, u, CiteOptions{})
	if rerr == nil {
		t.Fatal("want a read error")
	}
	for where, s := range map[string]string{"page_fetches.error": stored, "verification read": rerr.Error()} {
		if strings.Contains(s, "opaqueSecret") || strings.Contains(s, "access_token") || strings.Contains(s, "ref=a") {
			t.Errorf("%s carries the token: %s", where, s)
		}
		if !strings.Contains(s, "network error ("+srv.URL+"/pricing)") {
			t.Errorf("%s doesn't say what failed: %s", where, s)
		}
	}
}
