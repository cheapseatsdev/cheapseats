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
