package exa

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

func TestSearchRestrictsToDomainsAndTakesExasCost(t *testing.T) {
	var got map[string]any
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("x-api-key")
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{
			"requestId":   "r1",
			"results":     []any{map[string]any{"title": "Guide", "url": "https://example.com/docs/guide", "publishedDate": "2026-10-01"}},
			"costDollars": map[string]any{"total": 0.005},
		})
	}))
	defer srv.Close()
	t.Setenv(KeyVar, "k")
	cfg := &config.Config{Pricing: map[string]config.Pricing{Model: {SearchPer1K: 4}}}
	c := &Client{URL: srv.URL, Cfg: cfg}
	resp, err := c.Search(context.Background(), "t", "how do I measure x", []string{"example.com", "docs.example.com/guides"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if key != "k" {
		t.Errorf("key header %q", key)
	}
	if got["query"] != "how do I measure x" || got["numResults"].(float64) != 3 || got["type"] != "auto" {
		t.Errorf("request %v", got)
	}
	if d := got["includeDomains"].([]any); len(d) != 2 || d[1] != "docs.example.com/guides" {
		t.Errorf("domains %v", d)
	}
	if _, ok := got["contents"]; ok {
		t.Error("contents cost extra and weren't asked for")
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://example.com/docs/guide" || resp.Results[0].Title != "Guide" {
		t.Errorf("results %+v", resp.Results)
	}
	if resp.CostUSD != 0.005 {
		t.Errorf("Exa's own estimate should be the cost: %f", resp.CostUSD)
	}
}

func TestSearchFallsBackToTheConfiguredPriceAndProxy(t *testing.T) {
	var key, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, path = r.Header.Get("x-api-key"), r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "costDollars": map[string]any{"total": 0}})
	}))
	defer srv.Close()
	t.Setenv(KeyVar, "")
	t.Setenv(config.ExaBaseURLVar, "")
	cfg := &config.Config{Pricing: map[string]config.Pricing{Model: {SearchPer1K: 4}}}
	c := &Client{Cfg: cfg}
	if _, err := c.Search(context.Background(), "t", "q", nil, 3); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key and no proxy: %v", err)
	}
	t.Setenv(config.ExaBaseURLVar, srv.URL)
	resp, err := c.Search(context.Background(), "t", "q", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/search" || key != "" {
		t.Errorf("proxy got path %q key %q", path, key)
	}
	if resp.CostUSD != 0.004 {
		t.Errorf("with no estimate from Exa, one search at $4 per 1,000: %f", resp.CostUSD)
	}
	if m := config.Missing(config.ExaProvider); m != "" {
		t.Errorf("a proxy is enough: %s", m)
	}
}

func TestFindSimilarAndContents(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, got)
		switch r.URL.Path {
		case "/findSimilar":
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"title": "Roundup", "url": "https://other.example/best"}}, "costDollars": map[string]any{"total": 0}})
		case "/contents":
			json.NewEncoder(w).Encode(map[string]any{
				"results":  []any{map[string]any{"url": "https://a.example/p", "title": "P", "text": "Real text naming Rival."}},
				"statuses": []any{map[string]any{"id": "https://a.example/p", "status": "success"}, map[string]any{"id": "https://b.example/q", "status": "error", "error": map[string]any{"tag": "CRAWL_NOT_FOUND", "httpStatusCode": 404}}},
			})
		default:
			http.Error(w, "unexpected "+r.URL.Path, 400) // a client error isn't retried
		}
	}))
	defer srv.Close()
	t.Setenv(KeyVar, "k")
	cfg := &config.Config{Pricing: map[string]config.Pricing{Model: {SearchPer1K: 4}, ContentsModel: {PagePer1K: 1}}}
	// The first release's URL named the search endpoint; it still works for every endpoint.
	c := &Client{URL: srv.URL + "/search", Cfg: cfg}
	sim, err := c.FindSimilar(context.Background(), "t", "https://seed.example/top-10", []string{"cheapseats.dev", "rival.com"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if b := bodies[0]; b["url"] != "https://seed.example/top-10" || b["numResults"].(float64) != 5 || b["excludeSourceDomain"] != true || len(b["excludeDomains"].([]any)) != 2 {
		t.Errorf("find-similar request %v", b)
	}
	if len(sim.Results) != 1 || sim.Results[0].URL != "https://other.example/best" || sim.CostUSD != 0.004 {
		t.Errorf("find-similar is priced as a search when Exa gives no estimate: %+v %f", sim.Results, sim.CostUSD)
	}
	pages, err := c.Contents(context.Background(), "t", []string{"https://a.example/p", "https://b.example/q"})
	if err != nil {
		t.Fatal(err)
	}
	if b := bodies[1]; b["text"] != true || len(b["urls"].([]any)) != 2 {
		t.Errorf("contents request %v", b)
	}
	if len(pages.Results) != 1 || pages.Results[0].Text != "Real text naming Rival." || pages.Statuses[1].Error.HTTPStatusCode != 404 {
		t.Errorf("contents %+v", pages)
	}
	if pages.CostUSD != 0.001 {
		t.Errorf("one page read at $1 per 1,000: %f", pages.CostUSD)
	}
	if paths[0] != "/findSimilar" || paths[1] != "/contents" {
		t.Errorf("paths %v", paths)
	}
	// A search with excluded domains, through Run.
	if _, err := c.Run(context.Background(), "t", Query{Text: "q", Exclude: []string{"x.com"}, N: 2}); err == nil {
		t.Error("the fake has no /search; the error should surface")
	}
	if b := bodies[2]; b["excludeDomains"].([]any)[0] != "x.com" || b["numResults"].(float64) != 2 {
		t.Errorf("search request %v", b)
	}
}
