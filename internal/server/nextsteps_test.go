package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These fixtures test serving preverified work, independently of model verification.
func storeVerifiedServerActions(t *testing.T, a *app.App, id string, fixes ...analysis.Fix) {
	t.Helper()
	for i := range fixes {
		if len(fixes[i].DoneWhen) < 12 {
			fixes[i].DoneWhen = "The proposed page change has been reviewed."
		}
		fixes[i].Verification = verifiedServerEvidence(a, fixes[i])
	}
	if _, err := a.DB.Exec(`UPDATE runs SET status='done', fixes=?, fixes_at=? WHERE id=?`, analysis.Encode(analysis.Analysis{Fixes: fixes}), store.Now(), id); err != nil {
		t.Fatal(err)
	}
}

func verifiedServerEvidence(a *app.App, f analysis.Fix) analysis.Verification {
	target := f.Target
	if !strings.Contains(target, "://") {
		target = "https://" + target
	}
	v := analysis.Verification{Status: "verified", Summary: "Fixture artifact and scope checked.", Sources: []string{target}}
	for _, gate := range []string{"existing_work", "remaining_change", "capability", "strategy", "eligibility", "completion"} {
		source := target
		quote := "Fixture verified current artifact evidence."
		if gate == "capability" {
			source, quote = "brand.facts", a.Cfg.Brand.Facts[0] // a quoted fact must still be listed
		}
		if gate == "strategy" {
			source = "brand.advisory"
		}
		if gate == "completion" {
			source = "candidate.done_when"
			quote = f.DoneWhen
		}
		v.Checks = append(v.Checks, analysis.VerificationCheck{Gate: gate, Source: source, Quote: quote, Reason: "The fixture explicitly verifies this gate for a transport test."})
	}
	v.Context = analysis.VerificationContext(a.Cfg, f.Target, v)
	return v
}

func TestNextStepsKeepsLegacyWorkAsReadOnlyInvestigation(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	id := execute()
	a.Cfg.Brand.Advisory.Boundaries = []string{"Claude and OpenAI only"}
	// Historical arrays remain historical even when they contain an old implementation prompt.
	legacy := analysis.Fix{Rank: 1, Type: analysis.NewPage, Title: "Old suggested change", Target: "https://cheapseats.dev/existing", Claim: "Do this", DoneWhen: "More recommendations", Evidence: "Earlier answer", Prompts: []int{1}, Effort: "S", ClaudePrompt: "DO-NOT-IMPLEMENT-THIS"}
	body, _ := json.Marshal([]analysis.Fix{legacy})
	if _, err := db.Exec(`UPDATE runs SET fixes=? WHERE id=?`, string(body), id); err != nil {
		t.Fatal(err)
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(path string) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	for _, path := range []string{"/next-steps", "/quick-wins", "/runs/" + id} {
		text := get(path)
		for _, want := range []string{"Next steps", "Investigations", "Old suggested change", "Verification was not recorded"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
		for _, forbidden := range []string{"DO-NOT-IMPLEMENT-THIS", "Prompt for Claude Code", ">Record work<"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s exposes implementation: %q", path, forbidden)
			}
		}
	}
	if text := get("/api/quick-wins"); strings.TrimSpace(text) != "[]" {
		t.Errorf("legacy action API: %s", text)
	}
	for _, path := range []string{"/api/next-steps", "/api/runs/" + id, "/api/runs/" + id + "/fixes"} {
		if text := get(path); strings.Contains(text, "DO-NOT-IMPLEMENT-THIS") {
			t.Errorf("%s exposes a historical implementation prompt", path)
		}
	}
	var steps app.NextSteps
	if err := json.Unmarshal([]byte(get("/api/next-steps")), &steps); err != nil {
		t.Fatal(err)
	}
	if len(steps.Actions) != 0 || len(steps.Investigations) != 1 || steps.Investigations[0].Legacy == nil {
		t.Fatalf("categories: %+v", steps)
	}
	fd := s.frontDoor(context.Background())
	// The advisory is internal strategy; a shared or public MCP server must not hand it out.
	if js, _ := json.Marshal(fd); strings.Contains(string(js), "advisory") || strings.Contains(string(js), "Claude and OpenAI only") {
		t.Errorf("front door exposes the advisory: %s", js)
	}
	if st := stepTo(fd, "get_next_steps"); st == nil || st.Args["kind"] != "investigations" {
		t.Errorf("front door: %+v", fd.NextSteps)
	}
}

func TestNextStepsServesAllCategoriesAndActionOnlyCompatibility(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	id := execute()
	fixes, _, _ := a.Fixes(context.Background(), id)
	body := analysis.Encode(analysis.Analysis{Fixes: fixes,
		Investigations: []analysis.Investigation{{Title: "Check current inclusion", Why: "Unknown eligibility", Check: "Read submission rules", Decision: "Whether a draft is appropriate", Evidence: "Result 12", Prompts: []int{1}}},
		Findings:       []analysis.Finding{{Title: "Keep the engine boundary", Detail: "Broader coverage is outside the chosen scope.", Evidence: "Result 13", Prompts: []int{1}}}})
	if _, err := db.Exec(`UPDATE runs SET fixes=? WHERE id=?`, body, id); err != nil {
		t.Fatal(err)
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for path, want := range map[string]string{"/next-steps": "Actions 1", "/next-steps?view=investigations": "Check current inclusion", "/next-steps?view=market-findings": "Keep the engine boundary", "/runs/" + id: "Check current inclusion"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d lacks %q", path, w.Code, want)
		}
	}
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "next-steps-test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for name, wants := range map[string][]string{"get_next_steps": {"Check current inclusion", "Keep the engine boundary", "Write a credential-gateway guide"}, "get_quick_wins": {"Write a credential-gateway guide"}} {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name})
		if err != nil || r.IsError {
			t.Fatalf("%s: %v %+v", name, err, r)
		}
		text := ""
		for _, c := range r.Content {
			if x, ok := c.(*mcp.TextContent); ok {
				text += x.Text
			}
		}
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
		if name == "get_quick_wins" && strings.Contains(text, "Check current inclusion") {
			t.Error("compatibility tool includes investigations")
		}
	}
}

