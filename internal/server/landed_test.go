package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// Two runs of a set, each with a citation check: the page that started naming the brand is
// chipped on the run page and flagged in /api/runs/{id} and get_run; the page that didn't change
// is in neither.
func TestRunShowsCitedPageFlips(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "fake-landed-provider-key-123456")
	db, _ := store.Open(t.TempDir())
	defer db.Close()
	a := newApp(t, testConfig(t), db)
	set, _ := a.Set("ai-search-visibility")
	ctx := context.Background()
	gj, _ := json.Marshal(grade.Grade{Verdict: grade.Absent, Named: []string{"Profound"}})
	aj, _ := json.Marshal(answer.Answer{Text: "Profound leads.", Citations: []answer.Link{{URL: "https://blog.example.com/best-tools"}}})
	var ids []string
	for i, at := range []string{"2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z"} {
		r, err := a.Runs.Create(ctx, set, "manual")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
		db.Exec(`UPDATE results SET status='done', grade=?, answer=?, verdict='absent', finished_at=? WHERE run_id=? AND prompt_idx=0 AND track='search'`, string(gj), string(aj), store.Now(), r.ID)
		db.Exec(`UPDATE runs SET status='done', created_at=? WHERE id=?`, at, r.ID)
		check := audit.CitationCheck{RunID: r.ID, At: at, Pages: []audit.CitedPage{
			{Target: audit.Target{URL: "https://blog.example.com/best-tools"}, NamesBrand: i == 1, Competitors: []string{"Profound"}},
			{Target: audit.Target{URL: "https://steady.example.org/list"}, Competitors: []string{"Profound"}},
		}}
		if err := audit.SaveCheck(ctx, db.DB, check); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, limit(rec.Body.String()))
		}
		return rec.Body.String()
	}
	// The first run has nothing earlier to compare with.
	if page := get("/runs/" + ids[0]); strings.Contains(page, "Cited pages that changed") {
		t.Error("the first checked run has no flips")
	}
	page := get("/runs/" + ids[1])
	if !strings.Contains(page, `<span class="chip">now names you</span>`) || strings.Contains(page, "steady.example.org") {
		t.Errorf("run page: want the flipped page chipped and the steady one left out")
	}
	var api struct {
		Changes []audit.PageFlip `json:"cited_page_changes"`
	}
	json.Unmarshal([]byte(get("/api/runs/"+ids[1])), &api)
	if len(api.Changes) != 1 || !api.Changes[0].NowNamesBrand || api.Changes[0].URL != "https://blog.example.com/best-tools" || api.Changes[0].SinceRun != ids[0] {
		t.Errorf("api: %+v", api.Changes)
	}
	d, err := s.runData(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := json.Marshal(runDigest(d))
	if !strings.Contains(string(digest), `"cited_page_changes":[{"url":"https://blog.example.com/best-tools","now_names_brand":true`) {
		t.Errorf("get_run: %s", limit(string(digest)))
	}
}

// A submitted fix on a brand site shows what its target page says under "done when", on the run
// page and on quick wins: landed for the page with the claim, could not read it for a page
// robots.txt keeps us off, and nothing for a fix nobody has worked on.
func TestFixShowsWhetherItLanded(t *testing.T) {
	defer audit.AllowLoopbackForTests()()
	t.Setenv("ANTHROPIC_API_KEY", "fake-landed-provider-key-123456")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: *\nDisallow: /private\n")
		case "/pricing":
			fmt.Fprint(w, `<html><body><p>Cheap Seats plans start at $49 a month.</p></body></html>`)
		default:
			t.Errorf("fetched %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	db, _ := store.Open(t.TempDir())
	defer db.Close()
	cfg := testConfig(t)
	// The page's server is one of the brand's sites: quick wins triages each fix's site again.
	cfg.Brand.Sites = append(cfg.Brand.Sites, config.Site{URL: srv.URL, Kind: "marketing"})
	a := newApp(t, cfg, db)
	set, _ := a.Set("ai-search-visibility")
	ctx := context.Background()
	r, err := a.Runs.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	fix := func(rank int, path string) string {
		return fmt.Sprintf(`{"rank":%d,"type":"fix-page","title":"Fix %d","target":"%s%s","claim":"Say plans start at $49.","done_when":"d","evidence":"e","prompts":[1],"effort":"S","displaces":null,"site":"%s"}`, rank, rank, srv.URL, path, srv.URL)
	}
	fixes := "[" + fix(1, "/pricing") + "," + fix(2, "/private/pricing") + "," + fix(3, "/untouched") + "]"
	var stored []analysis.Fix
	if err := json.Unmarshal([]byte(fixes), &stored); err != nil {
		t.Fatal(err)
	}
	storeVerifiedServerActions(t, a, r.ID, stored...)
	fx, _, _ := a.Fixes(ctx, r.ID)
	for _, f := range fx[:2] {
		if _, err := a.MarkFix(ctx, r.ID, f.Rank, "https://github.com/acme/site/pull/1", "proposed", "", "test"); err != nil {
			t.Fatal(err)
		}
		a.ReadLanding(ctx, f) // today's reading, so the page shows it rather than starting one
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/runs/" + r.ID, "/quick-wins"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		page := rec.Body.String()
		if rec.Code != 200 || strings.Count(page, "On the page now:") != 2 || !strings.Contains(page, `<span class="chip">landed</span> names Cheap Seats; has “$49”`) ||
			!strings.Contains(page, "could not read it") {
			t.Errorf("%s: %d, want a landed reading and a could-not-read one", path, rec.Code)
		}
	}
}
