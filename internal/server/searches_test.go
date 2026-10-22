package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSearchesPage(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body)
		s := string(b)
		switch {
		case strings.Contains(s, "output_config"):
			text := `{"verdict":"absent","named":["Peec AI"],"reasoning":"r"}`
			json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
		case strings.Contains(s, `"tools"`):
			w.Write([]byte(`{"content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"Peec AI vs Profound 2026"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://peec.ai/compare","title":"Peec AI vs Profound"}]},{"type":"text","text":"Use Peec AI."}],"usage":{"input_tokens":10,"output_tokens":5}}`))
		default:
			w.Write([]byte(`{"content":[{"type":"text","text":"Use Peec AI."}],"usage":{"input_tokens":10,"output_tokens":5}}`))
		}
	}))
	defer api.Close()
	llm.AnthropicURL = api.URL
	t.Setenv("ANTHROPIC_API_KEY", "fake-review-provider-key-123456")
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("POPCORN_TOKENS")

	db, _ := store.Open(t.TempDir())
	a := newApp(t, testConfig(t), db)
	set, _ := a.Set("ai-search-visibility")
	ctx := context.Background()
	r, err := a.Runs.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Runs.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for path, want := range map[string]string{
		"/runs/" + r.ID:                          "/runs/" + r.ID + "/searches",
		"/runs/" + r.ID + "/searches":            "Peec AI vs Profound 2026",
		"/runs/" + r.ID + "/searches?pool=1":     `id = &#34;peec-ai-vs-profound-2026&#34;`,
		"/api/runs/" + r.ID + "/searches?pool=1": `"name": "Profound"`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d, want %q in:\n%s", path, rec.Code, want, limit(rec.Body.String()))
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sets/"+set.ID+"/searches", nil))
	if loc := rec.Header().Get("Location"); loc != "/runs/"+r.ID+"/searches?pool=1" {
		t.Errorf("set searches: %d %q", rec.Code, loc)
	}

	ts := httptest.NewServer(h)
	defer ts.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_search_queries", Arguments: map[string]any{"run": r.ID, "limit": 3}})
	if err != nil || res.IsError {
		t.Fatalf("get_search_queries: %v %v", res, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	// Every prompt ran the same query, naming two competitors none of the prompts named.
	for _, want := range []string{`"brand_named":"0/`, `"injected":["Peec AI","Profound"]`, `"word":"2026"`, `[[prompts]]`} {
		if !strings.Contains(text, want) {
			t.Errorf("get_search_queries: want %s in %s", want, limit(text))
		}
	}
	if len(text) > 12000 {
		t.Errorf("get_search_queries is %d bytes", len(text))
	}
}
