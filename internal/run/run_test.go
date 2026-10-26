package run

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// A fake Messages API: answers name Cheap Seats on the search track, the grader grades by whether
// the answer named it.
func fakeAnthropic(t *testing.T, calls *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		w.Header().Set("content-type", "application/json")
		if _, grading := req["output_config"]; grading {
			msg := llm.Str(llm.Map(llm.List(req["messages"])[0])["content"])
			g := `{"verdict":"absent","position":null,"top_pick":"Coder","named":["Coder"],"reasoning":"not named","why_not_top":"preferred Coder","objections":[]}`
			if strings.Contains(msg, "Use Cheap Seats") {
				g = `{"verdict":"recommended","position":1,"top_pick":"Cheap Seats","named":["Cheap Seats","Coder"],"reasoning":"top pick","why_not_top":null,"relevance":"relevant","accuracy":"not-assessed","understanding":"Workstation option","evidence":"Use Cheap Seats.","objections":["private beta"]}`
			}
			json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": g}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 100, "output_tokens": 50}})
			return
		}
		if _, search := req["tools"]; search {
			w.Write([]byte(`{"content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://docs.cheapseats.example/x","title":"x"}]},{"type":"text","text":"Use Cheap Seats.","citations":[{"url":"https://docs.cheapseats.example/x"}]}],"usage":{"input_tokens":1000,"output_tokens":200,"server_tool_use":{"web_search_requests":1}}}`))
			return
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"Use Coder."}],"usage":{"input_tokens":500,"output_tokens":100}}`))
	}))
}

func setup(t *testing.T, budget float64) (*Engine, sets.Set, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := fakeAnthropic(t, &calls)
	t.Cleanup(srv.Close)
	llm.AnthropicURL = srv.URL
	t.Setenv("ANTHROPIC_API_KEY", "test")
	t.Setenv("OPENAI_API_KEY", "")
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Brand:     config.Brand{Name: "Cheap Seats", Domains: []string{"cheapseats.example"}},
		Answerers: []config.Answerer{{ID: "a", Provider: "anthropic", Model: "m", Samples: config.Samples{Search: 2, Recall: 1}}, {ID: "o", Provider: "openai", Model: "g", Samples: config.Samples{Search: 1}}},
		Grader:    config.Model{Model: "g"},
		Budget:    config.Budget{DailyUSD: budget, Concurrency: 3},
		Pricing:   map[string]config.Pricing{"g": {Input: 1, Output: 1}, "m": {Input: 3, Output: 15, SearchPer1K: 10}},
	}
	cfg.Tiers.PLG.MaxSamples = 1
	set := sets.Set{ID: "s", Name: "S", Tier: sets.Core, Prompts: []sets.Prompt{{ID: "p1", Text: "p1"}, {ID: "p2", Text: "p2"}}, Version: "v"}
	return New(cfg, db), set, &calls
}

func TestRunEndToEnd(t *testing.T) {
	e, set, calls := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	r, err := e.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Get(ctx, r.ID)
	if got.Status != "done" {
		t.Fatalf("status %s %v", got.Status, got.Error)
	}
	res, _ := e.Results(ctx, r.ID)
	if len(res) != 6 { // 2 prompts × (2 search + 1 recall)
		t.Fatalf("%d results", len(res))
	}
	for _, x := range res {
		if x.Status != "done" || x.Grade == nil {
			t.Fatalf("%+v", x)
		}
		want := "absent"
		if x.Track == "search" {
			want = "recommended"
		}
		if string(x.Grade.Verdict) != want {
			t.Fatalf("%s: %s", x.Track, x.Grade.Verdict)
		}
	}
	if calls.Load() != 12 {
		t.Fatalf("%d API calls", calls.Load())
	}
	spent, _ := e.DB.SpentToday(ctx)
	if spent <= 0 {
		t.Fatal("no spend recorded")
	}
}

func TestBudgetStopsRun(t *testing.T) {
	e, set, _ := setup(t, 1.0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1] // room for one 0.60 reservation at a time
	e.Cfg.Budget.Concurrency = 1
	ctx := context.Background()
	r, _ := e.Create(ctx, set, "manual")
	// Pretend most of today's budget is already gone.
	e.DB.Exec(`INSERT INTO spend_events (created_at,kind,model,amount_usd,state) VALUES (?,'test','m',0.5,'settled')`, store.Now())
	e.Execute(ctx, r.ID)
	got, _ := e.Get(ctx, r.ID)
	if got.Status != "budget" {
		t.Fatalf("status %s", got.Status)
	}
	res, _ := e.Results(ctx, r.ID)
	skipped := 0
	for _, x := range res {
		if x.Status == "skipped" {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatal("expected skipped results")
	}
	// Retry picks the skipped ones back up once there's budget.
	e.Cfg.Budget.DailyUSD = 0
	e.Execute(ctx, r.ID)
	got, _ = e.Get(ctx, r.ID)
	if got.Status != "done" {
		t.Fatalf("after retry: %s", got.Status)
	}
}

// Runs recorded before brand.audience existed have no Audience key in their snapshot, so the
// design hash must not gain one either, or every earlier run drops out of the rankings.
func TestDesignIgnoresAudience(t *testing.T) {
	cfg := &config.Config{Brand: config.Brand{Name: "B", Audience: "a software developer"}}
	s := NewSnapshot(cfg, sets.Set{ID: "s", Prompts: []sets.Prompt{{ID: "q", Text: "q"}}})
	before := s
	before.Brand.Audience = ""
	b, _ := json.Marshal(before)
	if strings.Contains(string(b), "Audience") {
		t.Fatalf("an empty audience is in the snapshot: %s", b)
	}
	if Design(s) != Digest(func() Snapshot { x := before; x.Skipped, x.Brand.Facts = nil, nil; return x }()) {
		t.Fatal("the audience changed the design hash")
	}
}

// A CLI answerer's flags are part of how its answers were asked: changing them starts a new method
// and stops an older run resuming, while a run recorded before them keeps its stored method.
func TestMethodCoversCLIArgs(t *testing.T) {
	cfg := &config.Config{Brand: config.Brand{Name: "B", Aliases: []string{"b"}, About: "x"}}
	api := NewSnapshot(cfg, sets.Set{})
	b := api.Brand
	before := Digest(struct {
		AnswerPrompt, SearchPolicy, GraderPrompt, Grader string
		Name, About                                      string
		Aliases                                          []string
	}{api.AnswerPrompt, api.SearchPolicy, api.GraderPrompt, api.Grader, b.Name, b.About, b.Aliases})
	if api.CLIArgs != nil || Method(api) != before {
		t.Fatalf("a run without CLI answerers changed method: %v", api.CLIArgs)
	}

	cli := api
	cli.Answerers = []config.Answerer{{ID: "api", Provider: config.Anthropic}, {ID: "c", Provider: config.Codex}}
	cli.CLIArgs = cliArgs(cli.Answerers)
	if len(cli.CLIArgs) != 1 || !strings.Contains(cli.CLIArgs[config.Codex], `web_search=\"live\"`) {
		t.Fatalf("codex arguments not recorded: %v", cli.CLIArgs)
	}
	if !cli.Current(cfg) || Method(cli) == before {
		t.Fatal("recording codex arguments should give a current snapshot a new method")
	}
	old := cli
	old.CLIArgs = map[string]string{config.Codex: strings.Replace(cli.CLIArgs[config.Codex], "live", "cached", 1)}
	if Method(old) == Method(cli) || old.Current(cfg) {
		t.Fatal("a codex flag change should change the method and stop the old run resuming")
	}
}

