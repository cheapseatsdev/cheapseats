package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metcalfc/popcorn/internal/audit"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The site audit, the citation check and the crawler counts end to end: the audit's findings reach
// the analyst, the check reads the page the answers cited, and the pages and MCP tools show them.
func TestSiteChecks(t *testing.T) {
	defer audit.AllowLoopbackForTests()() // the sites are httptest servers
	// The brand's site: an app shell with no text until JavaScript runs.
	brandSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<!doctype html><html lang="en"><head><title>Popcorn</title></head><body><div id="root"></div></body></html>`)
	}))
	defer brandSite.Close()
	// A third-party roundup the answers cite, naming a competitor but not the brand.
	var competitor string
	third := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/roundup" {
			fmt.Fprintf(w, `<html><body><h1>The best tools</h1><p>We like %s.</p></body></html>`, competitor)
			return
		}
		http.NotFound(w, r)
	}))
	defer third.Close()
	var mu sync.Mutex
	var analystPrompt string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		var text string
		switch {
		case strings.Contains(s, "sharp growth lead"):
			mu.Lock()
			analystPrompt = s
			mu.Unlock()
			text = `{"insights":"## Why Popcorn lost\nx","fixes":[]}`
		case strings.Contains(s, "output_config"):
			text = `{"verdict":"absent","position":null,"top_pick":null,"named":[],"reasoning":"r","why_not_top":"w","objections":[]}`
		case strings.Contains(s, `"tools"`):
			fmt.Fprintf(w, `{"content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"%[1]s/roundup","title":"x"}]},{"type":"text","text":"See the roundup.","citations":[{"url":"%[1]s/roundup"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`, third.URL)
			return
		default:
			w.Write([]byte(`{"content":[{"type":"text","text":"Use something."}],"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer api.Close()
	llm.AnthropicURL = api.URL
	t.Setenv("ANTHROPIC_API_KEY", "fake-review-provider-key-123456")
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("POPCORN_TOKENS")

	cfg := testConfig(t)
	cfg.Brand.Sites = []config.Site{{URL: brandSite.URL, Kind: "marketing"}}
	cfg.Brand.Domains = nil
	db, _ := store.Open(t.TempDir())
	defer db.Close()
	a := newApp(t, cfg, db)
	set, _ := a.Set("ai-search-visibility")
	competitor = set.Competitors[0].Name
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	do := func(method, path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, limit(rec.Body.String()))
		}
		return rec.Body.String()
	}
	if body := do("GET", "/audit"); !strings.Contains(body, "No audit yet") {
		t.Errorf("/audit before any audit: %s", limit(body))
	}
	if body := do("POST", "/api/audit"); !strings.Contains(body, `"check": "page.needs-js"`) {
		t.Errorf("audit: %s", limit(body))
	}

	// A run: its fix list sees the audit's errors.
	r, err := a.Runs.Create(context.Background(), set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Runs.Execute(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if !strings.Contains(analystPrompt, "page.needs-js") || !strings.Contains(analystPrompt, "audit of Popcorn") {
		t.Errorf("the analyst didn't get the audit's findings: %s", limit(analystPrompt))
	}
	mu.Unlock()

	if body := do("POST", "/api/runs/"+r.ID+"/citation-check"); !strings.Contains(body, `"outreach": true`) || !strings.Contains(body, competitor) {
		t.Errorf("citation check: %s", limit(body))
	}
	log := `203.0.113.7 - - [01/Jan/2026:00:00:00 +0000] "GET / HTTP/1.1" 200 10 "-" "GPTBot/1.2"` + "\n"
	log = strings.Replace(log, "01/Jan/2026:00:00:00 +0000", nowCLF(), 1)
	if st, err := a.ImportCrawl(context.Background(), strings.NewReader(log), "127.0.0.1"); err != nil || st.Counted != 1 {
		t.Fatalf("import: %+v %v", st, err)
	}

	for path, want := range map[string]string{
		"/audit":                                "page.needs-js",
		"/audit?run=" + r.ID:                    "outreach</span>",
		"/crawlers":                             "crawled, never cited</span>",
		"/crawlers?flag=cited-not-crawled":      "AI crawlers",
		"/api/crawlers":                         `"training": 1`,
		"/api/runs/" + r.ID + "/citation-check": competitor,
	} {
		if body := do("GET", path); !strings.Contains(body, want) {
			t.Errorf("%s: want %q in %s", path, want, limit(body))
		}
	}

	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	// MCP can audit only the brand's sites, not make the server fetch any address it's given.
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "audit_sites", Arguments: map[string]any{"site": "169.254.169.254"}}); err != nil || !res.IsError {
		t.Errorf("audit_sites on another site: %v %+v", err, res)
	}
	for name, c := range map[string]struct {
		args map[string]any
		want string
	}{
		"audit_sites":     {map[string]any{"cached": true}, "page.needs-js"},
		"check_citations": {map[string]any{"run": r.ID, "cached": true}, `"outreach":true`},
		"get_crawlers":    {map[string]any{"flag": "crawled-not-cited"}, "GPTBot"},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: c.args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(b), c.want) {
			t.Errorf("%s: want %q in %s", name, c.want, limit(string(b)))
		}
	}
}

func nowCLF() string { return time.Now().UTC().Format("02/Jan/2006:15:04:05 -0700") }
