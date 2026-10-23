package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The review page, its JSON and review_prompts show Jev's stored judgments of the prompts'
// wording and never ask for them: with none stored the section says so; once cheapseats check
// (here, JudgePrompts) has asked, the flags show with their probabilities.
func TestReviewShowsJevsPromptJudgments(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	os.Unsetenv("CHEAPSEATS_TOKENS")
	var requests atomic.Int32
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		answers := map[string]any{}
		for kind := range req["questions"].(map[string]any) {
			if kind == app.JudgeIntent {
				answers[kind] = map[string]any{"type": "choice", "choice": "info", "probabilities": map[string]float64{"info": 0.8, "selection": 0.2}}
			} else {
				answers[kind] = map[string]any{"type": "noul", "noul": 0.1}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": answers, "usage": map[string]any{"input_tokens": 100}})
	}))
	defer jev.Close()
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, jev.URL)

	cfg := testConfig(t)
	cfg.TypeSafe = &config.TypeSafe{Model: "jev-1.14.0"}
	cfg.Pricing["jev-1.14.0"] = config.Pricing{Input: 0.042}
	db, _ := store.Open(t.TempDir())
	defer db.Close()
	a := newApp(t, cfg, db)
	set, _ := a.Set("ai-search-visibility")
	ctx := context.Background()
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
	// Nothing stored yet: the section is there, empty, and the page asked nothing.
	if code, body := get("/sets/" + set.ID + "/review"); code != 200 || !strings.Contains(body, `<h2 id="jev">Jev`) || !strings.Contains(body, "None yet.") || requests.Load() != 0 {
		t.Fatalf("empty: %d requests %d %s", code, requests.Load(), limit(body))
	}
	// check asks; the fake reads every prompt as an explanation, so those not set to info flag.
	want := 0
	for _, p := range set.Prompts {
		if !p.Info() {
			want++
		}
	}
	j, err := a.JudgePrompts(ctx, set)
	if err != nil || want == 0 || len(j.Intent) != want || j.Count() != want {
		t.Fatalf("judging: %v want %d got %+v", err, want, j)
	}
	n := requests.Load()
	code, body := get("/sets/" + set.ID + "/review")
	if code != 200 || !strings.Contains(body, `consider intent = &#34;info&#34;`) || !strings.Contains(body, `(0.80)`) || strings.Contains(body, "None yet.") {
		t.Errorf("page with rows: %d %s", code, limit(body))
	}
	code, body = get("/api/sets/" + set.ID + "/review")
	var rev app.Review
	if code != 200 || json.Unmarshal([]byte(body), &rev) != nil || rev.Jev == nil || len(rev.Jev.Intent) != want || rev.Jev.Judged == 0 || rev.Jev.Pending != 0 {
		t.Errorf("api: %d %s", code, limit(body))
	}

	ts := httptest.NewServer(h)
	defer ts.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "review_prompts", Arguments: map[string]any{"set": set.ID}})
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	var dg struct {
		Jev *app.PromptJudgments `json:"jev"`
	}
	if res.IsError || json.Unmarshal([]byte(text), &dg) != nil || dg.Jev == nil || len(dg.Jev.Intent) != want {
		t.Errorf("review_prompts: %s", limit(text))
	}
	if requests.Load() != n {
		t.Errorf("reading the page, the JSON or the tool asked Jev: %d requests, had %d", requests.Load(), n)
	}
}
