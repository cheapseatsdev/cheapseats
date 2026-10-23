package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// Where the fixture's answers point: two listicle-shaped pages, one docs page, and a page a
// search retrieved and nobody cited.
const (
	seedList    = "https://blog.example.com/best-ai-visibility-tools"
	seedCompare = "https://example.org/tools/acme-vs-profound"
	seedDocs    = "https://docs.example.net/guide/setup"
	retrieved   = "https://retrieved.example.com/list"
)

// prospectsApp is an App over a real database with [exa] configured, and [typesafe] when asked,
// and one finished run: two answers cite the listicle, one the comparison, one the docs page.
func prospectsApp(t *testing.T, withJev bool) (*App, string) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "x")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "prompt-sets"), 0o755)
	jev := ""
	if withJev {
		jev = "[typesafe]\nmodel = \"jev-1.14.0\"\n"
	}
	os.WriteFile(filepath.Join(dir, "cheapseats.toml"), []byte(`[brand]
name = "Cheap Seats"
about = "An AI visibility tracker. It asks assistants buyer questions."
audience = "a software developer"

[[brand.sites]]
url = "cheapseats.dev"
kind = "marketing"

[[answerers]]
id = "claude"
provider = "anthropic"
model = "m"
samples = { search = 1, recall = 1 }

[grader]
model = "g"

[[competitors]]
name = "Profound"
aliases = ["tryprofound"]
domains = ["tryprofound.com"]

[[competitors]]
name = "Peec AI"
domains = ["github.com/peec"]

`+jev+`
[exa]
prospect_results = 3

[pricing.m]
input = 1
output = 1
[pricing.g]
input = 1
output = 1
[pricing."jev-1.14.0"]
input = 0.042
[pricing.exa-search]
search_per_1k = 4
`), 0o644)
	os.WriteFile(filepath.Join(dir, "prompt-sets", "s.toml"), []byte(`id = "s"
name = "S"
tier = "core"
[[prompts]]
id = "p1"
text = "What's the best AI visibility tool?"
[[prompts]]
id = "p2"
text = "How do I measure share of voice?"
`), 0o644)
	cfg, err := config.Load(filepath.Join(dir, "cheapseats.toml"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	set, _ := a.Set("s")
	r, err := a.Runs.Create(context.Background(), set, "test")
	if err != nil {
		t.Fatal(err)
	}
	finish := func(idx int, track string, text string, cited []answer.Link, searched []answer.Link) {
		g := grade.Grade{Verdict: grade.Absent, Named: []string{"Profound"}}
		ans := answer.Answer{Text: text, Citations: cited}
		if searched != nil {
			ans.Searches = []answer.Query{{Query: "q", Completed: true, Results: searched}}
		}
		gj, _ := json.Marshal(g)
		aj, _ := json.Marshal(ans)
		if _, err := db.Exec(`UPDATE results SET status='done', grade=?, answer=?, verdict=?, finished_at=? WHERE run_id=? AND prompt_idx=? AND track=?`, string(gj), string(aj), "absent", store.Now(), r.ID, idx, track); err != nil {
			t.Fatal(err)
		}
	}
	finish(0, "search", "Profound leads.", []answer.Link{{URL: seedList}}, []answer.Link{{URL: seedList, Title: "The best AI visibility tools"}, {URL: retrieved, Title: "A list"}})
	finish(0, "recall", "Profound again.", []answer.Link{{URL: seedList + "/"}}, nil)
	finish(1, "search", "Compare them.", []answer.Link{{URL: seedCompare}, {URL: seedDocs}}, []answer.Link{{URL: seedCompare, Title: "Acme vs Profound"}})
	finish(1, "recall", "Count how often you're named.", nil, nil)
	if _, err := db.Exec(`UPDATE runs SET status='done', finished_at=? WHERE id=?`, store.Now(), r.ID); err != nil {
		t.Fatal(err)
	}
	return a, r.ID
}

// fakeSite serves the prospect pages: one names a competitor and not the brand, one names both,
// one names nobody.
func fakeSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: *\nAllow: /\n")
		case "/top-tools":
			fmt.Fprint(w, `<html><body><h1>Top tools</h1><p>Profound and a few others.</p></body></html>`)
		case "/roundup":
			fmt.Fprint(w, `<html><body><p>Cheap Seats and Profound, side by side.</p></body></html>`)
		case "/compare":
			fmt.Fprint(w, `<html><body><p>Two things compared, neither of them tracked here.</p></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeExaProspects answers findSimilar per seed: three pages for each listicle, among them a page the run
// cited (with a trailing slash), a page a search retrieved, and one page both seeds return.
func fakeExaProspects(t *testing.T, site string, calls *[]map[string]any) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/findSimilar" {
			t.Errorf("unexpected Exa call %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		*calls = append(*calls, req)
		mu.Unlock()
		var results []any
		switch req["url"] {
		case seedList:
			results = []any{
				map[string]any{"title": "Top tools", "url": site + "/top-tools"},
				map[string]any{"title": "Acme vs Profound", "url": seedCompare + "/"},
				map[string]any{"title": "Roundup", "url": site + "/roundup"},
			}
		case seedCompare:
			results = []any{
				map[string]any{"title": "Roundup", "url": site + "/roundup/"},
				map[string]any{"title": "A list", "url": retrieved},
				map[string]any{"title": "Compare", "url": site + "/compare"},
			}
		default:
			t.Errorf("findSimilar for %v: only the listicle and the comparison are seeds", req["url"])
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results, "costDollars": map[string]any{"total": 0.004}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFindProspectsStoresSimilarPagesWithJevAndWhoTheyName(t *testing.T) {
	defer audit.AllowLoopbackForTests()()
	a, id := prospectsApp(t, true)
	site := fakeSite(t)
	var exaCalls []map[string]any
	exaSrv := fakeExaProspects(t, site.URL, &exaCalls)
	var jevCalls []map[string]any
	var mu sync.Mutex
	jevSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		jevCalls = append(jevCalls, req)
		mu.Unlock()
		p := 0.9
		if strings.HasSuffix(req["state"].(map[string]any)["url"].(string), "/compare") {
			p = 0.3
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": map[string]any{"in_category": map[string]any{"type": "noul", "noul": p}}, "usage": map[string]any{"input_tokens": 200, "output_tokens": 0}})
	}))
	defer jevSrv.Close()
	t.Setenv(exa.KeyVar, "k")
	t.Setenv(config.ExaBaseURLVar, exaSrv.URL)
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, jevSrv.URL)

	ctx := context.Background()
	n, err := a.FindProspects(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Two seeds (the docs page is never one), each asked once, from its own domain and never
	// the brand's or a competitor's.
	if len(exaCalls) != 2 {
		t.Fatalf("findSimilar called %d times: %v", len(exaCalls), exaCalls)
	}
	if exaCalls[0]["url"] != seedList || exaCalls[1]["url"] != seedCompare {
		t.Errorf("seeds most cited first: %v, %v", exaCalls[0]["url"], exaCalls[1]["url"])
	}
	for _, c := range exaCalls {
		if c["excludeSourceDomain"] != true || c["numResults"] != 3.0 {
			t.Errorf("excludeSourceDomain and prospect_results: %v", c)
		}
		ex := fmt.Sprint(c["excludeDomains"])
		for _, d := range []string{"cheapseats.dev", "tryprofound.com", "github.com/peec"} {
			if !strings.Contains(ex, d) {
				t.Errorf("exclude %s: %v", d, ex)
			}
		}
	}
	// Six results came back; the cited page, the retrieved page and the duplicate are dropped.
	if n != 3 {
		t.Fatalf("stored %d", n)
	}
	var stored int
	a.DB.QueryRow(`SELECT COUNT(*) FROM prospects WHERE run_id=?`, id).Scan(&stored)
	if stored != 3 {
		t.Errorf("rows %d", stored)
	}
	var cited, dup int
	a.DB.QueryRow(`SELECT COUNT(*) FROM prospects WHERE url LIKE ? OR url LIKE ?`, "%acme-vs-profound%", "%retrieved.example.com%").Scan(&cited)
	a.DB.QueryRow(`SELECT COUNT(*) FROM prospects WHERE url LIKE ?`, "%/roundup%").Scan(&dup)
	if cited != 0 || dup != 1 {
		t.Errorf("cited/retrieved pages stored: %d; roundup rows: %d", cited, dup)
	}
	// Jev saw each page once, with the url, the title and the category, and nothing else.
	if len(jevCalls) != 3 {
		t.Fatalf("jev asked %d times", len(jevCalls))
	}
	st := jevCalls[0]["state"].(map[string]any)
	if len(st) != 3 || st["url"] == "" || st["title"] == "" || !strings.HasPrefix(st["category"].(string), "An AI visibility tracker.") {
		t.Errorf("state %v", st)
	}
	qs := jevCalls[0]["questions"].(map[string]any)
	q := qs["in_category"].(map[string]any)
	if len(qs) != 1 || q["type"] != "noul" || !strings.Contains(q["instructions"].(string), "`category`") {
		t.Errorf("one noul over the category: %v", qs)
	}

	res, _ := a.Runs.Results(ctx, id)
	ps := a.Prospects(ctx, id, res)
	if len(ps) != 2 {
		t.Fatalf("at the default threshold the 0.3 page is out: %+v", ps)
	}
	first, second := ps[0], ps[1]
	if !strings.HasSuffix(first.URL, "/top-tools") || !first.Outreach || first.NamesBrand || fmt.Sprint(first.Competitors) != "[Profound]" {
		t.Errorf("the page naming a competitor and not the brand comes first: %+v", first)
	}
	if !strings.HasSuffix(second.URL, "/roundup") || second.Outreach || !second.NamesBrand || !second.Read {
		t.Errorf("then the page naming both: %+v", second)
	}
	if first.InCategory == nil || *first.InCategory != 0.9 || first.Seed != seedList || first.SeedTitle != "The best AI visibility tools" || first.SeedCited != 2 {
		t.Errorf("provenance: %+v", first)
	}
	if !strings.Contains(first.Why(), "names Profound and not the brand") {
		t.Errorf("why: %s", first.Why())
	}
	// A looser threshold reads the same rows and lists the 0.3 page, last.
	a.Cfg.TypeSafe.MinConfidence = 0.2
	ps = a.Prospects(ctx, id, res)
	if len(ps) != 3 || !strings.HasSuffix(ps[2].URL, "/compare") || ps[2].Read == false || ps[2].NamesBrand || len(ps[2].Competitors) != 0 {
		t.Errorf("at 0.2: %+v", ps)
	}
	// A second pass searches, asks and reads nothing.
	if n, err := a.FindProspects(ctx, id); err != nil || n != 0 || len(exaCalls) != 2 || len(jevCalls) != 3 {
		t.Errorf("second pass stored %d (%v), exa %d, jev %d", n, err, len(exaCalls), len(jevCalls))
	}
	// Both providers settled under the pass's ledger name.
	var exaEvents, jevEvents int
	a.DB.QueryRow(`SELECT COUNT(*) FROM spend_events WHERE kind=? AND model='exa-search' AND state='settled'`, "provider-attempt/prospects/"+id).Scan(&exaEvents)
	a.DB.QueryRow(`SELECT COUNT(*) FROM spend_events WHERE kind=? AND model='jev-1.14.0' AND state='settled'`, "provider-attempt/prospects/"+id).Scan(&jevEvents)
	if exaEvents != 2 || jevEvents != 3 {
		t.Errorf("ledger: exa %d, jev %d", exaEvents, jevEvents)
	}
	var cost float64
	a.DB.QueryRow(`SELECT SUM(cost_usd) FROM prospects WHERE run_id=?`, id).Scan(&cost)
	if cost < 0.008 {
		t.Errorf("the rows carry the searches' cost: %f", cost)
	}
	// The pass is registered under its command.
	if p, ok := PassByCommand("prospect"); !ok || p.Name != "prospects" || p.Order != 50 || p.Provider != config.ExaProvider || !p.On(a.Cfg) {
		t.Errorf("pass %+v %v", p, ok)
	}
}

func TestFindProspectsWithoutTypeSafeListsPagesUnjudged(t *testing.T) {
	defer audit.AllowLoopbackForTests()()
	a, id := prospectsApp(t, false)
	site := fakeSite(t)
	var exaCalls []map[string]any
	exaSrv := fakeExaProspects(t, site.URL, &exaCalls)
	t.Setenv(exa.KeyVar, "k")
	t.Setenv(config.ExaBaseURLVar, exaSrv.URL)
	t.Setenv(typesafe.KeyVar, "k")
	t.Setenv(config.TypeSafeBaseURLVar, "http://127.0.0.1:1") // callable, but there is no [typesafe]: never used

	ctx := context.Background()
	n, err := a.FindProspects(ctx, id)
	if err != nil || n != 3 {
		t.Fatalf("stored %d (%v)", n, err)
	}
	var judged int
	a.DB.QueryRow(`SELECT COUNT(*) FROM prospects WHERE run_id=? AND in_category IS NOT NULL`, id).Scan(&judged)
	if judged != 0 {
		t.Errorf("in_category should be NULL without [typesafe]: %d judged", judged)
	}
	res, _ := a.Runs.Results(ctx, id)
	ps := a.Prospects(ctx, id, res)
	if len(ps) != 3 || ps[0].InCategory != nil || !strings.HasSuffix(ps[0].URL, "/top-tools") || !ps[0].Outreach {
		t.Errorf("all three listed, the outreach page first: %+v", ps)
	}
	if !strings.Contains(ps[0].Why(), "similar to The best AI visibility tools") || strings.Contains(ps[0].Why(), "in category") {
		t.Errorf("why: %s", ps[0].Why())
	}
	// A run with no rows has no prospects.
	if ps := a.Prospects(ctx, "no-such-run", nil); ps != nil {
		t.Errorf("no rows: %+v", ps)
	}
}
