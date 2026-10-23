package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// wait_for_run returns a finished run at once with its rates; errors on an unknown run; times out
// on a queued run nothing is running, with its progress and a note; and follows a run started
// through the app to the end, fix list included.
func TestWaitForRunOverMCP(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	finished := execute()
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
	wait := func(args map[string]any) RunWait {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "wait_for_run", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("wait_for_run %v: %v %+v", args, err, res)
		}
		var w RunWait
		b, _ := json.Marshal(res.StructuredContent)
		json.Unmarshal(b, &w)
		return w
	}

	w := wait(map[string]any{"run": finished})
	if !w.Finished || w.TimedOut || w.Status != "done" || w.Rates == nil || w.Progress != nil || w.WaitedSeconds > 2 || w.ID != finished {
		t.Errorf("finished run: %+v", w)
	}

	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "wait_for_run", Arguments: map[string]any{"run": "r-nope"}}); err != nil || !res.IsError {
		t.Errorf("unknown run should be a tool error: %v %+v", err, res)
	}

	old := waitPoll
	waitPoll = 50 * time.Millisecond
	defer func() { waitPoll = old }()
	set, _ := a.Set("ai-search-visibility")
	queued, err := a.Runs.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	w = wait(map[string]any{"run": queued.ID, "timeout_seconds": 1})
	if w.Finished || !w.TimedOut || w.Status != "queued" || w.Progress == nil || w.Progress.Stage != "answering" || !strings.Contains(w.Note, "nothing is running it") || w.Rates != nil {
		t.Errorf("queued run: %+v", w)
	}
	if d := time.Since(began); d < time.Second || d > 3*time.Second {
		t.Errorf("waited %v for a 1s timeout", d)
	}

	// The leftover blocks a new run of its set until it is out of the way.
	if _, err := db.Exec(`UPDATE runs SET status='cancelled' WHERE id=?`, queued.ID); err != nil {
		t.Fatal(err)
	}
	started, err := a.StartRun(ctx, "ai-search-visibility", "test")
	if err != nil {
		t.Fatal(err)
	}
	w = wait(map[string]any{"run": started.ID, "timeout_seconds": 60})
	if !w.Finished || w.TimedOut || w.Status != "done" || w.Rates == nil || w.Error != "" {
		t.Fatalf("started run: %+v", w)
	}
	var built bool
	db.QueryRow(`SELECT fixes IS NOT NULL FROM runs WHERE id=?`, started.ID).Scan(&built)
	if !built || a.RunProgress(ctx, started.ID) != nil {
		t.Errorf("finished before its fix list was built (built=%v)", built)
	}
}
