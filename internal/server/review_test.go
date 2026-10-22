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

	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The prompt review's page, JSON and MCP tool read three stored runs and call no model; only
// suggest (the button, or suggest=true) makes the one analyst call, and its diff keeps the ids.
// get_started suggests a review once a set has four weeks of runs and none this month.
func TestPromptReview(t *testing.T) {
	var reviews atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		var text string
		switch {
		case strings.Contains(s, "You maintain the questions"):
			reviews.Add(1)
			text = `{"rewrites":[{"id":"geo-aeo-llm-seo-difference","text":"What is GEO and how is it different from SEO?","why":"flat"},{"id":"no-such-prompt","text":"x","why":"y"}],"retire":[],"add":[{"stage":"pricing","text":"How much does an AI visibility tracker cost?","intent":"choice"}]}`
		case strings.Contains(s, "output_config"):
			text = `{"verdict":"absent","position":null,"top_pick":"Profound","named":["Profound","Peec AI"],"reasoning":"r","why_not_top":null,"objections":[]}`
		case strings.Contains(s, `"tools"`):
			w.Write([]byte(`{"content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"best geo tools pricing"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://profound.example/","title":"Profound"}]},{"type":"text","text":"Use Profound."}],"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		default:
			w.Write([]byte(`{"content":[{"type":"text","text":"Use Profound."}],"usage":{"input_tokens":10,"output_tokens":5}}`))
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
	os.Unsetenv("CHEAPSEATS_TOKENS")

	db, _ := store.Open(t.TempDir())
	defer db.Close()
	a := newApp(t, testConfig(t), db)
	set, _ := a.Set("ai-search-visibility")
	ctx := context.Background()
	for range 3 {
		r, err := a.Runs.Create(ctx, set, "manual")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Runs.Execute(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code, rec.Body.String()
	}
	// Only reading the page (or the CLI, or MCP) is a review: not the JSON, not a HEAD.
	get("/api/sets/" + set.ID + "/review")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("HEAD", "/sets/"+set.ID+"/review", nil))
	if at := a.LastReview(ctx, set.ID); at != "" || rec.Code != 200 {
		t.Errorf("the JSON or a HEAD recorded a review: %q %d", at, rec.Code)
	}
	// Three runs of one search and one recall answer a prompt: six answers, all absent.
	if code, body := get("/sets/" + set.ID + "/review"); code != 200 || !strings.Contains(body, `<h2 id="flat">Flat`) || !strings.Contains(body, "always_absent") || !strings.Contains(body, "Propose rewrites") {
		t.Errorf("page: %d %s", code, limit(body))
	}
	if a.LastReview(ctx, set.ID) == "" {
		t.Error("reading the page didn't record a review")
	}
	if code, body := get("/sets/" + set.ID); code != 200 || !strings.Contains(body, `href="/sets/`+set.ID+`/review"`) {
		t.Errorf("set page links the review: %d", code)
	}
	code, body := get("/api/sets/" + set.ID + "/review?days=60")
	var rev app.Review
	if code != 200 || json.Unmarshal([]byte(body), &rev) != nil || rev.Days != 60 || rev.Runs != 3 || len(rev.Prompts) != len(set.Prompts) || len(rev.NotEnough) != 0 {
		t.Fatalf("api: %d %s", code, limit(body))
	}
	if code, _ := get("/api/sets/" + set.ID + "/review?days=45"); code != 400 {
		t.Errorf("days=45: %d", code)
	}
	if code, _ := get("/api/sets/no-such-set/review"); code != 404 {
		t.Errorf("unknown set: %d", code)
	}
	if n := reviews.Load(); n != 0 {
		t.Fatalf("reading the review called the analyst %d times", n)
	}

	ts := httptest.NewServer(h)
	defer ts.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(args map[string]any) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "review_prompts", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		return text, res.IsError
	}
	text, isErr := call(map[string]any{"set": set.ID})
	var dg struct {
		Days  int `json:"days"`
		Flags map[string]struct {
			Count int `json:"count"`
			Top   []struct {
				Prompt, Numbers, Suggestion string
			} `json:"top"`
		} `json:"flags"`
		Gaps      []map[string]any `json:"gaps"`
		Suggested any              `json:"suggested"`
	}
	if isErr || json.Unmarshal([]byte(text), &dg) != nil || dg.Days != 30 || len(dg.Flags) != len(app.ReviewKinds) || dg.Flags["flat"].Count != len(set.Prompts) ||
		len(dg.Flags["flat"].Top) != 5 || dg.Suggested != nil || len(text) > 12000 {
		t.Errorf("review_prompts (%d chars): %s", len(text), limit(text))
	}
	if reviews.Load() != 0 {
		t.Fatal("review_prompts without suggest called the analyst")
	}
	if _, isErr := call(map[string]any{"set": set.ID, "days": 45}); !isErr {
		t.Error("days=45 should be a tool error")
	}
	text, isErr = call(map[string]any{"set": set.ID, "suggest": true})
	if isErr || reviews.Load() != 1 || !strings.Contains(text, `+text = \"What is GEO and how is it different from SEO?\"`) || !strings.Contains(text, `id = \"geo-aeo-llm-seo-difference\"`) ||
		!strings.Contains(text, `+id = \"much-ai-visibility-tracker-cost\"`) || !strings.Contains(text, `"dropped":1`) {
		t.Errorf("suggest: %s", limit(text))
	}
	// The page's button: one more call, the diff on the page.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/sets/"+set.ID+"/review?days=30", nil))
	if reviews.Load() != 2 || !strings.Contains(rec.Body.String(), "@@ rewrite geo-aeo-llm-seo-difference") {
		t.Errorf("button: %d %s", reviews.Load(), limit(rec.Body.String()))
	}

	// get_started: the runs are a month old and the set was reviewed today, so no review step;
	// a month on, there is one.
	month := time.Now().UTC().AddDate(0, 0, -40).Format(time.RFC3339)
	db.Exec(`UPDATE runs SET created_at=?`, month)
	if st := stepTo(s.frontDoor(ctx), "review_prompts"); st != nil {
		t.Errorf("reviewed today: %+v", st)
	}
	db.Exec(`UPDATE prompt_reviews SET reviewed_at=?`, month)
	if st := stepTo(s.frontDoor(ctx), "review_prompts"); st == nil || st.Args["set"] != set.ID {
		t.Errorf("due a review: %+v", s.frontDoor(ctx).NextSteps)
	}
}
