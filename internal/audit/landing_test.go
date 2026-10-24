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

// A fix's target page is read as the citation check reads one: the page that carries the claim
// landed, the one that doesn't hasn't (and says what's missing), a page robots.txt keeps us off
// says only that it couldn't be read, and a second read the same day comes from the cache.
func TestCheckLanding(t *testing.T) {
	defer AllowLoopbackForTests()()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: CheapSeats-Audit\nDisallow: /private\n")
		case "/pricing":
			fmt.Fprint(w, `<html><body><h1>Cheap Seats pricing</h1><p>Plans start at $49 a month, with a 30-day trial.</p></body></html>`)
		case "/old-pricing":
			fmt.Fprint(w, `<html><body><h1>Cheap Seats pricing</h1><p>Plans start at $490 a year.</p></body></html>`)
		case "/private/pricing":
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
	brand := []string{"Cheap Seats"}
	terms := ClaimTerms("Say plans start at $49, with a 30-day trial.")
	if fmt.Sprint(terms) != "[$49 30-day]" {
		t.Fatalf("terms: %q", terms)
	}
	now := func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }
	o := CiteOptions{DB: db.DB, Now: now}
	ctx := context.Background()

	if l := CheckLanding(ctx, srv.URL+"/pricing", brand, terms, o); !l.Landed() || l.Day != "2026-10-24" || len(l.Has) != 2 {
		t.Errorf("the page with the claim: %+v", l)
	}
	if l := CheckLanding(ctx, srv.URL+"/old-pricing", brand, terms, o); l.Landed() || !l.NamesBrand || fmt.Sprint(l.Missing) != "[$49 30-day]" {
		t.Errorf("the page without it ($490 isn't $49): %+v", l)
	}
	if l := CheckLanding(ctx, srv.URL+"/private/pricing", brand, terms, o); l.Skipped == "" || l.Landed() || l.NamesBrand || len(l.Has)+len(l.Missing) != 0 {
		t.Errorf("robots.txt: %+v", l)
	}

	before := hits.Load()
	if l := CheckLanding(ctx, srv.URL+"/pricing", brand, terms, o); hits.Load() != before || !l.Landed() {
		t.Errorf("the same day should read the cache: %d more fetches, %+v", hits.Load()-before, l)
	}
	if l, fresh := CachedLanding(ctx, db.DB, srv.URL+"/pricing", "2026-10-24", brand, terms); l == nil || !fresh || !l.Landed() {
		t.Errorf("cached today: %+v %v", l, fresh)
	}
	if l, fresh := CachedLanding(ctx, db.DB, srv.URL+"/pricing", "2026-10-25", brand, terms); l == nil || fresh || l.Day != "2026-10-24" {
		t.Errorf("the next day the reading is yesterday's, and not fresh: %+v %v", l, fresh)
	}
	if l, _ := CachedLanding(ctx, db.DB, srv.URL+"/never", "2026-10-24", brand, terms); l != nil {
		t.Errorf("never read: %+v", l)
	}
}

func TestClaimTerms(t *testing.T) {
	for claim, want := range map[string]string{
		`State "SOC 2 Type II" and 99.9% uptime.`:                     `[soc 2 type ii 99.9%]`,
		`Cheap Seats grades whether assistants recommend your brand.`: `[cheap seats grades assistants recommend brand]`,
		``: `[]`,
	} {
		if got := fmt.Sprint(ClaimTerms(claim)); got != want {
			t.Errorf("ClaimTerms(%q) = %s, want %s", claim, got, want)
		}
	}
}

// A page that starts or stops naming someone is a flip; one that says the same, or that either
// check couldn't read, isn't.
func TestFlips(t *testing.T) {
	page := func(u string, brand bool, comps ...string) CitedPage {
		return CitedPage{Target: Target{URL: u}, NamesBrand: brand, Competitors: append([]string{}, comps...)}
	}
	unread := page("https://c.example/", false)
	unread.Skipped = "it answered 403"
	prev := CitationCheck{RunID: "r-1", Pages: []CitedPage{
		page("https://a.example/", false, "Profound"),
		page("https://b.example/", true, "Gauge"),
		page("https://c.example/", true),
		page("https://d.example/", true, "Gauge", "Profound"),
	}}
	cur := CitationCheck{RunID: "r-2", Pages: []CitedPage{
		page("https://a.example/", true, "Profound"),
		page("https://b.example/", true, "Gauge"),
		unread,
		page("https://d.example/", false, "Profound", "Peec AI"),
		page("https://new.example/", true),
	}}
	got := Flips(prev, cur)
	var lines []string
	for _, f := range got {
		lines = append(lines, f.URL+" "+strings.Join(f.Chips(), "; ")+" since "+f.SinceRun)
	}
	want := []string{
		"https://a.example/ now names you since r-1",
		"https://d.example/ stopped naming you; now names Peec AI; stopped naming Gauge since r-1",
	}
	if fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Errorf("flips:\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
