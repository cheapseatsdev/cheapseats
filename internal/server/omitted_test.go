package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two analyses on 0.7.4 saved no actions and nothing showed why. The run page lists the
// candidates not promoted, collapsed and escaped; /api/runs/{id} always returns them;
// /api/next-steps and get_next_steps only when asked.
func TestOmittedCandidatesAreShownAndExposed(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	id := execute()
	hostile := analysis.Omission{Title: `Compare <script>alert(1)</script> pricing`, Type: analysis.FixPage, Target: "https://example.org/a?b=1&c=2", Stage: analysis.StageReview, Outcome: analysis.OutcomeRejected, Reason: `reject: the page says "<b>free</b>" already.`}
	capped := analysis.Omission{Title: "Add the gateway guide", Type: analysis.NewPage, Target: "https://example.org/gateway", Stage: analysis.StageCap, Outcome: analysis.OutcomeDropped, Reason: "ranked 9; only the 8 best-ranked candidates are reviewed."}
	body := analysis.Encode(analysis.Analysis{Insights: "Nothing to do.", Omitted: []analysis.Omission{hostile, capped}})
	if _, err := db.Exec(`UPDATE runs SET fixes=? WHERE id=?`, body, id); err != nil {
		t.Fatal(err)
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}

	page := get("/runs/" + id)
	for _, want := range []string{
		`<details id="omitted"><summary>Candidates not promoted (2)</summary>`,
		`Compare &lt;script&gt;alert(1)&lt;/script&gt; pricing`,
		`https://example.org/a?b=1&amp;c=2`,
		`review · rejected`,
		`reject: the page says &#34;&lt;b&gt;free&lt;/b&gt;&#34; already.`,
		`cap · dropped`,
		`ranked 9; only the 8 best-ranked candidates are reviewed.`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("run page lacks %s", want)
		}
	}
	if strings.Contains(page, "<script>alert") || strings.Contains(page, "<b>free") {
		t.Error("the run page renders an omission's text as HTML")
	}

	var run struct {
		Omitted []analysis.Omission `json:"omitted"`
	}
	if err := json.Unmarshal([]byte(get("/api/runs/"+id)), &run); err != nil || len(run.Omitted) != 2 || run.Omitted[0] != hostile {
		t.Fatalf("/api/runs/{id} omitted: %+v %v", run.Omitted, err)
	}
	if steps := get("/api/next-steps"); strings.Contains(steps, `"omitted"`) {
		t.Error("/api/next-steps lists omissions unasked")
	}
	var steps struct {
		Omitted []struct {
			analysis.Omission
			RunID string `json:"run_id"`
		} `json:"omitted"`
	}
	if err := json.Unmarshal([]byte(get("/api/next-steps?omitted=true&limit=1")), &steps); err != nil || len(steps.Omitted) != 1 || steps.Omitted[0].RunID != id {
		t.Fatalf("/api/next-steps?omitted=true&limit=1: %+v %v", steps.Omitted, err)
	}

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_next_steps", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("get_next_steps: %v %+v", err, res)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	if strings.Contains(call(map[string]any{"brief": true}), `"omitted"`) {
		t.Error("get_next_steps lists omissions unasked")
	}
	if !strings.Contains(call(map[string]any{"brief": true, "omitted": true}), "ranked 9; only the 8 best-ranked") {
		t.Error("get_next_steps with omitted=true lacks the omissions")
	}

	// An analysis stored before omissions were recorded shows no list, and the API an empty one.
	if _, err := db.Exec(`UPDATE runs SET fixes=? WHERE id=?`, `{"version":1,"insights":"Earlier.","fixes":[],"investigations":[],"market_findings":[]}`, id); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(get("/runs/"+id), "Candidates not promoted") {
		t.Error("an old analysis shows an empty list of candidates not promoted")
	}
	if !strings.Contains(get("/api/runs/"+id), `"omitted": []`) {
		t.Error("/api/runs/{id} for an old analysis lacks an empty omitted list")
	}
}
