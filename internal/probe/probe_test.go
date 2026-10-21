package probe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/llm"
)

func TestProbeReturnsTheToolResultVerbatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		tool := llm.Map(llm.List(req["tools"])[0])
		if d := llm.List(tool["blocked_domains"]); len(d) != 1 || d[0] != "example.com" || tool["max_uses"] != float64(1) {
			t.Errorf("tool: %v", tool)
		}
		w.Write([]byte(`{"model":"m-1","content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"sandbox claude code"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://docs.popcorn.example/a","title":"A","page_age":"2026"},{"type":"web_search_result","url":"https://b.dev/","title":"B"}]},{"type":"text","text":"done"}],"usage":{"input_tokens":1000,"output_tokens":5,"server_tool_use":{"web_search_requests":1}}}`))
	}))
	defer srv.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	cfg := &config.Config{
		Answerers: []config.Answerer{{ID: "gpt", Provider: "openai", Model: "g"}, {ID: "claude", Provider: "anthropic", Model: "m"}},
		Pricing:   map[string]config.Pricing{"m": {Input: 1, Output: 1, SearchPer1K: 10}},
	}
	res, err := Run(context.Background(), cfg, Input{Query: " sandbox claude code ", BlockedDomains: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Query != "sandbox claude code" || res.Model != "m-1" || len(res.Links) != 2 || res.Links[0].URL != "https://docs.popcorn.example/a" {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(string(res.Output), `"page_age":"2026"`) || res.ToolInput["query"] != "sandbox claude code" {
		t.Fatalf("output not verbatim: %s %v", res.Output, res.ToolInput)
	}
	if res.CostUSD <= 0 {
		t.Fatal("probe cost not priced")
	}
	if _, err := Run(context.Background(), cfg, Input{Query: "q", AllowedDomains: []string{"a"}, BlockedDomains: []string{"b"}}); err == nil {
		t.Fatal("allowed and blocked together accepted")
	}
}
