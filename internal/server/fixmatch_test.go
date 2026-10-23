package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two sets' latest fix lists each ask for a pricing page. With nothing in fix_matches the page,
// the API and MCP show both; with the pair decided, one card says the other set also asks for it.
func TestQuickWinsShowWhichSetsAlsoAskForAPage(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	os.Unsetenv("CHEAPSEATS_TOKENS")
	db, _ := store.Open(t.TempDir())
	defer db.Close()
	a := newApp(t, testConfig(t), db)
	ctx := context.Background()
	fixes := map[string]analysis.Fix{
		"ai-search-visibility":   {Rank: 1, Type: analysis.NewPage, Title: "Publish a pricing page", Target: "https://cheapseats.dev/pricing", Claim: "c", DoneWhen: "d", Prompts: []int{1}, Effort: "S"},
		"fix-ai-recommendations": {Rank: 1, Type: analysis.NewPage, Title: "Add a plans and pricing page", Target: "https://cheapseats.dev/plans", Claim: "c", DoneWhen: "d", Prompts: []int{1}, Effort: "M"},
	}
	for id, f := range fixes {
		set, ok := a.Set(id)
		if !ok {
			t.Fatalf("no set %s", id)
		}
		r, err := a.Runs.Create(ctx, set, "test")
		if err != nil {
			t.Fatal(err)
		}
		fj, _ := json.Marshal([]analysis.Fix{f})
		if _, err := db.Exec(`UPDATE runs SET status='done', finished_at=?, fixes=?, fixes_at=? WHERE id=?`, store.Now(), string(fj), store.Now(), r.ID); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	ts := httptest.NewServer(h)
	defer ts.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	quickWins := func() string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_quick_wins", Arguments: map[string]any{"brief": true}})
		if err != nil || res.IsError {
			t.Fatalf("get_quick_wins: %v %+v", err, res)
		}
		text := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		return text
	}

	// Without rows: two cards, nothing about other sets.
	if body := get("/quick-wins"); strings.Contains(body, "also asked for by") || strings.Count(body, `class="fix-item"`) != 2 {
		t.Errorf("before: %s", limit(body))
	}
	var api []app.QuickWin
	json.Unmarshal([]byte(get("/api/quick-wins")), &api)
	if len(api) != 2 || len(api[0].AlsoIn) != 0 {
		t.Errorf("api before: %+v", api)
	}
	if out := quickWins(); strings.Contains(out, "also_in") {
		t.Errorf("mcp before: %s", limit(out))
	}

	// The pair decided (as the pass stores it): one card, with the other set under also_in.
	sa, sb := app.FixSignature(fixes["ai-search-visibility"]), app.FixSignature(fixes["fix-ai-recommendations"])
	if _, err := db.Exec(`INSERT INTO fix_matches(key, fix_a, fix_b, confidence, source, decided_at) VALUES (?,?,?,?,?,?)`, app.FixMatchKey(sa, sb), sa, sb, 0.9, "jev", store.Now()); err != nil {
		t.Fatal(err)
	}
	body := get("/quick-wins")
	if strings.Count(body, `class="fix-item"`) != 1 || !strings.Contains(body, "also asked for by Getting recommended instead of a competitor") || !strings.Contains(body, "<b>Also asked for by:</b>") {
		t.Errorf("after: %s", limit(body))
	}
	json.Unmarshal([]byte(get("/api/quick-wins")), &api)
	if len(api) != 1 || len(api[0].AlsoIn) != 1 || api[0].AlsoIn[0].SetID != "fix-ai-recommendations" || api[0].AlsoIn[0].Rank != 1 || api[0].AlsoIn[0].Confidence != 0.9 {
		t.Errorf("api after: %+v", api)
	}
	// Filtering by the other set still finds the card.
	json.Unmarshal([]byte(get("/api/quick-wins?set=fix-ai-recommendations")), &api)
	if len(api) != 1 {
		t.Errorf("filtered by the other set: %+v", api)
	}
	if out := quickWins(); !strings.Contains(out, `"also_in"`) || !strings.Contains(out, `"set_name":"Getting recommended instead of a competitor"`) {
		t.Errorf("mcp after: %s", limit(out))
	}
}
