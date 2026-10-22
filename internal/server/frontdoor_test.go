package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/metcalfc/popcorn/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stepTo finds the first next step calling tool.
func stepTo(fd FrontDoor, tool string) *nextStep {
	for i := range fd.NextSteps {
		if fd.NextSteps[i].Tool == tool {
			return &fd.NextSteps[i]
		}
	}
	return nil
}

// The front door in each data state: what it says, and the calls it sends the client to.
func TestFrontDoor(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	fd := s.frontDoor(ctx)
	if fd.DataState != NoRuns || fd.Brand != "Popcorn" || len(fd.Sets) != len(a.Sets()) || !fd.Readiness.CanRun || fd.Audit != nil || len(fd.Prompts) != 3 {
		t.Fatalf("no runs: %+v", fd)
	}
	if st := stepTo(fd, "start_run"); st == nil || st.Args["dry_run"] != true || st.Args["set"] != a.Sets()[0].ID {
		t.Errorf("no runs should start with a dry run: %+v", fd.NextSteps)
	}
	if stepTo(fd, "audit_sites") == nil {
		t.Errorf("never audited should suggest audit_sites: %+v", fd.NextSteps)
	}

	// A run in progress.
	db.Exec(`INSERT INTO runs(id,set_id,set_version,name,trigger,status,snapshot,created_at) VALUES ('r-busy','ai-search-visibility','v','busy','manual','running','{}',?)`, store.Now())
	fd = s.frontDoor(ctx)
	if fd.DataState != Running {
		t.Fatalf("running: %s", fd.DataState)
	}
	if st := stepTo(fd, "get_run"); st == nil || st.Args["run"] != "r-busy" {
		t.Errorf("running should follow the run: %+v", fd.NextSteps)
	}
	db.Exec(`DELETE FROM runs WHERE id='r-busy'`)

	// Finished: ready, with the report, the urgent fix and the audit to read next.
	id := execute()
	fd = s.frontDoor(ctx)
	if fd.DataState != Ready || fd.OpenFixes != 1 || fd.UrgentFixes != 1 {
		t.Fatalf("ready: %s open %d urgent %d", fd.DataState, fd.OpenFixes, fd.UrgentFixes)
	}
	var set doorSet
	for _, x := range fd.Sets {
		if x.ID == "ai-search-visibility" {
			set = x
		}
	}
	if set.Status != "fresh" || set.LastFinished == nil || set.LastFinished.ID != id || set.LastFinished.Named == nil || set.LastRun.Status != "done" {
		t.Errorf("set: %+v", set)
	}
	if st := stepTo(fd, "get_run"); st == nil || st.Args["run"] != id {
		t.Errorf("ready should read the latest run: %+v", fd.NextSteps)
	}
	if st := stepTo(fd, "get_quick_wins"); st == nil || st.Args["priority"] != "urgent" || st.Args["status"] != "open" {
		t.Errorf("an urgent fix should be next: %+v", fd.NextSteps)
	}
	if stepTo(fd, "start_run") != nil {
		t.Errorf("nothing is due: %+v", fd.NextSteps)
	}
	// Work submitted on the fix waits for the set to run again.
	if _, err := a.MarkFix(ctx, id, 1, "https://github.com/acme/docs/pull/1", "proposed", "", "test"); err != nil {
		t.Fatal(err)
	}
	if fd = s.frontDoor(ctx); fd.AwaitingOutcome != 1 || fd.UrgentFixes != 0 || stepTo(fd, "list_attempts") == nil {
		t.Errorf("awaiting: %d urgent %d %+v", fd.AwaitingOutcome, fd.UrgentFixes, fd.NextSteps)
	}

	// Older than the set's schedule: stale, and a dry run of it comes first.
	old := time.Now().UTC().AddDate(0, 0, -5).Format(time.RFC3339)
	db.Exec(`UPDATE runs SET created_at=? WHERE id=?`, old, id)
	fd = s.frontDoor(ctx)
	if fd.DataState != Stale {
		t.Fatalf("stale: %s", fd.DataState)
	}
	if st := stepTo(fd, "start_run"); st == nil || st.Args["set"] != "ai-search-visibility" || st.Args["dry_run"] != true {
		t.Errorf("stale should dry-run the set: %+v", fd.NextSteps)
	}

	// Over MCP, small and an object; the prompts are listed and say which tools to call.
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_started"})
	if err != nil || res.IsError {
		t.Fatalf("get_started: %v %+v", err, res)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if len(b) > 8000 || !strings.Contains(string(b), `"data_state":"stale"`) || !strings.Contains(string(b), `"tool":"start_run"`) {
		t.Errorf("get_started (%d bytes): %s", len(b), limit(string(b)))
	}
	tools, _ := cs.ListTools(ctx, nil)
	for _, tl := range tools.Tools {
		if tl.Name == "get_started" && !strings.HasPrefix(tl.Description, "Call this first.") {
			t.Errorf("get_started should say to call it first: %s", tl.Description)
		}
		if tl.Name == "start_run" && !strings.Contains(tl.Description, "dry_run=true first") {
			t.Errorf("start_run should say to dry-run first: %s", tl.Description)
		}
	}
	prompts, err := cs.ListPrompts(ctx, nil)
	if err != nil || len(prompts.Prompts) != 3 {
		t.Fatalf("prompts: %+v %v", prompts, err)
	}
	for name, want := range map[string]string{"weekly_pulse": "get_rankings", "competitor_radar": "head_to_head", "why_not_recommended": "get_result"} {
		p, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: name, Arguments: map[string]string{"prompt": "best sandbox", "set": "ai-search-visibility"}})
		if err != nil || len(p.Messages) != 1 {
			t.Fatalf("%s: %v", name, err)
		}
		if text := p.Messages[0].Content.(*mcp.TextContent).Text; !strings.Contains(text, want) || !strings.Contains(text, "ai-search-visibility") {
			t.Errorf("%s: %s", name, text)
		}
	}
}

