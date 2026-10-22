package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metcalfc/popcorn/internal/app"
	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fixFixture is an app over a fake API whose analyst always proposes one urgent fix, the same
// guide (7 of 15 prompts, so urgent), with execute to run ai-search-visibility to the end and analyst to read what the analyst
// was sent each time.
func fixFixture(t *testing.T) (a *app.App, db *store.DB, execute func() string, analyst func() []string) {
	t.Helper()
	var mu sync.Mutex
	var analystSaw []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		var text string
		switch {
		case strings.Contains(s, "sharp growth lead"):
			mu.Lock()
			analystSaw = append(analystSaw, s)
			mu.Unlock()
			text = `{"insights":"## Why Popcorn lost\nx","fixes":[{"rank":1,"type":"new-page","title":"Write a credential-gateway guide","target":"https://docs.popcorn.example/guides/keys-off-the-box/","claim":"how","done_when":"page live","evidence":"e (prompt 1)","prompts":[1,2,3,4,5,6,7],"effort":"S","displaces":null}]}`
		case strings.Contains(s, "output_config"):
			text = `{"verdict":"mentioned","position":2,"top_pick":"Docker Sandboxes","named":["Docker Sandboxes","Popcorn"],"reasoning":"r","why_not_top":"w","objections":[]}`
		case strings.Contains(s, `"tools"`): // the search track must search first
			w.Write([]byte(`{"content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://docs.docker.com/ai/sandboxes/security/","title":"x"}]},{"type":"text","text":"Use Docker Sandboxes or Popcorn."}],"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		default:
			w.Write([]byte(`{"content":[{"type":"text","text":"Use Docker Sandboxes or Popcorn."}],"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	t.Cleanup(api.Close)
	old := llm.AnthropicURL
	llm.AnthropicURL = api.URL
	t.Cleanup(func() { llm.AnthropicURL = old })
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("POPCORN_TOKENS")

	db, _ = store.Open(t.TempDir())
	t.Cleanup(func() { db.Close() })
	a = newApp(t, testConfig(t), db)
	set, _ := a.Set("ai-search-visibility")
	execute = func() string {
		r, err := a.Runs.Create(context.Background(), set, "manual")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Runs.Execute(context.Background(), r.ID); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	analyst = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), analystSaw...)
	}
	return a, db, execute, analyst
}

// A fix marked with its pull request leaves the open quick wins; the next run's fix for the same
// page is open again, as a retry, with the attempt, its outcome, and the analyst told about it.
func TestMarkingAFixAndItsRetry(t *testing.T) {
	a, db, execute, analyst := fixFixture(t)
	first := execute()
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		req.Header.Set("content-type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if !strings.Contains(get("/api/quick-wins?status=open"), "keys-off-the-box") {
		t.Fatal("a fix nobody has touched is open")
	}

	// A link is required, and a form can't send anyone to another site.
	if rec := post("/runs/"+first+"/fixes/1/attempts", url.Values{"url": {"not a link"}}); rec.Code != 400 {
		t.Errorf("no link: %d", rec.Code)
	}
	pr := "https://github.com/metcalfc/popcorn-site/pull/9"
	rec := post("/runs/"+first+"/fixes/1/attempts", url.Values{"url": {pr}, "note": {"the guide"}, "back": {"//evil.example/"}})
	if rec.Code != 303 || rec.Header().Get("location") != "/runs/"+first+"#fix-1" {
		t.Fatalf("mark: %d %s", rec.Code, rec.Header().Get("location"))
	}
	if body := get("/api/quick-wins?status=open"); body != "[]\n" {
		t.Errorf("a proposed fix left in the open list:\n%s", limit(body))
	}
	if body := get("/api/quick-wins?status=proposed"); !strings.Contains(body, pr) {
		t.Errorf("proposed list lacks the attempt:\n%s", limit(body))
	}
	if body := get("/quick-wins?status=proposed"); !strings.Contains(body, `class="chip st-proposed">proposed`) || !strings.Contains(body, "pending: the set hasn") {
		t.Errorf("quick wins page doesn't show the attempt:\n%s", limit(body))
	}
	var attempts []app.Attempt
	json.Unmarshal([]byte(get("/api/attempts")), &attempts)
	if len(attempts) != 1 || attempts[0].State != app.Proposed || attempts[0].By != "web" || attempts[0].Outcome.RunID != "" {
		t.Fatalf("attempts: %+v", attempts)
	}

	// The set runs again: the analyst hears about the attempt, and its new fix for the same page is
	// open, as a retry carrying the attempt and its outcome. Timestamps are to the second, so the
	// attempt is dated well before the new fix list to keep the order certain (and old enough to
	// be read). One run after it is two answers a prompt, too few to read.
	db.Exec(`UPDATE fix_attempts SET created_at='2000-01-01T00:00:00Z'`)
	second := execute()
	json.Unmarshal([]byte(get("/api/attempts")), &attempts)
	if o := attempts[0].Outcome; o.RunID != second || o.Status() != app.OutcomeTooEarly || !strings.Contains(get("/api/attempts"), `"too early"`) {
		t.Errorf("one run of two answers a prompt should be too early: %+v %s", o, o.Status())
	}
	// A second run pools with the first: four answers a prompt. Every answer is the same, so
	// nothing changed, within a margin.
	third := execute()
	saw := analyst()
	last := saw[len(saw)-1]
	if !strings.Contains(last, "Already tried by the Popcorn team") || !strings.Contains(last, pr) {
		t.Errorf("the analyst wasn't told what was tried:\n%s", limit(last))
	}
	var open []app.QuickWin
	json.Unmarshal([]byte(get("/api/quick-wins?status=open")), &open)
	if len(open) != 1 || open[0].RunID != third || len(open[0].Earlier) != 1 || open[0].Earlier[0].URL != pr {
		t.Fatalf("the retry isn't open with its earlier attempt: %+v", open)
	}
	if o := open[0].Earlier[0].Outcome; o.RunID != third || o.AfterRuns != 2 || o.BeforeRuns != 1 || len(o.Prompts) != 7 || o.Prompts[0].Before.Named == 0 ||
		o.Prompts[0].After.N != 4 || o.Status() != app.OutcomeNoChange || !strings.HasPrefix(o.Summary(), "no detectable change (±") {
		t.Errorf("outcome: %+v %s", o, o.Summary())
	}
	if !strings.Contains(saw[len(saw)-1], "no detectable change (±") {
		t.Errorf("the analyst should see the outcome with its margin:\n%s", limit(saw[len(saw)-1]))
	}
	if body := get("/quick-wins"); !strings.Contains(body, `<span class="chip">retry</span>`) || !strings.Contains(body, "Tried before for this page") {
		t.Errorf("the retry isn't marked:\n%s", limit(body))
	}

	// Work on the same page recorded after a fix list was built covers that list's fix too: the
	// list didn't know about it. Here it's recorded on the first run's fix, after the second's list.
	db.Exec(`UPDATE runs SET fixes_at='2001-01-01T00:00:00Z' WHERE id=?`, third)
	pr2 := "https://github.com/metcalfc/popcorn-site/pull/10"
	if rec := post("/runs/"+first+"/fixes/1/attempts", url.Values{"url": {pr2}, "state": {"accepted"}}); rec.Code != 303 {
		t.Fatalf("second mark: %d", rec.Code)
	}
	if body := get("/api/quick-wins?status=open"); body != "[]\n" {
		t.Errorf("work the fix list didn't know about should cover it:\n%s", limit(body))
	}
	if body := get("/api/quick-wins?status=accepted"); !strings.Contains(body, third) {
		t.Errorf("the covered fix should be accepted:\n%s", limit(body))
	}
	if body := get("/runs/" + third); !strings.Contains(body, `class="chip st-accepted">accepted`) {
		t.Errorf("run page doesn't show the covered fix as accepted:\n%s", limit(body))
	}
	// The trend marks the work, on the day it was proposed and accepted, linking to it; any rate
	// and window can be drawn. The runs are spread over three days to make a trend.
	for i, id := range []string{first, second} {
		db.Exec(`UPDATE runs SET created_at=? WHERE id=?`, time.Now().UTC().AddDate(0, 0, i-2).Format(time.RFC3339), id)
	}
	if body := get("/rankings?metric=recommended&window=14"); !strings.Contains(body, `class="fixmark accepted"`) || !strings.Contains(body, `href="`+pr2+`"`) ||
		!strings.Contains(body, "recommended, 14-day") || !strings.Contains(body, "14-day named") {
		t.Errorf("rankings page:\n%s", limit(body))
	}
	var rk []app.Ranking
	json.Unmarshal([]byte(get("/api/rankings?window=28")), &rk)
	if len(rk) == 0 || rk[0].Days[0].WindowDays != 28 || rk[0].Days[0].Compare == nil || !rk[0].Days[0].Compare.WithinNoise || len(rk[0].Attempts) == 0 {
		t.Errorf("api rankings: %+v", rk)
	}
	// Each prompt pooled over the window, with its interval.
	if body := get("/sets/ai-search-visibility?window=28"); !strings.Contains(body, `Named <span class="muted">28d</span>`) || !strings.Contains(body, "100% (") {
		t.Errorf("set page:\n%s", limit(body))
	}

	// Accepted, then withdrawn; an unknown state is refused.
	for state, code := range map[string]int{"accepted": 200, "withdrawn": 200, "merged": 400} {
		req := httptest.NewRequest("POST", "/api/attempts/1", strings.NewReader(`{"state":"`+state+`"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != code {
			t.Errorf("%s: %d %s", state, rec.Code, rec.Body.String())
		}
	}
}

// The loop Claude Code runs over MCP: find the open urgent fixes, do one, mark it with its pull
// request, accept it when it merges, and find the next.
func TestTheFixLoopOverMCP(t *testing.T) {
	a, _, execute, _ := fixFixture(t)
	runID := execute()
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
	call := func(name string, args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		if res.IsError {
			t.Fatalf("%s: %s", name, text)
		}
		return text
	}
	open := map[string]any{"status": "open", "priority": "urgent", "brief": true, "limit": 1}

	// Identify: brief leaves out the long Claude prompt; run_id and rank are what mark_fix takes.
	var todo []app.QuickWin
	json.Unmarshal([]byte(call("get_quick_wins", open)), &todo)
	if len(todo) != 1 || todo[0].RunID != runID || todo[0].Rank != 1 || todo[0].ClaudePrompt != "" || todo[0].Status != app.Open {
		t.Fatalf("identify: %+v", todo)
	}
	if full := call("get_quick_wins", map[string]any{"limit": 1}); !strings.Contains(full, "claude_prompt") || !strings.Contains(full, "## Your task") {
		t.Errorf("without brief the prompt should be there: %s", limit(full))
	}

	// Mark: proposed with the pull request; it leaves the open list.
	pr := "https://github.com/metcalfc/popcorn-site/pull/11"
	var at app.Attempt
	marked := call("mark_fix", map[string]any{"run": todo[0].RunID, "rank": todo[0].Rank, "url": pr, "note": "the guide"})
	json.Unmarshal([]byte(marked), &at)
	if at.ID == 0 || at.State != app.Proposed || at.By != "mcp" || at.URL != pr {
		t.Fatalf("mark: %+v", at)
	}
	if got := call("get_quick_wins", open); got != "[]" {
		t.Errorf("a marked fix is still open: %s", limit(got))
	}
	// The loop's results stay small: no Claude prompt in an attempt's copy of its fix, nested or not.
	proposed := call("get_quick_wins", map[string]any{"status": "proposed", "brief": true})
	for name, text := range map[string]string{"mark_fix": marked, "brief get_quick_wins": proposed} {
		if strings.Contains(text, "## Your task") {
			t.Errorf("%s carries a Claude prompt: %s", name, limit(text))
		}
	}

	// Accept when it merges; list_attempts shows it.
	call("set_attempt_state", map[string]any{"id": at.ID, "state": "accepted"})
	var all []app.Attempt
	listed := call("list_attempts", map[string]any{"set": "ai-search-visibility"})
	if strings.Contains(listed, "## Your task") {
		t.Errorf("list_attempts carries a Claude prompt: %s", limit(listed))
	}
	json.Unmarshal([]byte(listed), &all)
	if len(all) != 1 || all[0].State != app.Accepted || all[0].Fix.Title != "Write a credential-gateway guide" {
		t.Fatalf("list: %+v", all)
	}

	// Mistakes come back as tool errors the caller can read, not as success.
	res, _ := cs.CallTool(ctx, &mcp.CallToolParams{Name: "mark_fix", Arguments: map[string]any{"run": runID, "rank": 9, "url": pr}})
	if !res.IsError {
		t.Error("a fix that doesn't exist should be a tool error")
	}
	res, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_attempt_state", Arguments: map[string]any{"id": at.ID, "state": "merged"}})
	if !res.IsError {
		t.Error("an unknown state should be a tool error")
	}
}

func TestTargetKey(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.Popcorn.zone/blog/x/?utm=1#top":                        "popcorn.zone/blog/x",
		"popcorn.zone/blog/x":                                               "popcorn.zone/blog/x",
		"github.com/metcalfc/popcorn (README.md) and popcorn.zone homepage": "github.com/metcalfc/popcorn",
		"https://popcorn.zone/":                                             "popcorn.zone",
	} {
		if got := app.TargetKey(in); got != want {
			t.Errorf("TargetKey(%q) = %q, want %q", in, got, want)
		}
	}
}
