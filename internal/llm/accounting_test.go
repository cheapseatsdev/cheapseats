package llm

import (
	"context"
	"encoding/json"
	"github.com/metcalfc/popcorn/internal/budget"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMalformedStructuredOutputStillCharged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": "invalid json"}}, "usage": map[string]any{"input_tokens": 1000, "output_tokens": 100}})
	}))
	defer srv.Close()
	old := AnthropicURL
	AnthropicURL = srv.URL
	defer func() { AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	d, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	c := &config.Config{Pricing: map[string]config.Pricing{"fake": {Input: 1, Output: 10}}}
	var out any
	_, e = Structured(WithAccounting(context.Background(), budget.New(c, d)), "fake", "system", "question", 100, nil, &out)
	if e == nil {
		t.Fatal("expected parse failure")
	}
	spent, _ := d.SpentToday(context.Background())
	if spent < .00199 || spent > .00201 {
		t.Fatal(spent)
	}
}
func TestExhaustedBudgetBlocksProviderAndUnpricedModel(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()
	old := AnthropicURL
	AnthropicURL = srv.URL
	defer func() { AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	d, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	c := &config.Config{Budget: config.Budget{DailyUSD: .1}, Pricing: map[string]config.Pricing{"fake": {Input: 1, Output: 1}}}
	ctx := WithAccounting(context.Background(), budget.New(c, d))
	for _, m := range []string{"fake", "unpriced"} {
		if _, e := Anthropic(ctx, map[string]any{"model": m}); e == nil {
			t.Fatal("expected admission failure")
		}
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}
