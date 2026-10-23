package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The search probe's second engine: engine exa goes to Exa through the app's client, both
// compares the two, and the page, the API and MCP all take the engine. Without [exa] the page
// is as it was and engine exa is a clear error.
func TestSearchProbeEngines(t *testing.T) {
	var exaReqs []map[string]any
	fakeExa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		exaReqs = append(exaReqs, req)
		json.NewEncoder(w).Encode(map[string]any{"results": []any{
			map[string]any{"title": "Shared", "url": "https://docs.example/shared"},
			map[string]any{"title": "Exa only", "url": "https://exa-only.example/page"},
		}, "costDollars": map[string]any{"total": 0.004}})
	}))
	defer fakeExa.Close()
	anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"model":"m-1","content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"best seats"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://docs.example/shared/","title":"Shared"},{"type":"web_search_result","url":"https://claude-only.example/","title":"Claude only"}]},{"type":"text","text":"done"}],"usage":{"input_tokens":100,"output_tokens":5,"server_tool_use":{"web_search_requests":1}}}`))
	}))
	defer anth.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = anth.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	t.Setenv(exa.KeyVar, "k")
	t.Setenv(config.ExaBaseURLVar, fakeExa.URL)

	cfg := testConfig(t)
	cfg.Exa = &config.Exa{}
	db, _ := store.Open(t.TempDir())
	a := newApp(t, cfg, db)
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	post := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/search-probe", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	code, body := post(`{"query":"best seats","engine":"exa","allowed_domains":["docs.example"]}`)
	if code != 200 || !strings.Contains(body, `"model": "exa-search"`) || !strings.Contains(body, `"url": "https://docs.example/shared"`) {
		t.Fatalf("engine exa: %d %s", code, limit(body))
	}
	if len(exaReqs) != 1 || exaReqs[0]["query"] != "best seats" {
		t.Fatalf("exa requests %v", exaReqs)
	}
	if d, _ := exaReqs[0]["includeDomains"].([]any); len(d) != 1 || d[0] != "docs.example" {
		t.Errorf("domains passed through: %v", exaReqs[0])
	}

	code, body = post(`{"query":"best seats","engine":"both"}`)
	var cmp struct {
		Overlap    []string `json:"overlap"`
		OnlyClaude []string `json:"only_claude"`
		OnlyExa    []string `json:"only_exa"`
		Claude     struct{ Model string }
		Exa        struct{ Model string }
	}
	json.Unmarshal([]byte(body), &cmp)
	if code != 200 || len(cmp.Overlap) != 1 || cmp.Overlap[0] != "https://docs.example/shared/" || len(cmp.OnlyClaude) != 1 || cmp.OnlyClaude[0] != "https://claude-only.example/" || len(cmp.OnlyExa) != 1 || cmp.Claude.Model != "m-1" || cmp.Exa.Model != "exa-search" {
		t.Fatalf("engine both: %d %s", code, limit(body))
	}

	// The default engine is still Claude's tool alone: Exa isn't called.
	n := len(exaReqs)
	if code, body = post(`{"query":"best seats"}`); code != 200 || !strings.Contains(body, `"model": "m-1"`) || strings.Contains(body, "only_exa") {
		t.Fatalf("default engine: %d %s", code, limit(body))
	}
	if len(exaReqs) != n {
		t.Error("the default engine called Exa")
	}
	if code, body = post(`{"query":"best seats","engine":"bing"}`); code != 400 || !strings.Contains(body, "unknown engine") {
		t.Fatalf("unknown engine: %d %s", code, body)
	}

	// The page: a selector with [exa], the form field honoured, and a comparison rendered.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/search", nil))
	if !strings.Contains(rec.Body.String(), `<select name="engine">`) {
		t.Errorf("no engine selector: %s", limit(rec.Body.String()))
	}
	form := url.Values{"query": {"best seats"}, "engine": {"both"}}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/search", strings.NewReader(form.Encode()))
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	for _, want := range []string{"Claude and Exa for “best seats”", "1 in both · 1 only Claude · 1 only Exa", `<option value="both" selected>`, "<h3>Only Exa</h3><ul><li><a href=\"https://exa-only.example/page\""} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("page: want %q in:\n%s", want, limit(rec.Body.String()))
		}
	}

	// MCP: the tool's schema lists engine, and a call with it goes to Exa.
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tool := range tools.Tools {
		if tool.Name != "search_probe" {
			continue
		}
		found = true
		schema, _ := json.Marshal(tool.InputSchema)
		if !strings.Contains(string(schema), `"engine"`) || !strings.Contains(string(schema), "both") || strings.Contains(string(schema), `"Exa"`) {
			t.Errorf("search_probe schema: %s", schema)
		}
		if !strings.Contains(tool.Description, "engine") {
			t.Errorf("description doesn't mention the engine: %s", tool.Description)
		}
	}
	if !found {
		t.Fatal("no search_probe tool")
	}
	n = len(exaReqs)
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "search_probe", Arguments: map[string]any{"query": "best seats", "engine": "exa", "blocked_domains": []string{"reddit.com"}}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if len(exaReqs) != n+1 {
		t.Fatalf("MCP engine exa didn't reach Exa")
	}
	if d, _ := exaReqs[n]["excludeDomains"].([]any); len(d) != 1 || d[0] != "reddit.com" {
		t.Errorf("blocked domains over MCP: %v", exaReqs[n])
	}

	// Without [exa]: the page has no selector, and engine exa says why.
	cfg.Exa = nil
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/search", nil))
	if strings.Contains(rec.Body.String(), `name="engine"`) {
		t.Error("engine selector without [exa]")
	}
	if code, body = post(`{"query":"best seats","engine":"exa"}`); code != 400 || !strings.Contains(body, "no [exa] in the config") {
		t.Fatalf("engine exa without [exa]: %d %s", code, body)
	}
}
