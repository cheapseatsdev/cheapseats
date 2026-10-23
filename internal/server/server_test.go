package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPagesRenderWithData(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		var text string
		switch {
		case strings.Contains(s, "sharp growth lead"):
			text = `{"insights":"## Why Cheap Seats lost\nDocker is GA.","fixes":[{"rank":1,"type":"new-page","title":"Write a credential-gateway guide","target":"docs.cheapseats.example/guides/keys-off-the-box/","claim":"how","done_when":"page live","evidence":"Use Docker Sandboxes or Cheap Seats. (prompt 1)","prompts":[1],"effort":"S","displaces":"https://docs.docker.com/ai/sandboxes/security/"}]}`
		case strings.Contains(s, "output_config"):
			text = `{"verdict":"mentioned","position":2,"top_pick":"Docker Sandboxes","named":["Docker Sandboxes","Cheap Seats"],"reasoning":"r","why_not_top":"Docker is GA","objections":["private beta"],"tone":"positive","praise":["open source"],"evidence":"Docker Sandboxes or Cheap Seats"}`
		case strings.Contains(s, `"tools"`):
			w.Write([]byte(`{"content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://docs.docker.com/ai/sandboxes/security/","title":"x"}]},{"type":"text","text":"Use Docker Sandboxes or Cheap Seats.","citations":[{"url":"https://docs.docker.com/ai/sandboxes/security/"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		default:
			w.Write([]byte(`{"content":[{"type":"text","text":"Use Docker Sandboxes or Cheap Seats."}],"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer api.Close()
	llm.AnthropicURL = api.URL
	t.Setenv("ANTHROPIC_API_KEY", "fake-review-provider-key-123456")
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("CHEAPSEATS_TOKENS")

	cfg := testConfig(t)
	dir := t.TempDir()
	db, _ := store.Open(dir)
	a := newApp(t, cfg, db)
	set, _ := a.Set("ai-search-visibility")
	r, err := a.Runs.Create(context.Background(), set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Runs.Execute(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for path, want := range map[string]string{
		"/":                                 "Write a credential-gateway guide",
		"/runs/" + r.ID:                     "<h2>Why Cheap Seats lost</h2>",
		"/runs/" + r.ID + "?opinion":        "&#43;100</b><small>net sentiment</small>",
		"/runs/" + r.ID + "?facts":          "None: no checked answer contradicts a listed fact", // the fact check ran after the run; the fake grader lists nothing
		"/rankings":                         "#1",
		"/quick-wins":                       "keys-off-the-box",
		"/api/runs/" + r.ID:                 `"verdict": "mentioned"`,
		"/api/rankings":                     `"rank": 1`,
		"/api/quick-wins?gap=documentation": "claude_prompt",
		"/api/quick-wins?site=elsewhere":    "keys-off-the-box", // the fake fix targets a page off the brand's sites
		"/api/quick-wins?site=code":         "[]",
		"/api/citations?urlContains=DOCKER": `"url": "https://docs.docker.com/ai/sandboxes/security"`,
		// One answerer, one sample on each of two tracks.
		"/api/citations?urlContains=nothing": fmt.Sprintf(`"answers_scanned": %d`, 2*len(set.Prompts)),
		"/quick-wins?gap=documentation":      `aria-current="true">documentation 1</a>`,
		"/quick-wins?gap=product":            `aria-current="true">product 0</a>`,
		"/sets/" + set.ID:                    `<td class="num">#1</td>`,
		"/sets/" + set.ID + "/regrade":       "Nothing to re-grade",
		"/search":                            "Search probe",
		"/runs/" + r.ID + "/export.csv":      "result_id,prompt_n,prompt",
		"/runs/" + r.ID + "/export.json":     `"verdict": "mentioned"`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d, want %q in:\n%s", path, rec.Code, want, limit(rec.Body.String()))
		}
	}

	// The run's measures beyond the rates: positions, head to head, objections and sources.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/runs/"+r.ID, nil))
	// The grader's evidence, highlighted in the answer: the answer's own words, escaped.
	for _, want := range []string{`<blockquote class="evidence">Docker Sandboxes or Cheap Seats</blockquote>`, `<pre>Use <mark title="the grader's evidence">Docker Sandboxes or Cheap Seats</mark>.</pre>`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("run page: want %q", want)
		}
	}
	for _, want := range []string{`<h2 id="h2h">Head to head</h2>`, "<td>private beta</td>", `<h2 id="sources">Sources</h2>`, `<td class="mono">docs.docker.com</td><td>reference</td>`, "Avg place", "<h3>Page types</h3>", "<tr><td>docs</td>"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("run page: want %q", want)
		}
	}

	// The same run over MCP, through a real client.
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 26 {
		t.Fatalf("tools: %v %v", tools, err)
	}
	call := func(name string, args map[string]any) (string, bool) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Clients on the 2025-06-18 spec reject a result whose structuredContent isn't an object.
		if res.StructuredContent != nil {
			if b, _ := json.Marshal(res.StructuredContent); len(b) == 0 || b[0] != '{' {
				t.Errorf("%s: structuredContent is not an object: %s", name, limit(string(b)))
			}
		}
		text := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		return text, res.IsError
	}
	if text, isErr := call("get_run", map[string]any{"run": r.ID}); isErr || !strings.Contains(text, "Write a credential-gateway guide") || !strings.Contains(text, `"share_of_voice_rank":1`) ||
		!strings.Contains(text, `"source_classes"`) || !strings.Contains(text, `"page_type":"docs"`) || !strings.Contains(text, `"objection":"private beta"`) || !strings.Contains(text, `"head_to_head"`) ||
		!strings.Contains(text, `"praise":[{"text":"open source","n":`) {
		t.Errorf("get_run: %s", limit(text))
	}
	// Kept small for MCP clients: no Claude prompts (get_quick_wins has them), no snapshot.
	if text, _ := call("get_run", map[string]any{"run": r.ID}); strings.Contains(text, `"claude_prompt":"`+"#") || strings.Contains(text, `"answerers":[{`) {
		t.Errorf("get_run carries the Claude prompts or the snapshot: %s", limit(text))
	}
	if text, isErr := call("get_rankings", map[string]any{}); isErr || !strings.Contains(text, `"board":[{"name":`) || strings.Contains(text, `"named_first"`) {
		t.Errorf("get_rankings: %s", limit(text))
	}
	if text, isErr := call("get_citations", map[string]any{"url_contains": "docker"}); isErr || !strings.Contains(text, "docs.docker.com") {
		t.Errorf("get_citations: %s", limit(text))
	}
	// After a change to what the grader reads, the set's re-grade page shows what it would do,
	// behind a form rather than a dialog. Then back as it was.
	cfg.Brand.About += " (changed)"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sets/"+set.ID+"/regrade?since=0", nil))
	if body := rec.Body.String(); !strings.Contains(body, `<form method="post" action="/sets/`+set.ID+`/regrade"><input type="hidden" name="since" value="0">`) ||
		!strings.Contains(body, fmt.Sprintf("Re-grade %d answers", 2*len(set.Prompts))) {
		t.Errorf("regrade page: %s", limit(body[strings.Index(body, "<main>"):]))
	}
	cfg.Brand.About = strings.TrimSuffix(cfg.Brand.About, " (changed)")
	// Re-grading is a dry run unless asked: nothing changed since this run, so nothing to do.
	if text, isErr := call("regrade", map[string]any{"target": set.ID}); isErr || !strings.Contains(text, `"current_runs":1`) || !strings.Contains(text, `"started":false`) {
		t.Errorf("regrade: %s", limit(text))
	}
	if _, isErr := call("regrade", map[string]any{"target": set.ID, "since_days": -1}); !isErr {
		t.Error("negative since_days should be a tool error")
	}
	if res, _ := a.Runs.Results(ctx, r.ID); len(res) > 0 {
		if text, isErr := call("get_result", map[string]any{"run": r.ID, "result": res[0].ID}); isErr || !strings.Contains(text, `"evidence":"Docker Sandboxes or Cheap Seats"`) {
			t.Errorf("get_result: %s", limit(text))
		}
	}
	// The tools that return lists.
	if text, isErr := call("list_prompt_sets", nil); isErr || !strings.HasPrefix(text, "[") {
		t.Errorf("list_prompt_sets: %s", limit(text))
	}
	if text, isErr := call("list_runs", nil); isErr || !strings.Contains(text, r.ID) {
		t.Errorf("list_runs: %s", limit(text))
	}
	if text, isErr := call("get_quick_wins", nil); isErr || !strings.Contains(text, "Write a credential-gateway guide") {
		t.Errorf("get_quick_wins: %s", limit(text))
	}
	if _, isErr := call("get_prompt_set", map[string]any{"set": "no-such-set"}); !isErr {
		t.Error("an unknown set should be a tool error")
	}

	// Objections Jev clustered (phrase_clusters, written by the cluster-phrases pass) count as
	// one row on the run page, marked with how many wordings, and in the API; no page calls Jev.
	if res, _ := a.Runs.Results(ctx, r.ID); len(res) > 0 {
		g := *res[0].Grade
		g.Objections = []string{"still in private beta"}
		gj, _ := json.Marshal(g)
		if _, err := db.Exec(`UPDATE results SET grade=? WHERE id=?`, string(gj), res[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO phrase_clusters(kind, phrase, cluster, display, confidence, source, decided_at) VALUES ('objection','still in private beta','private beta','private beta',0.9,'jev','2026-10-23T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		// Read through a fresh process: the clustering map, like the alias map, is cached per
		// process and rebuilt when the pass writes.
		s2, err := New(newApp(t, cfg, db))
		if err != nil {
			t.Fatal(err)
		}
		h2 := s2.Handler()
		rec = httptest.NewRecorder()
		h2.ServeHTTP(rec, httptest.NewRequest("GET", "/runs/"+r.ID, nil))
		if body := rec.Body.String(); !strings.Contains(body, `<td>private beta <span class="muted small" title="wordings Jev judged to say the same thing">(2 wordings)</span></td>`) || strings.Contains(body, "<td>still in private beta") {
			t.Errorf("run page should fold the wordings: %s", limit(body[strings.Index(body, `id="objections"`):]))
		}
		rec = httptest.NewRecorder()
		h2.ServeHTTP(rec, httptest.NewRequest("GET", "/api/runs/"+r.ID, nil))
		if body := rec.Body.String(); !strings.Contains(body, `"wordings": 2`) || strings.Contains(body, `"text": "still in private beta"`) {
			t.Errorf("api should carry wordings and count the sentiment through the clusters: %s", limit(body))
		}
	}
}
func TestFixListNeedsAFinishedRun(t *testing.T) {
	t.Setenv("CHEAPSEATS_TOKENS", "")
	cfg := testConfig(t)
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	s, err := New(newApp(t, cfg, d))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	d.Exec(`INSERT INTO runs(id,set_id,set_version,name,trigger,status,snapshot,created_at) VALUES ('partial','s','v','test','test','partial','{}',?)`, store.Now())
	// A set with a run in progress: the form goes to that run, the API says 409.
	t.Setenv("ANTHROPIC_API_KEY", "fake")
	d.Exec(`INSERT INTO runs(id,set_id,set_version,name,trigger,status,snapshot,created_at) VALUES ('busy','fix-ai-recommendations','v','test','test','running','{}',?)`, store.Now())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/sets/fix-ai-recommendations/run", nil))
	if rec.Code != 303 || rec.Header().Get("location") != "/runs/busy" {
		t.Fatalf("form: %d %s", rec.Code, rec.Header().Get("location"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/runs", strings.NewReader(`{"set":"fix-ai-recommendations"}`)))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"id":"busy"`) {
		t.Fatalf("api: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/runs/partial/fixes", "/runs/partial/fixes"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
		if rec.Code != 409 {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
	}
	if spent, _ := d.SpentToday(context.Background()); spent != 0 {
		t.Fatal("a refused fix list cost money")
	}
}

func TestLiveChangesWhenRunsDo(t *testing.T) {
	t.Setenv("CHEAPSEATS_TOKENS", "")
	cfg := testConfig(t)
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	s, err := New(newApp(t, cfg, d))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	live := func() app.Live {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/live", nil))
		var l app.Live
		if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil || rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return l
	}
	idle := live()
	if idle.Active || idle != live() {
		t.Fatalf("an idle server should be stable: %+v", idle)
	}
	d.Exec(`INSERT INTO runs(id,set_id,set_version,name,trigger,status,snapshot,created_at) VALUES ('r','s','v','test','test','running','{}',?)`, store.Now())
	running := live()
	if !running.Active || running.Version == idle.Version {
		t.Fatalf("a new running run should change the version: %+v", running)
	}
	d.Exec(`UPDATE runs SET status='done', finished_at=? WHERE id='r'`, store.Now())
	if done := live(); done.Active || done.Version == running.Version {
		t.Fatalf("finishing should change the version: %+v", done)
	}
}

func TestDashboardShowsProgress(t *testing.T) {
	t.Setenv("CHEAPSEATS_TOKENS", "")
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	s, err := New(newApp(t, testConfig(t), d))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	started := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	d.Exec(`INSERT INTO runs(id,set_id,set_version,name,trigger,status,snapshot,created_at,started_at) VALUES ('r1','fix-ai-recommendations','v','First workstation today','manual','running','{}',?,?)`, started, started)
	for i, x := range []struct{ answerer, status string }{{"claude-code-opus", "done"}, {"claude-code-opus", "done"}, {"claude-code-opus", "running"}, {"codex-sol", "done"}, {"codex-sol", "error"}, {"codex-sol", "pending"}} {
		d.Exec(`INSERT INTO results(run_id,prompt_idx,prompt,answerer,model,track,sample,status,finished_at) VALUES ('r1',?,'q',?,'m','search',0,?,?)`, i, x.answerer, x.status, store.Now())
	}
	get := func(path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	home := get("/")
	for _, want := range []string{"In progress", "<b>3</b> of 6 answers", "1 errors", "1 in flight", "1 waiting", "10m elapsed", "~5m left", "claude-code-opus", "2/3", "answering 3/6"} {
		if !strings.Contains(home, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	if !strings.Contains(get("/runs/r1"), "<b>3</b> of 6 answers") {
		t.Error("run page lacks progress")
	}
	if api := get("/api/progress"); !strings.Contains(api, `"done": 3`) || !strings.Contains(api, `"stage": "answering"`) {
		t.Errorf("api: %s", api)
	}
	d.Exec(`UPDATE runs SET status='done' WHERE id='r1'`)
	if strings.Contains(get("/"), "In progress") {
		t.Error("a finished run with no fix list being built is not in progress")
	}
}

func TestTokens(t *testing.T) {
	t.Setenv("CHEAPSEATS_TOKENS", "a,b")
	s := &Server{tokens: []string{"a", "b"}, cookie: "cheapseats_token_x"}
	h := s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	for _, c := range []struct {
		path, auth string
		code       int
	}{{"/api/runs", "", 401}, {"/api/runs", "Bearer b", 200}, {"/api/runs", "Bearer c", 401}, {"/", "", 303}, {"/login", "", 200},
		// Another deployment's cookie on the same host doesn't count, this one's does.
		{"/", "cookie cheapseats_token_other=a", 303}, {"/", "cookie cheapseats_token_x=a", 200}} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", c.path, nil)
		if v, ok := strings.CutPrefix(c.auth, "cookie "); ok {
			name, val, _ := strings.Cut(v, "=")
			req.AddCookie(&http.Cookie{Name: name, Value: val})
		} else if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != c.code {
			t.Errorf("%s %q: %d", c.path, c.auth, rec.Code)
		}
	}
}

// testConfig is the fixture cheapseats.toml (with its prompt sets beside it) and one API answerer on
// a fake provider, so no test depends on the config repo, or ever reaches a real CLI or API.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../examples/cheapseats-config/cheapseats.toml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Answerers = []config.Answerer{{ID: "claude", Provider: config.Anthropic, Model: "claude-opus-5-5", Samples: config.Samples{Search: 1, Recall: 1}}}
	return cfg
}

func newApp(t *testing.T, cfg *config.Config, db *store.DB) *app.App {
	t.Helper()
	a, err := app.New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func limit(s string) string {
	if len(s) > 1500 {
		return s[:1500]
	}
	return s
}

// Behind a proxy that signs people in (exe.dev, oauth2-proxy), its email header signs a request
// in; the allowlist narrows who; tokens keep working for API clients.
func TestProxyEmailHeader(t *testing.T) {
	t.Setenv("CHEAPSEATS_TOKENS", "tok")
	t.Setenv("CHEAPSEATS_AUTH_HEADER", "X-ExeDev-Email")
	t.Setenv("CHEAPSEATS_ALLOWED_EMAILS", "alice@acme.com, @team.example")
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	s, err := New(newApp(t, testConfig(t), d))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for _, c := range []struct {
		email, bearer, path string
		code                int
	}{
		{"alice@acme.com", "", "/", 200},
		{"ALICE@acme.com", "", "/api/status", 200},
		{"anyone@team.example", "", "/rankings", 200},
		{"eve@acme.com", "", "/", 403},
		{"eve@notteam.example", "", "/", 403},
		{"", "", "/api/status", 401},
		{"", "", "/", 303}, // tokens are configured too: the token sign-in page
		{"", "tok", "/api/status", 200},
		{"", "", "/healthz", 200},
	} {
		req := httptest.NewRequest("GET", c.path, nil)
		if c.email != "" {
			req.Header.Set("X-ExeDev-Email", c.email)
		}
		if c.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+c.bearer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.code {
			t.Errorf("%q %s: %d, want %d", c.email, c.path, rec.Code, c.code)
		}
	}
	// Without tokens, a request the proxy didn't sign in is told where to sign in.
	t.Setenv("CHEAPSEATS_TOKENS", "")
	s, _ = New(newApp(t, testConfig(t), d))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 401 {
		t.Errorf("unsigned request without tokens: %d", rec.Code)
	}
}

// Pages link assets by content hash, so a deploy that changes one changes its URL, and only
// versioned URLs are cached for long.
func TestAssetsAreVersioned(t *testing.T) {
	t.Setenv("CHEAPSEATS_TOKENS", "")
	t.Setenv("CHEAPSEATS_AUTH_HEADER", "")
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	s, _ := New(newApp(t, testConfig(t), d))
	h := s.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/search", nil))
	if !strings.Contains(rec.Body.String(), `href="/static/cheapseats.css?v=`+assetHashes["cheapseats.css"]+`"`) || assetHashes["cheapseats.css"] == "" {
		t.Fatalf("stylesheet not versioned:\n%s", limit(rec.Body.String()))
	}
	if !strings.Contains(rec.Body.String(), `<link rel="icon" type="image/svg+xml" href="/static/favicon.svg?v=`) {
		t.Error("no favicon")
	}
	fav := httptest.NewRecorder()
	h.ServeHTTP(fav, httptest.NewRequest("GET", "/static/favicon.svg", nil))
	if fav.Code != 200 || !strings.HasPrefix(fav.Header().Get("content-type"), "image/svg+xml") || !strings.Contains(fav.Body.String(), "<path") {
		t.Errorf("favicon: %d %q", fav.Code, fav.Header().Get("content-type"))
	}
	for path, want := range map[string]string{"/static/cheapseats.css?v=x": "immutable", "/static/cheapseats.css": "no-cache"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("cache-control"), want) {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Header().Get("cache-control"))
		}
	}
}

// Behind a proxy on the same machine, MCP requests arrive on loopback under the public host name:
// served when that name is in CHEAPSEATS_HOSTS, refused under any other (DNS rebinding).
func TestMCPBehindALocalProxy(t *testing.T) {
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	for _, c := range []struct {
		hosts []string
		host  string
		code  int
	}{
		{nil, "cheapseats.example.com", 403},
		{nil, "localhost:3000", 200},
		{[]string{"cheapseats.example.com"}, "cheapseats.example.com", 200},
		{[]string{"cheapseats.example.com"}, "CHEAPSEATS.example.com:443", 200},
		{[]string{"cheapseats.example.com"}, "attacker.example", 403},
		{[]string{"cheapseats.example.com"}, "127.0.0.1:3000", 200},
	} {
		s := &Server{App: &app.App{Cfg: &config.Config{Brand: config.Brand{Name: "Acme"}}}, hosts: c.hosts}
		ts := httptest.NewServer(s.mcpHandler())
		req, _ := http.NewRequest("POST", ts.URL, strings.NewReader(initialize))
		req.Host = c.host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		ts.Close()
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.code {
			t.Errorf("hosts %v, Host %q: %d, want %d", c.hosts, c.host, resp.StatusCode, c.code)
		}
	}
}

// The seat meter shows a grade, top pick included; anything ungraded keeps its status icon.
func TestSeatMeter(t *testing.T) {
	top, other := "Acme", "Gauge"
	for g, want := range map[*grade.Grade]string{
		{Verdict: grade.Recommended, TopPick: &top}:   "top-pick",
		{Verdict: grade.Recommended, TopPick: &other}: "recommended",
		{Verdict: grade.Mentioned}:                    "mentioned",
		{Verdict: grade.Absent}:                       "absent",
	} {
		if got := gradeKey(g, "acme"); got != want {
			t.Errorf("gradeKey(%v) = %q, want %q", g.Verdict, got, want)
		}
		if !strings.Contains(string(seats(want)), `class="seat-meter"`) {
			t.Errorf("no meter for %s", want)
		}
	}
	if strings.Contains(string(seats("error")), "seat-meter") || !strings.Contains(string(seats("error")), "icon-circle-x") {
		t.Error("an ungraded answer got a meter")
	}
}

// A read-only copy serves pages and the JSON API to anyone, and refuses every change and MCP.
func TestReadOnly(t *testing.T) {
	t.Setenv("CHEAPSEATS_TOKENS", "")
	t.Setenv("CHEAPSEATS_READONLY", "1")
	cfg := testConfig(t)
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	a := newApp(t, cfg, d)
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for _, c := range []struct {
		method, path string
		code         int
	}{
		{"GET", "/", 200}, {"GET", "/api/runs", 200}, {"GET", "/quick-wins", 200},
		{"GET", "/sets/ai-search-visibility/review", 200},
		{"POST", "/sets/fix-ai-recommendations/run", 403}, {"POST", "/api/runs", 403},
		{"POST", "/audit", 403}, {"DELETE", "/api/shares/x", 403}, {"POST", "/api/mcp", 403},
		{"GET", "/api/mcp", 403}, {"POST", "/login", 403},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader(`{"set":"fix-ai-recommendations"}`)))
		if rec.Code != c.code {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, rec.Code, c.code)
		}
		if c.path == "/" && (!strings.Contains(rec.Body.String(), `class="readonly"`) || !strings.Contains(rec.Body.String(), "read-only copy")) {
			t.Error("no read-only banner")
		}
	}
	// Reading the review page is a review on the real thing; a visitor to the copy isn't reviewing.
	if at := a.LastReview(context.Background(), "ai-search-visibility"); at != "" {
		t.Errorf("a visit to the read-only copy recorded a review at %q", at)
	}
	// A read-only copy is public, so a page that rebinds its name to loopback reads nothing new.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, onLoopback(httptest.NewRequest("GET", "/api/runs", nil), "evil.example"))
	if rec.Code != 200 {
		t.Errorf("read-only copy under a foreign name: %d", rec.Code)
	}
}

// onLoopback is a request as the server sees it arriving on 127.0.0.1 under the given Host.
func onLoopback(r *http.Request, host string) *http.Request {
	r.Host = host
	return r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8000}))
}

// What a page on another site can make a browser send to a local Cheap Seats is refused: a
// request under the page's own name, rebound to loopback, and a form or fetch from the page's
// origin that would change (or pay for) something. Neither reaches a provider.
func TestForeignRequestsRefused(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"content":[{"type":"text","text":"no"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer api.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = api.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake")
	t.Setenv("CHEAPSEATS_TOKENS", "")
	t.Setenv("CHEAPSEATS_AUTH_HEADER", "")
	t.Setenv("CHEAPSEATS_HOSTS", "seats.example")
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	s, err := New(newApp(t, testConfig(t), d))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	// Rebinding: the name the request came in under, on loopback.
	for host, code := range map[string]int{"evil.example": 403, "evil.example:8000": 403, "localhost:8000": 200, "127.0.0.1:8000": 200, "[::1]:8000": 200, "seats.example": 200, "SEATS.example:443": 200} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, onLoopback(httptest.NewRequest("GET", "/api/sets", nil), host))
		if rec.Code != code {
			t.Errorf("GET /api/sets as %s: %d, want %d", host, rec.Code, code)
		}
	}
	// Not on loopback (a container's address, say): the Host is whatever the proxy sent.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/sets", nil))
	if rec.Code != 200 {
		t.Errorf("GET /api/sets off loopback: %d", rec.Code)
	}
	// Forgery: a POST from another origin, a sandboxed page (Origin: null), or a browser that
	// says the site relation without an Origin. The page's own forms, and clients that aren't
	// browsers (no Origin), get through.
	for _, c := range []struct {
		origin, site string
		code         int
	}{
		{"https://evil.example", "", 403}, {"null", "", 403}, {"", "cross-site", 403}, {"", "same-site", 403},
		{"http://localhost:8000", "", 200}, {"http://LOCALHOST:8000", "", 200}, {"https://seats.example", "cross-site", 200},
		{"http://localhost:9000", "", 403}, {"http://127.0.0.1:8000", "", 403}, // another local app's page
		{"", "", 200}, {"", "same-origin", 200}, {"", "none", 200},
	} {
		calls = 0
		req := httptest.NewRequest("POST", "/search", strings.NewReader("query=best+credential+gateway"))
		req.Host = "localhost:8000"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if c.site != "" {
			req.Header.Set("Sec-Fetch-Site", c.site)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.code || (c.code == 403) == (calls > 0) {
			t.Errorf("POST /search from %q (%s): %d, want %d; %d provider calls", c.origin, c.site, rec.Code, c.code, calls)
		}
	}
	// A GET is a read, whatever its origin.
	req := httptest.NewRequest("GET", "/api/sets", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("cross-origin GET: %d", rec.Code)
	}
	// Behind a signing-in proxy the proxy's session would sign a forged form in: refused there too.
	t.Setenv("CHEAPSEATS_AUTH_HEADER", "X-ExeDev-Email")
	s, _ = New(newApp(t, testConfig(t), d))
	req = httptest.NewRequest("POST", "/search", strings.NewReader("query=x"))
	req.Host = "seats.example"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-ExeDev-Email", "alice@acme.com")
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("forged form behind the proxy: %d", rec.Code)
	}
}

// Text that starts like a formula is marked as text for the spreadsheet; other text is as it was.
func TestCSVCell(t *testing.T) {
	for in, want := range map[string]string{
		"=HYPERLINK(\"https://evil.example\")": "'=HYPERLINK(\"https://evil.example\")",
		"+1 for Cheap Seats":                   "'+1 for Cheap Seats",
		"- Cheap Seats\n- Docker":              "'- Cheap Seats\n- Docker",
		"@acme":                                "'@acme",
		"\tcmd":                                "'\tcmd",
		"Cheap Seats is cheap":                 "Cheap Seats is cheap",
		"":                                     "",
	} {
		if got := cell(in); got != want {
			t.Errorf("cell(%q) = %q, want %q", in, got, want)
		}
	}
}