// A deploy restarts Cheap Seats mid-run: the run records itself as interrupted rather than
// cancelled, and the next start resumes it, keeping every answer it already had.
func TestShutdownInterruptsAndTheNextStartResumes(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	e.Cfg.Budget.Concurrency = 1
	ctx := context.Background()
	r, err := e.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	// A provider that answers until the run has one result in, then holds every call until the
	// test lets go (or the engine hangs up): the shutdown finds the run midway, some answers kept
	// and the rest not, however slow the machine is.
	fast := llm.AnthropicURL
	midway, release := make(chan struct{}), make(chan struct{})
	var holding sync.Once
	stuck := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var done int
		e.DB.QueryRow(`SELECT COUNT(*) FROM results WHERE run_id=? AND status='done'`, r.ID).Scan(&done)
		if done > 0 {
			holding.Do(func() { close(midway) })
			select {
			case <-release:
			case <-req.Context().Done():
			}
			w.WriteHeader(502)
			return
		}
		out, _ := http.NewRequest(req.Method, fast, bytes.NewReader(body))
		out.Header = req.Header
		res, err := http.DefaultClient.Do(out)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer res.Body.Close()
		w.Header().Set("content-type", "application/json")
		io.Copy(w, res.Body)
	}))
	defer stuck.Close()
	defer close(release)
	llm.AnthropicURL = stuck.URL
	e.Start(r.ID)
	select {
	case <-midway:
	case <-time.After(10 * time.Second):
		t.Fatal("the run never finished its first answer")
	}
	e.Stop(5 * time.Second)
	if s := e.Status(ctx, r.ID); s != "queued" {
		t.Fatalf("after shutdown the run is %q, want queued to resume", s)
	}
	if _, err := e.Create(ctx, set, "manual"); err == nil {
		t.Fatal("a stopping engine started a new run")
	}
	var doneBefore int
	e.DB.QueryRow(`SELECT COUNT(*) FROM results WHERE run_id=? AND status='done'`, r.ID).Scan(&doneBefore)

	// The next start.
	llm.AnthropicURL = fast
	next := New(e.Cfg, e.DB)
	ids, err := next.Recover(ctx)
	if err != nil || len(ids) != 1 || ids[0] != r.ID {
		t.Fatalf("recover: %v %v", ids, err)
	}
	if err := next.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := next.Get(ctx, r.ID)
	var done, total int
	next.DB.QueryRow(`SELECT SUM(status='done'), COUNT(*) FROM results WHERE run_id=?`, r.ID).Scan(&done, &total)
	if got.Status != "done" || done != total || doneBefore == 0 || doneBefore == total {
		t.Fatalf("status %s, %d/%d done (%d before the restart)", got.Status, done, total, doneBefore)
	}
}

