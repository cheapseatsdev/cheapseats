package audit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/metcalfc/popcorn/internal/sets"
	"github.com/metcalfc/popcorn/internal/store"
)

func TestNamesAny(t *testing.T) {
	for _, c := range []struct {
		text  string
		names []string
		want  bool
	}{
		{"Try Popcorn for this.", []string{"Popcorn"}, true},
		{"try POPCORN.zone", []string{"popcorn.zone"}, true},
		{"the whole world", []string{"rl"}, false},
		{"Gauge-style tracking", []string{"Gauge"}, true},
		{"Gauges", []string{"Gauge"}, false},
		{"Gauges, then Gauge", []string{"Gauge"}, true}, // a later whole-word match still counts
		{"Peec AI and Profound", []string{"", "Peec AI"}, true},
		{"", []string{"x"}, false},
	} {
		if got := NamesAny(c.text, c.names); got != c.want {
			t.Errorf("NamesAny(%q, %q) = %v", c.text, c.names, got)
		}
	}
}

func TestCheckCitations(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: Popcorn-Audit\nDisallow: /private\n")
		case "/roundup":
			fmt.Fprint(w, `<html><body><h1>Best AI visibility tools</h1><p>Profound leads; Peec AI is close.</p><script>var x = "Popcorn";</script></body></html>`)
		case "/both":
			fmt.Fprint(w, `<html><body><p>Popcorn is free; Gauge is not.</p></body></html>`)
		case "/plain":
			w.Header().Set("content-type", "text/plain")
			fmt.Fprint(w, "popcorn.zone is a tracker.")
		case "/pdf":
			w.Header().Set("content-type", "application/pdf")
			fmt.Fprint(w, "%PDF")
		case "/private/page":
			t.Error("fetched a page robots.txt disallows")
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
	comps := []sets.Competitor{{Name: "Profound"}, {Name: "Peec AI", Aliases: []string{"Peec"}}, {Name: "Gauge"}}
	targets := []Target{
		{URL: srv.URL + "/roundup", Cited: 5}, {URL: srv.URL + "/both", Cited: 3}, {URL: srv.URL + "/plain", Cited: 2},
		{URL: srv.URL + "/private/page", Cited: 1}, {URL: srv.URL + "/gone", Surfaced: 4}, {URL: srv.URL + "/pdf"},
	}
	brand := []string{"Popcorn", "popcorn.zone"}
	c := CheckCitations(context.Background(), targets, brand, comps, CiteOptions{DB: db.DB})
	type row struct {
		names, outreach bool
		comps           string
		skipped         bool
	}
	want := []row{
		{false, true, "[Profound Peec AI]", false}, // the script's "Popcorn" isn't visible text
		{true, false, "[Gauge]", false},
		{true, false, "[]", false},
		{false, false, "[]", true}, // robots.txt
		{false, false, "[]", true}, // 404
		{false, false, "[]", true}, // not text
	}
	for i, p := range c.Pages {
		got := row{p.NamesBrand, p.Outreach, fmt.Sprint(p.Competitors), p.Skipped != ""}
		if got != want[i] || p.Cited != targets[i].Cited {
			t.Errorf("%s: %+v (%s), want %+v", p.URL, got, p.Skipped, want[i])
		}
	}
	if c.Outreach() != 1 {
		t.Errorf("outreach: %d", c.Outreach())
	}
	// The same day, the pages come from the cache: nothing is fetched again.
	before := hits.Load()
	again := CheckCitations(context.Background(), targets, brand, comps, CiteOptions{DB: db.DB})
	if hits.Load() != before || fmt.Sprint(again.Pages) != fmt.Sprint(c.Pages) {
		t.Errorf("second check fetched %d more, or differs", hits.Load()-before)
	}
	c.RunID = "r-1"
	if err := SaveCheck(context.Background(), db.DB, c); err == nil {
		t.Error("a check for a run that doesn't exist should be refused (foreign key)")
	}
}
