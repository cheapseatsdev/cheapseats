package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// get_run had no analysis error at all, and get_quick_wins none either, so an MCP client never
// learned that the latest build failed or that what it read was an older, unverified fallback.
// The hub, too, said it showed an older run's analysis without saying that one was unverified.
func TestAnalysisErrorsReachMCP(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	older, latest := execute(), execute()
	unverified := analysis.Encode(analysis.Analysis{Insights: "One candidate.", VerificationFailed: true, Investigations: []analysis.Investigation{{
		Title: "Check: the gateway guide", Why: "w", Check: "Read the gateway guide.", Decision: "d", Evidence: "e", Prompts: []int{1},
	}}})
	stored := `HTTP 400: {"type":"error","error":{"type":"invalid_request_error","message":"schema refused"},"request_id":"req_MCP"}`
	if _, err := db.Exec(`UPDATE runs SET created_at=CASE id WHEN ? THEN '2026-10-01T00:00:00Z' ELSE '2026-10-02T00:00:00Z' END, fixes=CASE id WHEN ? THEN ? END, fixes_error=CASE id WHEN ? THEN ? ELSE 'verification could not complete' END WHERE id IN (?, ?)`, older, older, unverified, latest, stored, older, latest); err != nil {
		t.Fatal(err)
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) []string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		var blocks []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				blocks = append(blocks, tc.Text)
			}
		}
		return blocks
	}

	var run struct {
		Failed *app.AnalysisError `json:"analysis_error"`
	}
	if err := json.Unmarshal([]byte(call("get_run", map[string]any{"run": latest})[0]), &run); err != nil || run.Failed == nil || run.Failed.Message != "HTTP 400: schema refused" || run.Failed.RequestID != "req_MCP" || run.Failed.Shown != app.ShownNone {
		t.Fatalf("get_run analysis_error: %+v %v", run.Failed, err)
	}

	var steps app.NextSteps
	if err := json.Unmarshal([]byte(call("get_next_steps", map[string]any{"brief": true})[0]), &steps); err != nil || len(steps.Errors) != 1 {
		t.Fatalf("get_next_steps analysis_errors: %+v %v", steps.Errors, err)
	}
	e := steps.Errors[0]
	if e.RunID != latest || e.ShownRunID != older || e.Shown != app.ShownOlderRun || !e.VerificationFailed {
		t.Errorf("the older run's analysis must be marked unverified: %+v", e)
	}
	if want := "Showing the analysis from run " + older + " instead. That run's verification failed too, so its candidates are shown as investigations, unverified."; e.Showing() != want {
		t.Errorf("Showing() = %q", e.Showing())
	}

	blocks := call("get_quick_wins", map[string]any{"brief": true})
	var wins []app.QuickWin
	var beside struct {
		Errors []app.AnalysisError `json:"analysis_errors"`
	}
	if len(blocks) != 2 || json.Unmarshal([]byte(blocks[0]), &wins) != nil || json.Unmarshal([]byte(blocks[1]), &beside) != nil || len(beside.Errors) != 1 || beside.Errors[0].RunID != latest {
		t.Fatalf("get_quick_wins must keep its list and add analysis_errors beside it: %q", blocks)
	}
	if blocks := call("get_quick_wins", map[string]any{"brief": true, "set": "another-set"}); len(blocks) != 1 {
		t.Errorf("a set filter keeps other sets' errors: %q", blocks)
	}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/next-steps", nil))
	if !strings.Contains(w.Body.String(), "instead. That run&#39;s verification failed too, so its candidates are shown as investigations, unverified.") {
		t.Error("the Next steps page doesn't say the older run's analysis is unverified")
	}
}
