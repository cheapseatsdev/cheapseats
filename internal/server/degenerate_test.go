package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

// A rebuild that gets degenerate analyst output keeps the run's earlier analysis and records why,
// where it used to store the empty result as a success over it.
func TestDegenerateRebuildKeepsTheEarlierAnalysis(t *testing.T) {
	a, _, execute, _ := fixFixture(t)
	id := execute()
	before, _, _ := a.Fixes(context.Background(), id)
	analyst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text := `{"insights":"","fixes":[{"rank":1,"type":"new-page","title":"","target":"t","claim":"c","done_when":"d","check":"","decision":"","evidence":"e","prompts":[1],"effort":"S","displaces":null}],"investigations":[],"market_findings":[]}`
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	t.Cleanup(analyst.Close)
	old := llm.AnthropicURL
	llm.AnthropicURL = analyst.URL
	t.Cleanup(func() { llm.AnthropicURL = old })

	if err := a.BuildFixes(context.Background(), id); !errors.Is(err, analysis.ErrDegenerate) {
		t.Fatalf("want ErrDegenerate, got %v", err)
	}
	after, fe, err := a.Fixes(context.Background(), id)
	if err != nil || len(before) != 1 || len(after) != 1 || after[0].Title != before[0].Title {
		t.Fatalf("the earlier analysis was replaced: before %+v, after %+v, %v", before, after, err)
	}
	if fe == nil || !strings.Contains(*fe, "insights are 0 characters") {
		t.Fatalf("fixes_error = %v", fe)
	}
}
