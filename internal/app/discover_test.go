package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// discoverApp is providersApp with two competitors that have domains, and a fake Exa whose
// findSimilar answers depend on the seed. otterly.ai is similar to both competitors' home pages
// (once through a blog post); peec.ai is similar to the brand's and is on the board (the run's
// grades name "Peec AI"); rankprompt.example is similar to one competitor's; a page on the brand's
// own host comes back and must be dropped.
func discoverApp(t *testing.T) (*App, *[]map[string]any) {
	t.Helper()
	a, _ := providersApp(t)
	a.Cfg.Competitors = []sets.Competitor{
		{Name: "Profound", Aliases: []string{"tryprofound"}, Domains: []string{"tryprofound.com", "docs.tryprofound.com"}},
		{Name: "Scrunch AI", Domains: []string{"https://www.scrunch.ai/"}},
	}
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/findSimilar" {
			t.Errorf("unexpected Exa call %s", r.URL.Path)
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		calls = append(calls, req)
		var results []any
		page := func(title, url string) any { return map[string]any{"title": title, "url": url} }
		switch req["url"] {
		case "https://cheapseats.dev":
			results = []any{page("Peec AI | AI search analytics", "https://www.peec.ai/pricing"), page("Docs", "https://docs.cheapseats.dev/")}
		case "https://tryprofound.com":
			results = []any{page("How we measure AI visibility | Otterly", "https://otterly.ai/blog/how"), page("RankPrompt\u0000 - Home", "https://rankprompt.example/")}
		case "https://scrunch.ai":
			results = []any{page("Otterly – Monitor your AI search visibility", "https://otterly.ai/")}
		default:
			t.Errorf("unexpected seed %v", req["url"])
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results, "costDollars": map[string]any{"total": 0.004}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv(exa.KeyVar, "")
	t.Setenv(config.ExaBaseURLVar, srv.URL)
	return a, &calls
}

// fakeJev answers the discovery's one question by the URL in the state: rankprompt is doubtful,
// everything else is a product in the category.
func fakeJev(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var asked []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req)
		st := req["state"].(map[string]any)
		p := 0.9
		if strings.Contains(st["url"].(string), "rankprompt") {
			p = 0.2
		}
		answers := map[string]any{}
		for q := range req["questions"].(map[string]any) {
			answers[q] = map[string]any{"type": "noul", "noul": p}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": answers, "usage": map[string]any{"input_tokens": 200, "output_tokens": 1}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, srv.URL)
	return srv, &asked
}

func TestDiscoverCompetitorsSuggestsHostsSimilarToTheConfiguredSites(t *testing.T) {
	a, calls := discoverApp(t)
	_, asked := fakeJev(t)
	ctx := context.Background()

	d, err := a.DiscoverCompetitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// One find-similar per configured competitor's first domain, then the brand's first site, each
	// leaving out the seed's own host and every configured host.
	if d.Seeds != 3 || len(*calls) != 3 {
		t.Fatalf("seeds %d, calls %d", d.Seeds, len(*calls))
	}
	var seeds []string
	for _, c := range *calls {
		seeds = append(seeds, c["url"].(string))
		if c["excludeSourceDomain"] != true || c["numResults"] != float64(10) {
			t.Errorf("request %v", c)
		}
		var ex []string
		for _, x := range c["excludeDomains"].([]any) {
			ex = append(ex, x.(string))
		}
		sort.Strings(ex)
		if strings.Join(ex, " ") != "cheapseats.dev docs.tryprofound.com scrunch.ai tryprofound.com" {
			t.Errorf("exclusions %v", ex)
		}
	}
	if strings.Join(seeds, " ") != "https://tryprofound.com https://scrunch.ai https://cheapseats.dev" {
		t.Errorf("seeds %v", seeds)
	}
	// Three hosts: the brand's own docs host was dropped. Each was put to Jev once, with the page,
	// its title and the category in state, and the probability stored.
	if d.Hosts != 3 || d.Asked != 3 || len(*asked) != 3 {
		t.Fatalf("hosts %d asked %d (%d requests)", d.Hosts, d.Asked, len(*asked))
	}
	for _, q := range *asked {
		st := q["state"].(map[string]any)
		if !strings.HasPrefix(st["category"].(string), "An AI visibility tracker.") || st["url"] == "" {
			t.Errorf("state %v", st)
		}
		qs := q["questions"].(map[string]any)
		if len(qs) != 1 || qs["competes"].(map[string]any)["type"] != "noul" {
			t.Errorf("one noul per host: %v", qs)
		}
	}
	if fmt.Sprintf("%.4f", d.CostUSD) == "0.0000" {
		t.Error("cost should be carried")
	}

	ss, err := a.CompetitorSuggestions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The host two competitors led to sorts first; rankprompt (0.2) is hidden at the default 0.7.
	if len(ss) != 2 || ss[0].Host != "otterly.ai" || ss[1].Host != "peec.ai" {
		t.Fatalf("suggestions %+v", ss)
	}
	o := ss[0]
	if len(o.Seeds) != 2 || o.Seeds[0] != "Profound" || o.Seeds[1] != "Scrunch AI" || o.Name != "Otterly" || o.URL != "https://otterly.ai/" || *o.InCategory != 0.9 {
		t.Errorf("otterly: %+v", o)
	}
	if o.Why != "similar to 2 competitors' sites, in the category at 0.90" {
		t.Errorf("why %q", o.Why)
	}
	// Peec AI is on the board of the stored run (one answer named it) and unconfigured.
	p := ss[1]
	if p.OnBoard != 1 || p.Name != "Peec AI" || p.URL != "https://www.peec.ai/pricing" || len(p.Seeds) != 1 {
		t.Errorf("peec: %+v", p)
	}
	if p.Why != "similar to Cheap Seats' site, named in 1 answer, in the category at 0.90" {
		t.Errorf("why %q", p.Why)
	}

	// The TOML is real TOML: it parses into competitors as the config would read them.
	out := SuggestionsTOML(ss)
	var parsed struct{ Competitors []sets.Competitor }
	if _, err := toml.Decode(out, &parsed); err != nil {
		t.Fatalf("%v in:\n%s", err, out)
	}
	if len(parsed.Competitors) != 2 || parsed.Competitors[0].Name != "Otterly" || parsed.Competitors[0].Domains[0] != "otterly.ai" || parsed.Competitors[1].Domains[0] != "peec.ai" {
		t.Errorf("parsed %+v from:\n%s", parsed.Competitors, out)
	}

	// A lower threshold reads the same rows differently, with no request.
	a.Cfg.TypeSafe.MinConfidence = 0.1
	if ss, _ := a.CompetitorSuggestions(ctx); len(ss) != 3 || ss[2].Host != "rankprompt.example" || ss[2].Name != "RankPrompt" {
		t.Errorf("at 0.1: %+v", ss)
	}
	if len(*asked) != 3 {
		t.Error("reading asked Jev")
	}
	a.Cfg.TypeSafe.MinConfidence = 0

	// Dismissing persists across a refresh, and a refresh asks Jev about nothing already judged.
	if err := a.DismissSuggestion(ctx, "peec.ai"); err != nil {
		t.Fatal(err)
	}
	if err := a.DismissSuggestion(ctx, "nowhere.example"); err == nil {
		t.Error("dismissing an unknown host should fail")
	}
	d, err = a.DiscoverCompetitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Seeds != 3 || d.Asked != 0 || len(*asked) != 3 || len(*calls) != 6 {
		t.Errorf("second run: %+v, %d Jev requests", d, len(*asked))
	}
	ss, _ = a.CompetitorSuggestions(ctx)
	if len(ss) != 1 || ss[0].Host != "otterly.ai" {
		t.Errorf("after dismissing peec: %+v", ss)
	}
	var dismissed int
	a.DB.QueryRow(`SELECT dismissed FROM competitor_suggestions WHERE host='peec.ai'`).Scan(&dismissed)
	if dismissed != 1 {
		t.Error("the dismissed row should survive the refresh")
	}
	// Once a host is configured it stops being a suggestion.
	a.Cfg.Competitors = append(a.Cfg.Competitors, sets.Competitor{Name: "Otterly", Domains: []string{"otterly.ai"}})
	if ss, _ := a.CompetitorSuggestions(ctx); len(ss) != 0 {
		t.Errorf("configured: %+v", ss)
	}
	// Both providers settled in the ledger under the discovery's name.
	var n int
	a.DB.QueryRow(`SELECT COUNT(*) FROM spend_events WHERE kind='provider-attempt/discover'`).Scan(&n)
	if n < 9 {
		t.Errorf("ledger events %d", n)
	}
}

func TestDiscoverCompetitorsWithoutJevStoresNoJudgment(t *testing.T) {
	a, calls := discoverApp(t)
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Jev was called with no [typesafe] in the config")
	}))
	defer jev.Close()
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, jev.URL)
	a.Cfg.TypeSafe = nil
	ctx := context.Background()
	d, err := a.DiscoverCompetitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Asked != 0 || len(*calls) != 3 {
		t.Errorf("%+v", d)
	}
	var nulls int
	a.DB.QueryRow(`SELECT COUNT(*) FROM competitor_suggestions WHERE in_category IS NULL`).Scan(&nulls)
	if nulls != 3 {
		t.Errorf("in_category NULL on %d of 3 rows", nulls)
	}
	// With nothing to gate on, every host is listed, the floor being "Exa found it similar".
	ss, _ := a.CompetitorSuggestions(ctx)
	if len(ss) != 3 || ss[0].Host != "otterly.ai" || ss[0].InCategory != nil || ss[2].Why != "similar to Profound's site" {
		t.Errorf("%+v", ss)
	}
	// Without [exa], nothing is called.
	a.Cfg.Exa = nil
	if _, err := a.DiscoverCompetitors(ctx); err == nil || !strings.Contains(err.Error(), "[exa]") {
		t.Errorf("no [exa]: %v", err)
	}
}