// Cancelling a run that's waiting to resume stops it before it starts.
func TestCancelAQueuedRun(t *testing.T) {
	e, set, _ := setup(t, 0)
	ctx := context.Background()
	r, _ := e.Create(ctx, set, "manual")
	if !e.Cancel(r.ID) || e.Status(ctx, r.ID) != "cancelled" {
		t.Fatalf("status %s", e.Status(ctx, r.ID))
	}
}

// A provider error on one answer doesn't leave the run partial: it gets one more try before the
// run finishes.
func TestFailedAnswersGetOneMoreTry(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	fake := llm.AnthropicURL
	var failed atomic.Bool
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "output_config") && failed.CompareAndSwap(false, true) {
			w.WriteHeader(400) // not retried by the client: the answer errors
			w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"flaky"}}`))
			return
		}
		res, err := http.Post(fake+r.URL.Path, "application/json", strings.NewReader(string(body)))
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer res.Body.Close()
		w.Header().Set("content-type", "application/json")
		io.Copy(w, res.Body)
	}))
	defer flaky.Close()
	llm.AnthropicURL = flaky.URL
	ctx := context.Background()
	r, _ := e.Create(ctx, set, "schedule")
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Get(ctx, r.ID)
	if !failed.Load() || got.Status != "done" {
		t.Fatalf("status %s after one failed answer (failed=%v), want done", got.Status, failed.Load())
	}
}

// A failed write of the run's own records stops the run as failed, saying why, instead of being
// dropped while the run carries on and ends "done" or "partial".
func TestStorageFailureFailsTheRun(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	r, err := e.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(`CREATE TRIGGER no_grades BEFORE UPDATE OF grade ON results WHEN NEW.grade IS NOT NULL BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	e.Execute(ctx, r.ID)
	got, _ := e.Get(ctx, r.ID)
	if got.Status != "failed" || got.Error == nil || !strings.Contains(*got.Error, "storage") || !strings.Contains(*got.Error, "disk full") {
		t.Fatalf("status %s, error %v", got.Status, got.Error)
	}
}

// A stored grade that doesn't decode is an error, not an answer that drops out of the rates.
func TestCorruptStoredJSONIsAnError(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	r, _ := e.Create(ctx, set, "manual")
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Results(ctx, r.ID); err != nil {
		t.Fatalf("clean results: %v", err)
	}
	// Unknown fields from a newer version are fine.
	e.DB.Exec(`UPDATE results SET grade = json_set(grade, '$.added_later', 1) WHERE run_id = ?`, r.ID)
	if _, err := e.Results(ctx, r.ID); err != nil {
		t.Fatalf("unknown fields: %v", err)
	}
	e.DB.Exec(`UPDATE results SET grade = '{"verdict": ' WHERE id = (SELECT MIN(id) FROM results WHERE run_id = ?)`, r.ID)
	if _, err := e.Results(ctx, r.ID); err == nil || !strings.Contains(err.Error(), "grade") {
		t.Fatalf("want a decoding error, got %v", err)
	}
}
