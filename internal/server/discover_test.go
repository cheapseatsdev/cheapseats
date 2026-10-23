package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The API reads competitor suggestions and dismisses one without calling a provider; the MCP
// tool runs the discovery against Exa and returns the top suggestions with TOML.
func TestCompetitorSuggestionsOverTheAPIAndMCP(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "x")
	os.Unsetenv("CHEAPSEATS_TOKENS")
	cfg := testConfig(t)
	cfg.TypeSafe = nil
	cfg.Exa = &config.Exa{}
	cfg.Pricing["exa-search"] = config.Pricing{SearchPer1K: 4}
	cfg.Competitors = []sets.Competitor{{Name: "Acme", Domains: []string{"acme.example"}}}
	db, _ := store.Open(t.TempDir())
	defer db.Close()
	a := newApp(t, cfg, db)
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// Empty table: an empty list, and nothing to dismiss.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/competitors/suggestions", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"suggestions": []`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/competitors/suggestions/nowhere.example/dismiss", nil))
	if rec.Code != 404 {
		t.Errorf("dismissing an unknown host: %d", rec.Code)
	}

	calls := 0
	ex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var results []any
		if strings.Contains(r.URL.Path, "findSimilar") {
			results = []any{map[string]any{"title": "Gadget | Widgets for teams", "url": "https://gadget.example/"}}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results, "costDollars": map[string]any{"total": 0.004}})
	}))
	defer ex.Close()
	t.Setenv(exa.KeyVar, "")
	t.Setenv(config.ExaBaseURLVar, ex.URL)

	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "suggest_competitors", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("suggest_competitors: %v %v", err, res)
	}
	out, _ := json.Marshal(res.StructuredContent)
	for _, want := range []string{`"host":"gadget.example"`, `"name":"Gadget"`, `"why":"similar to Acme's site and Cheap Seats'"`, `"total":1`, `domains = [\"gadget.example\"]`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("want %s in %s", want, out)
		}
	}
	if calls != 2 { // Acme's home page and the brand's
		t.Errorf("Exa calls %d", calls)
	}

	// The API reads what the tool stored, and dismissing hides it, with no further Exa call.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/competitors/suggestions", nil))
	if !strings.Contains(rec.Body.String(), `"host": "gadget.example"`) || !strings.Contains(rec.Body.String(), "[[competitors]]") {
		t.Errorf("GET: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/competitors/suggestions/gadget.example/dismiss", nil))
	if rec.Code != 200 {
		t.Errorf("dismiss: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/competitors/suggestions", nil))
	if !strings.Contains(rec.Body.String(), `"suggestions": []`) || calls != 2 {
		t.Errorf("after dismissing: %s (%d Exa calls)", rec.Body.String(), calls)
	}
}
