package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	leakKey    = "sk-ant-api03-SENTINELkey0123456789"
	leakBearer = "SENTINELbearer0123456789abcdef"
	leakEnv    = "SENTINELenvkey-0123456789"
	leakPage   = "PAGETEXT-SENTINEL the pricing page says"
)

func leakCheck(t *testing.T, where, s string) {
	t.Helper()
	for _, v := range []string{"SENTINELkey", leakBearer, leakEnv, "PAGETEXT-SENTINEL"} {
		if strings.Contains(s, v) {
			t.Errorf("%s carries %q:\n%s", where, v, limit(s))
		}
	}
}

// leakFakeAnthropic fails every call with an error reply that carries a key in its message, and
// a bearer token, the configured key's value and page text in the rest of the body (an echoed
// request). The message is what the product shows, redacted and cut short; the rest never is.
func leakFakeAnthropic(t *testing.T) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"key %s rejected"},"request_id":"req_leak","echo":{"authorization":"Bearer %s","x-api-key":"%s","content":"%s"}}`, leakKey, leakBearer, leakEnv, leakPage)
	}))
	t.Cleanup(api.Close)
	old := llm.AnthropicURL
	llm.AnthropicURL = api.URL
	t.Cleanup(func() { llm.AnthropicURL = old })
	t.Setenv("ANTHROPIC_API_KEY", leakEnv)
	t.Setenv("OPENAI_API_KEY", "")
}

// Every answer of a run fails with that reply. The secrets and the page text appear in no log
// line, no stored error, no page, no export and no MCP reply; the answer's redacted error is
// there. (The run is partial, so its analysis error is tested on a done run, below.)
func TestFailedCallsLeakNothingToAnySink(t *testing.T) {
	leakFakeAnthropic(t)
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	cfg := testConfig(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := newApp(t, cfg, db)
	set, _ := a.Set("ai-search-visibility")
	r, err := a.Runs.Create(context.Background(), set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	a.Runs.Execute(context.Background(), r.ID)

	var stored []string
	rows, err := db.Query(`SELECT COALESCE(error, '') FROM results WHERE run_id=? UNION ALL SELECT COALESCE(error, '') || ' ' || COALESCE(fixes_error, '') FROM runs WHERE id=?`, r.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		stored = append(stored, s)
	}
	rows.Close()
	all := strings.Join(stored, "\n")
	if !strings.Contains(all, "HTTP 400: ") || !strings.Contains(all, "[REDACTED]") {
		t.Fatalf("the provider's redacted message was not stored:\n%s", limit(all))
	}
	leakCheck(t, "stored errors", all)

	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/runs/" + r.ID, "/runs/" + r.ID + "/export.json", "/runs/" + r.ID + "/export.csv", "/quick-wins", "/sets/" + set.ID} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		leakCheck(t, path, rec.Body.String())
	}

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	var firstResult int64
	db.QueryRow(`SELECT MIN(id) FROM results WHERE run_id=?`, r.ID).Scan(&firstResult)
	for name, args := range map[string]map[string]any{
		"get_run":        {"run": r.ID},
		"get_result":     {"run": r.ID, "result": firstResult},
		"list_runs":      {},
		"get_next_steps": {},
		"get_quick_wins": {},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				if name == "get_result" && !strings.Contains(tc.Text, `HTTP 400: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"key [REDACTED] rejected\"},\"request_id\":\"req_leak\"}`) {
					t.Errorf("get_result lacks the redacted error: %s", limit(tc.Text))
				}
				leakCheck(t, "MCP "+name, tc.Text)
			}
		}
	}
	leakCheck(t, "log", logs.String())
}

// A done run whose analysis failed on that reply: the hub, the run page, Next steps, MCP and the
// set's API show the provider's redacted message and request id, and none of the secrets.
func TestAnAnalysisErrorShowsRedactedEverywhere(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	id := execute()
	leakFakeAnthropic(t) // after the run: from here every call fails with the leaking reply
	_, callErr := llm.Anthropic(context.Background(), map[string]any{"model": "m"})
	verr := &analysis.VerificationError{Candidates: 1, Err: callErr}
	if _, err := db.Exec(`UPDATE runs SET fixes_error=? WHERE id=?`, verr.Error(), id); err != nil {
		t.Fatal(err)
	}
	var setID string
	db.QueryRow(`SELECT set_id FROM runs WHERE id=?`, id).Scan(&setID)
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	const shown = "HTTP 400: key [REDACTED] rejected"
	for _, path := range []string{"/", "/runs/" + id, "/next-steps", "/quick-wins"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		body := rec.Body.String()
		if !strings.Contains(body, shown) || !strings.Contains(body, "req_leak") {
			t.Errorf("%s doesn't show the analysis error: %s", path, limit(body))
		}
		leakCheck(t, path, body)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for name, args := range map[string]map[string]any{
		"get_run":        {"run": id},
		"get_next_steps": {"brief": true},
		"get_quick_wins": {"brief": true},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		var all string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				all += tc.Text
			}
		}
		if !strings.Contains(all, shown) || !strings.Contains(all, "req_leak") {
			t.Errorf("MCP %s doesn't carry the analysis error: %s", name, limit(all))
		}
		leakCheck(t, "MCP "+name, all)
	}
}