// Without the grader's provider, nothing can run: the front door says so first.
func TestFrontDoorCantRun(t *testing.T) {
	a, _, _, _ := fixFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	s, _ := New(a)
	fd := s.frontDoor(context.Background())
	if fd.Readiness.CanRun || fd.Readiness.Grader.Usable || len(fd.NextSteps) == 0 || fd.NextSteps[0].Tool != "" || stepTo(fd, "start_run") != nil {
		t.Errorf("can't run: %+v %+v", fd.Readiness, fd.NextSteps)
	}
}

// A dry run plans and prices a run, and creates nothing.
func TestDryRun(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	s, _ := New(a)
	h := s.Handler()
	ctx := context.Background()
	runs := func() int {
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n)
		return n
	}
	set, _ := a.Set("ai-search-visibility")
	p, err := a.DryRun(ctx, set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !p.DryRun || p.Answers != 2*len(set.Prompts) || len(p.Answerers) != 1 || p.Answerers[0].Search != len(set.Prompts) || p.Blocked != "" || runs() != 0 {
		t.Fatalf("plan: %+v (%d runs)", p, runs())
	}
	if !strings.Contains(p.Answerers[0].Basis, "reservation") || p.TotalUSD <= 0 || p.Remaining == nil || p.FitsBudget != (p.TotalUSD <= *p.Remaining) {
		t.Errorf("no history: %+v", p)
	}
	// After a run, from what its answers cost.
	execute()
	before := runs()
	if p, _ = a.DryRun(ctx, set.ID); !strings.HasPrefix(p.Answerers[0].Basis, "average of") {
		t.Errorf("with history: %+v", p.Answerers[0])
	}
	// The API and MCP take dry_run too, and start nothing.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/runs", strings.NewReader(`{"set":"ai-search-visibility","dry_run":true}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"dry_run": true`) || !strings.Contains(rec.Body.String(), `"est_total_usd"`) {
		t.Errorf("api: %d %s", rec.Code, rec.Body.String())
	}
	ts := httptest.NewServer(h)
	defer ts.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "start_run", Arguments: map[string]any{"set": set.ID, "dry_run": true}})
	if err != nil || res.IsError {
		t.Fatalf("start_run dry: %v %+v", err, res)
	}
	if b, _ := json.Marshal(res.StructuredContent); !strings.Contains(string(b), `"remaining_today_usd"`) {
		t.Errorf("start_run dry: %s", b)
	}
	if runs() != before {
		t.Errorf("a dry run created a run: %d, was %d", runs(), before)
	}
	// A set with a run in progress: the plan says start_run would refuse.
	db.Exec(`INSERT INTO runs(id,set_id,set_version,name,trigger,status,snapshot,created_at) VALUES ('r-busy',?,'v','busy','manual','running','{}',?)`, set.ID, store.Now())
	if p, _ = a.DryRun(ctx, set.ID); p.Running != "r-busy" || !strings.Contains(p.Blocked, "in progress") {
		t.Errorf("busy: %+v", p)
	}
}
