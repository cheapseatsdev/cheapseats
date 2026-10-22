package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metcalfc/popcorn/internal/app"
	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A set's citation stability, over daily runs whose answers cite the same host until the newest
// adds another: no score from two transitions, a score from three, the newest transition's new
// host, on the set page, the API and MCP.
func TestCitationStability(t *testing.T) {
	var extra atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		var text string
		switch {
		case strings.Contains(s, "sharp growth lead"):
			text = `{"insights":"x","fixes":[]}`
		case strings.Contains(s, "output_config"):
			text = `{"verdict":"mentioned","position":2,"top_pick":"Docker Sandboxes","named":["Docker Sandboxes","Popcorn"],"reasoning":"r","why_not_top":"w","objections":[]}`
		case strings.Contains(s, `"tools"`):
			cites := `{"url":"https://docs.docker.com/ai/sandboxes/security/"}`
			if extra.Load() {
				cites += `,{"url":"https://www.reddit.com/r/devops/comments/x/sandboxes/"}`
			}
			w.Write([]byte(`{"content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://docs.docker.com/ai/sandboxes/security/","title":"x"}]},{"type":"text","text":"Use Docker Sandboxes or Popcorn.","citations":[` + cites + `]}],"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		default:
			w.Write([]byte(`{"content":[{"type":"text","text":"Use Docker Sandboxes or Popcorn."}],"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer api.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = api.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("POPCORN_TOKENS")

	db, _ := store.Open(t.TempDir())
	defer db.Close()
	a := newApp(t, testConfig(t), db)
	set, _ := a.Set("ai-search-visibility")
	ctx := context.Background()
	// Runs on consecutive days, the newest today.
	day := 0
	execute := func() {
		r, err := a.Runs.Create(ctx, set, "manual")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Runs.Execute(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
		db.Exec(`UPDATE runs SET created_at=? WHERE id=?`, time.Now().UTC().AddDate(0, 0, day-3).Format(time.RFC3339), r.ID)
		day++
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	stability := func() app.CitationStability {
		var cs app.CitationStability
		json.Unmarshal([]byte(get("/api/sets/"+set.ID+"/citation-stability")), &cs)
		return cs
	}

	for range 3 {
		execute()
	}
	if cs := stability(); cs.Runs != 3 || len(cs.Transitions) != 2 || cs.Score != nil || !strings.Contains(cs.Note, "needs 3") || cs.Transitions[0].Similarity != 100 {
		t.Fatalf("two transitions: %+v", cs)
	}
	extra.Store(true)
	execute()
	cs := stability()
	// Every answer cites docs.docker.com and, newest, reddit too: half the citations each.
	if cs.Score == nil || len(cs.Transitions) != 3 || !near(cs.Transitions[0].Similarity, 50) || !near(cs.Transitions[0].Overlap, 50) ||
		len(cs.Transitions[0].New) != 1 || cs.Transitions[0].New[0] != "reddit.com" || !near(*cs.Score, (50+100+100)/3.0) {
		t.Fatalf("three transitions: %+v", cs)
	}
	if body := get("/sets/" + set.ID); !strings.Contains(body, `<h2 id="stability">Citation stability</h2>`) || !strings.Contains(body, "<b>83</b> stability") ||
		!strings.Contains(body, `New: <span class="mono">reddit.com</span>`) {
		t.Errorf("set page:\n%s", limit(body[strings.Index(body, "<h2>Prompts"):]))
	}
	if body := get("/api/sets/" + set.ID + "/citation-stability?track=recall"); !strings.Contains(body, `"runs": 0`) {
		t.Errorf("recall cites nothing: %s", limit(body))
	}

	ts := httptest.NewServer(h)
	defer ts.Close()
	cl, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	res, err := cl.CallTool(ctx, &mcp.CallToolParams{Name: "get_citation_stability", Arguments: map[string]any{"set": set.ID}})
	if err != nil || res.IsError {
		t.Fatalf("get_citation_stability: %v %+v", err, res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, `"score":83.3`) || !strings.Contains(text, `"newest_new":["reddit.com"]`) || !strings.Contains(text, `"similarity":50`) || len(text) > 4000 {
		t.Errorf("get_citation_stability: %s", limit(text))
	}
}

func near(a, b float64) bool { d := a - b; return d < 1e-6 && d > -1e-6 }