// Attempt history retains its original artifact but never republishes its coding handoff.
func TestHTTPAttemptResponsesOmitStaleImplementationPrompts(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	id := execute()
	fixes, _, err := a.Fixes(context.Background(), id)
	if err != nil || len(fixes) != 1 {
		t.Fatalf("fixes: %+v %v", fixes, err)
	}
	const stale = "DO-NOT-EXPOSE-STALE-HANDOFF"
	fixes[0].ClaudePrompt = stale
	if _, err := db.Exec(`UPDATE runs SET fixes=? WHERE id=?`, analysis.Encode(analysis.Analysis{Fixes: fixes}), id); err != nil {
		t.Fatal(err)
	}
	a.Cfg.Brand.Advisory.Boundaries = []string{"The previously verified work needs review"}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/runs/"+id+"/fixes/1/attempts", strings.NewReader(`{"url":"https://example.org/commit/1","state":"accepted","note":"Previously shipped"}`)))
	if w.Code != 200 || strings.Contains(w.Body.String(), stale) {
		t.Fatalf("mark response: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/attempts", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), stale) || !strings.Contains(w.Body.String(), "Previously shipped") {
		t.Fatalf("history response: %d %s", w.Code, w.Body.String())
	}
	a.Cfg.Brand.Advisory.Boundaries = nil
	for _, path := range []string{"/api/quick-wins?status=accepted", "/api/next-steps?status=accepted", "/api/runs/" + id} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || strings.Contains(w.Body.String(), stale) {
			t.Fatalf("nested history in %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	stored, err := a.Attempts(context.Background(), "")
	if err != nil || len(stored) != 1 || stored[0].Fix.ClaudePrompt != stale {
		t.Fatalf("original history changed: %+v %v", stored, err)
	}
}

// A new run's failed analysis was invisible where people look: the hub and the dashboard fell
// back to the older run's analysis without a word, /api/next-steps had no error field, and the
// run page printed the provider's raw JSON.
func TestFailedAnalysisShowsWhereTheAdviceIs(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	older, latest := execute(), execute()
	stored := `HTTP 400: {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 212000 tokens > 200000 maximum"},"request_id":"req_011FAKE"}`
	if _, err := db.Exec(`UPDATE runs SET created_at=CASE id WHEN ? THEN '2026-10-01T00:00:00Z' ELSE '2026-10-02T00:00:00Z' END, fixes=CASE id WHEN ? THEN fixes END, fixes_error=CASE id WHEN ? THEN ? END WHERE id IN (?, ?)`, older, older, latest, stored, older, latest); err != nil {
		t.Fatal(err)
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(path string) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	readable := "HTTP 400: prompt is too long: 212000 tokens &gt; 200000 maximum (request <span class=\"mono\">req_011FAKE</span>)"
	for _, path := range []string{"/next-steps", "/"} {
		page := get(path)
		for _, want := range []string{readable, "the analysis of the latest run, <a class=\"mono\" href=\"/runs/" + latest, "Showing the analysis from run <a class=\"mono\" href=\"/runs/" + older, "Write a credential-gateway guide"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
	}
	if page := get("/runs/" + latest); !strings.Contains(page, "Analysis failed: "+readable) || strings.Contains(page, "invalid_request_error") {
		t.Errorf("run page shows the provider's raw reply or no request id")
	}
	var steps app.NextSteps
	if err := json.Unmarshal([]byte(get("/api/next-steps")), &steps); err != nil {
		t.Fatal(err)
	}
	if len(steps.Errors) != 1 || steps.Errors[0].RunID != latest || steps.Errors[0].ShownRunID != older || steps.Errors[0].RequestID != "req_011FAKE" || steps.Errors[0].Message != "HTTP 400: prompt is too long: 212000 tokens > 200000 maximum" {
		t.Fatalf("analysis_errors: %+v", steps.Errors)
	}
	if text := get("/api/next-steps?set=another-set"); !strings.Contains(text, `"analysis_errors": []`) {
		t.Errorf("a set filter keeps other sets' errors: %s", text)
	}

	if steps.Errors[0].Shown != app.ShownOlderRun {
		t.Errorf("shown = %q, want %q", steps.Errors[0].Shown, app.ShownOlderRun)
	}

	// A failed rebuild of a run that already had an analysis keeps it. The surfaces said its
	// candidates were unverified investigations while they showed its earlier verified analysis.
	if _, err := db.Exec(`UPDATE runs SET fixes=(SELECT fixes FROM runs WHERE id=?), fixes_at='2026-10-02T01:00:00Z' WHERE id=?`, older, latest); err != nil {
		t.Fatal(err)
	}
	kept := "Showing the run&#39;s earlier analysis, which the failed rebuild left in place."
	for _, path := range []string{"/next-steps", "/"} {
		if page := get(path); !strings.Contains(page, kept) || strings.Contains(page, "unverified") {
			t.Errorf("%s doesn't say it shows the run's earlier analysis", path)
		}
	}
	if page := get("/runs/" + latest); !strings.Contains(page, "Showing this run's earlier analysis, from 2026-10-02 01:00:00 UTC, which the failed rebuild left in place.") {
		t.Error("run page doesn't say it shows its earlier analysis")
	}
	var fixes struct {
		Failed app.AnalysisError `json:"analysis_error"`
	}
	if err := json.Unmarshal([]byte(get("/api/runs/"+latest+"/fixes")), &fixes); err != nil || fixes.Failed.Shown != app.ShownEarlier || fixes.Failed.ShownRunID != latest {
		t.Errorf("/api/runs/{id}/fixes analysis_error: %+v %v", fixes.Failed, err)
	}

	// A first build whose verification failed stores its candidates, marked, with the error.
	unverified := analysis.Encode(analysis.Analysis{Insights: "One candidate.", VerificationFailed: true, Investigations: []analysis.Investigation{{
		Title: "Check: the gateway guide", Why: "w", Check: "Read the gateway guide.", Decision: "d", Evidence: "e", Prompts: []int{1},
	}}})
	if _, err := db.Exec(`UPDATE runs SET fixes=? WHERE id=?`, unverified, latest); err != nil {
		t.Fatal(err)
	}
	degraded := "Its candidates are shown as investigations, unverified: the run has no verified analysis to show instead."
	for _, path := range []string{"/next-steps", "/", "/runs/" + latest} {
		if page := get(path); !strings.Contains(page, degraded) || strings.Contains(page, "earlier analysis") {
			t.Errorf("%s doesn't say its investigations are the failed verification's", path)
		}
	}
	if err := json.Unmarshal([]byte(get("/api/next-steps")), &steps); err != nil || len(steps.Errors) != 1 || steps.Errors[0].Shown != app.ShownUnverified {
		t.Errorf("analysis_errors: %+v %v", steps.Errors, err)
	}
}

func TestReadAnalysisErrorKeepsTheRequestIDOfACutReply(t *testing.T) {
	// llm.APIError stores at most 400 characters of the reply, so a long one is cut mid-JSON.
	cut := `verification could not complete, so its 3 candidates are investigations: HTTP 529: {"type":"error","request_id":"req_CUT","error":{"type":"overloaded_error","message":"Overloaded, please try` + "…"
	got := app.ReadAnalysisError(cut)
	if got.RequestID != "req_CUT" || got.Message != "verification could not complete, so its 3 candidates are investigations: HTTP 529: Overloaded, please try…" {
		t.Errorf("%+v", got)
	}
	if got := app.ReadAnalysisError("analysis requires all results graded"); got.Message != "analysis requires all results graded" || got.RequestID != "" {
		t.Errorf("plain error changed: %+v", got)
	}
}
