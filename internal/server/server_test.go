package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/metcalfc/popcorn/internal/app"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPagesRenderWithData(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		var text string
		switch {
		case strings.Contains(s, "sharp growth lead"):
			text = `{"insights":"## Why Popcorn lost\nDocker is GA.","fixes":[{"rank":1,"type":"new-page","title":"Write a credential-gateway guide","target":"docs.popcorn.example/guides/keys-off-the-box/","claim":"how","done_when":"page live","evidence":"Use Docker Sandboxes or Popcorn. (prompt 1)","prompts":[1],"effort":"S","displaces":"https://docs.docker.com/ai/sandboxes/security/"}]}`
		case strings.Contains(s, "output_config"):
			text = `{"verdict":"mentioned","position":2,"top_pick":"Docker Sandboxes","named":["Docker Sandboxes","Popcorn"],"reasoning":"r","why_not_top":"Docker is GA","objections":["private beta"]}`
		case strings.Contains(s, `"tools"`):
			w.Write([]byte(`{"content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://docs.docker.com/ai/sandboxes/security/","title":"x"}]},{"type":"text","text":"Use Docker Sandboxes or Popcorn.","citations":[{"url":"https://docs.docker.com/ai/sandboxes/security/"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		default:
			w.Write([]byte(`{"content":[{"type":"text","text":"Use Docker Sandboxes or Popcorn."}],"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer api.Close()
	llm.AnthropicURL = api.URL
	t.Setenv("ANTHROPIC_API_KEY", "fake-review-provider-key-123456")
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("POPCORN_TOKENS")

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
		"/runs/" + r.ID:                     "<h2>Why Popcorn lost</h2>",
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
	if err != nil || len(tools.Tools) != 11 {
		t.Fatalf("tools: %v %v", tools, err)
	}
	call := func(name string, args map[string]any) (string, bool) {
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
		return text, res.IsError
	}
	if text, isErr := call("get_run", map[string]any{"run": r.ID}); isErr || !strings.Contains(text, "Write a credential-gateway guide") || !strings.Contains(text, `"share_of_voice_rank":1`) {
		t.Errorf("get_run: %s", limit(text))
	}
	if text, isErr := call("get_citations", map[string]any{"url_contains": "docker"}); isErr || !strings.Contains(text, "docs.docker.com") {
		t.Errorf("get_citations: %s", limit(text))
	}
	if _, isErr := call("get_prompt_set", map[string]any{"set": "no-such-set"}); !isErr {
		t.Error("an unknown set should be a tool error")
	}
}
func TestFixListNeedsAFinishedRun(t *testing.T) {
	t.Setenv("POPCORN_TOKENS", "")
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
	t.Setenv("POPCORN_TOKENS", "")
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
	t.Setenv("POPCORN_TOKENS", "")
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
	t.Setenv("POPCORN_TOKENS", "a,b")
	s := &Server{tokens: []string{"a", "b"}, cookie: "popcorn_token_x"}
	h := s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	for _, c := range []struct {
		path, auth string
		code       int
	}{{"/api/runs", "", 401}, {"/api/runs", "Bearer b", 200}, {"/api/runs", "Bearer c", 401}, {"/", "", 303}, {"/login", "", 200},
		// Another deployment's cookie on the same host doesn't count, this one's does.
		{"/", "cookie popcorn_token_other=a", 303}, {"/", "cookie popcorn_token_x=a", 200}} {
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

// testConfig is the fixture popcorn.toml (with its prompt sets beside it) and one API answerer on
// a fake provider, so no test depends on the config repo, or ever reaches a real CLI or API.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../examples/popcorn-config/popcorn.toml")
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
	t.Setenv("POPCORN_TOKENS", "tok")
	t.Setenv("POPCORN_AUTH_HEADER", "X-ExeDev-Email")
	t.Setenv("POPCORN_ALLOWED_EMAILS", "alice@acme.com, @team.example")
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
	t.Setenv("POPCORN_TOKENS", "")
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
	t.Setenv("POPCORN_TOKENS", "")
	t.Setenv("POPCORN_AUTH_HEADER", "")
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	s, _ := New(newApp(t, testConfig(t), d))
	h := s.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/search", nil))
	if !strings.Contains(rec.Body.String(), `href="/static/popcorn.css?v=`+assetHashes["popcorn.css"]+`"`) || assetHashes["popcorn.css"] == "" {
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
	for path, want := range map[string]string{"/static/popcorn.css?v=x": "immutable", "/static/popcorn.css": "no-cache"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("cache-control"), want) {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Header().Get("cache-control"))
		}
	}
}
