package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

// fakeExa answers every search with the given results and records the requests it got.
func fakeExa(t *testing.T, results []map[string]any) (*exa.Client, *[]map[string]any) {
	t.Helper()
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		req["path"] = r.URL.Path
		got = append(got, req)
		json.NewEncoder(w).Encode(map[string]any{"requestId": "r1", "results": results, "costDollars": map[string]any{"total": 0.005}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv(exa.KeyVar, "k")
	return &exa.Client{URL: srv.URL}, &got
}

func exaConfig() *config.Config {
	return &config.Config{
		Answerers: []config.Answerer{{ID: "claude", Provider: "anthropic", Model: "m"}},
		Exa:       &config.Exa{},
		Pricing:   map[string]config.Pricing{"m": {Input: 1, Output: 1, SearchPer1K: 10}, exa.Model: {SearchPer1K: 4}},
	}
}

func TestExaEngineReturnsItsLinksInOrderWithTheDomains(t *testing.T) {
	client, got := fakeExa(t, []map[string]any{
		{"title": "Guide", "url": "https://docs.cheapseats.example/guide", "publishedDate": "2026-10-01"},
		{"title": "", "url": "https://b.dev/compare"},
		{"title": "C", "url": "https://c.example/"},
	})
	cfg := exaConfig()
	res, err := Run(context.Background(), cfg, Input{Query: " sandbox claude code ", Engine: "exa", AllowedDomains: []string{"docs.cheapseats.example", "b.dev"}, Exa: client})
	if err != nil {
		t.Fatal(err)
	}
	if res.Query != "sandbox claude code" || res.Model != exa.Model || res.RanAt == "" {
		t.Fatalf("%+v", res)
	}
	want := []answer.Link{{URL: "https://docs.cheapseats.example/guide", Title: "Guide"}, {URL: "https://b.dev/compare"}, {URL: "https://c.example/", Title: "C"}}
	if !reflect.DeepEqual(res.Links, want) {
		t.Fatalf("links %+v", res.Links)
	}
	if res.CostUSD != 0.005 {
		t.Errorf("cost %f: Exa's own estimate", res.CostUSD)
	}
	if !strings.Contains(string(res.Output), `"publishedDate":"2026-10-01"`) {
		t.Errorf("output should be Exa's results verbatim: %s", res.Output)
	}
	if len(*got) != 1 {
		t.Fatalf("%d requests", len(*got))
	}
	req := (*got)[0]
	if req["path"] != "/search" || req["query"] != "sandbox claude code" || req["numResults"] != float64(ExaResults) {
		t.Errorf("request %v", req)
	}
	if d, _ := req["includeDomains"].([]any); len(d) != 2 || d[0] != "docs.cheapseats.example" || d[1] != "b.dev" {
		t.Errorf("includeDomains %v", req["includeDomains"])
	}
	if _, ok := req["excludeDomains"]; ok {
		t.Errorf("excludeDomains sent with allowed domains: %v", req)
	}
	if _, ok := req["contents"]; ok {
		t.Error("contents cost extra and weren't asked for")
	}
	if res.ToolInput["query"] != "sandbox claude code" || len(res.ToolInput["includeDomains"].([]string)) != 2 {
		t.Errorf("tool input %v", res.ToolInput)
	}

	// Blocked domains go out as excludeDomains.
	if _, err := Run(context.Background(), cfg, Input{Query: "q", Engine: "exa", BlockedDomains: []string{"reddit.com"}, Exa: client}); err != nil {
		t.Fatal(err)
	}
	req = (*got)[1]
	if d, _ := req["excludeDomains"].([]any); len(d) != 1 || d[0] != "reddit.com" {
		t.Errorf("excludeDomains %v", req)
	}
	if _, ok := req["includeDomains"]; ok {
		t.Errorf("includeDomains sent with blocked domains: %v", req)
	}
}

func TestExaEngineNeedsTheExaTable(t *testing.T) {
	client, got := fakeExa(t, nil)
	cfg := exaConfig()
	cfg.Exa = nil
	_, err := Run(context.Background(), cfg, Input{Query: "q", Engine: "exa", Exa: client})
	if err == nil || err.Error() != "no [exa] in the config" {
		t.Fatalf("err %v", err)
	}
	if _, err := Probe(context.Background(), cfg, Input{Query: "q", Engine: "both", Exa: client}); err == nil || !strings.Contains(err.Error(), "no [exa] in the config") {
		t.Fatalf("both without [exa]: %v", err)
	}
	if len(*got) != 0 {
		t.Fatal("Exa was called")
	}
	// A client is the caller's job; a missing one is an error, not a nil dereference.
	cfg.Exa = &config.Exa{}
	if _, err := Run(context.Background(), cfg, Input{Query: "q", Engine: "exa"}); err == nil {
		t.Fatal("no client accepted")
	}
	if _, err := Run(context.Background(), cfg, Input{Query: "q", Engine: "bing"}); err == nil || !strings.Contains(err.Error(), `unknown engine "bing"`) {
		t.Fatalf("unknown engine: %v", err)
	}
	if _, err := Run(context.Background(), cfg, Input{Query: "q", Engine: "both"}); err == nil {
		t.Fatal("Run accepted both; it returns one result")
	}
}

func TestCompareByNormalizedURL(t *testing.T) {
	claude := []answer.Link{
		{URL: "https://a.example/one"},
		{URL: "https://b.example/shared/"}, // a trailing slash isn't a different page
		{URL: "https://a.example/one#top"}, // nor a fragment: counted once
		{URL: "https://c.example/claude-only"},
	}
	exaLinks := []answer.Link{
		{URL: "https://d.example/exa-only"},
		{URL: "https://b.example/shared"},
		{URL: "https://d.example/exa-only/"},
	}
	both, onlyA, onlyB := compare(claude, exaLinks)
	if !reflect.DeepEqual(both, []string{"https://b.example/shared/"}) {
		t.Errorf("overlap %v", both)
	}
	if !reflect.DeepEqual(onlyA, []string{"https://a.example/one", "https://c.example/claude-only"}) {
		t.Errorf("only claude %v", onlyA)
	}
	if !reflect.DeepEqual(onlyB, []string{"https://d.example/exa-only"}) {
		t.Errorf("only exa %v", onlyB)
	}
	// Empty sides are empty lists, not null, so the API's shape doesn't change.
	both, onlyA, onlyB = compare(nil, nil)
	if both == nil || onlyA == nil || onlyB == nil {
		t.Error("nil lists")
	}
}

func TestBothRunsTheTwoEnginesAndCompares(t *testing.T) {
	var claudeTool map[string]any
	anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		claudeTool = llm.Map(llm.List(req["tools"])[0])
		w.Write([]byte(`{"model":"m-1","content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"best seats"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://a.example/one","title":"A"},{"type":"web_search_result","url":"https://b.example/shared/","title":"B"}]},{"type":"text","text":"done"}],"usage":{"input_tokens":1000,"output_tokens":5,"server_tool_use":{"web_search_requests":1}}}`))
	}))
	defer anth.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = anth.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	client, got := fakeExa(t, []map[string]any{
		{"title": "B", "url": "https://b.example/shared"},
		{"title": "C", "url": "https://c.example/exa-only"},
	})
	cfg := exaConfig()
	out, err := Probe(context.Background(), cfg, Input{Query: "best seats", Engine: "both", AllowedDomains: []string{"example"}, Exa: client})
	if err != nil {
		t.Fatal(err)
	}
	cmp, ok := out.(Compare)
	if !ok {
		t.Fatalf("%T", out)
	}
	if cmp.Query != "best seats" || cmp.Claude.Model != "m-1" || cmp.Exa.Model != exa.Model || len(cmp.Claude.Links) != 2 || len(cmp.Exa.Links) != 2 {
		t.Fatalf("%+v", cmp)
	}
	if !reflect.DeepEqual(cmp.Overlap, []string{"https://b.example/shared/"}) || !reflect.DeepEqual(cmp.OnlyClaude, []string{"https://a.example/one"}) || !reflect.DeepEqual(cmp.OnlyExa, []string{"https://c.example/exa-only"}) {
		t.Fatalf("overlap %v only claude %v only exa %v", cmp.Overlap, cmp.OnlyClaude, cmp.OnlyExa)
	}
	if cmp.CostUSD != cmp.Claude.CostUSD+cmp.Exa.CostUSD || cmp.CostUSD <= 0.005 {
		t.Errorf("cost %f (%f + %f)", cmp.CostUSD, cmp.Claude.CostUSD, cmp.Exa.CostUSD)
	}
	// The same domains went to both engines.
	if d := llm.List(claudeTool["allowed_domains"]); len(d) != 1 || d[0] != "example" {
		t.Errorf("claude tool %v", claudeTool)
	}
	if d, _ := (*got)[0]["includeDomains"].([]any); len(d) != 1 || d[0] != "example" {
		t.Errorf("exa request %v", (*got)[0])
	}
	// The default engine's result is a plain Result, as before.
	if res, err := Probe(context.Background(), cfg, Input{Query: "best seats", Exa: client}); err != nil {
		t.Fatal(err)
	} else if r, ok := res.(Result); !ok || r.Model != "m-1" {
		t.Fatalf("%T %+v", res, res)
	}
	if len(*got) != 1 {
		t.Errorf("the default engine called Exa: %d requests", len(*got))
	}
	// Half a comparison is no comparison: an engine's failure is the caller's error.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "bad key", 401) }))
	defer down.Close()
	if _, err := Probe(context.Background(), cfg, Input{Query: "best seats", Engine: "both", Exa: &exa.Client{URL: down.URL}}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Exa refused and both: %v", err)
	}
}
