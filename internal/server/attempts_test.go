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

	"github.com/metcalfc/popcorn/internal/app"
	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/store"
)

// A fix marked with its pull request leaves the open quick wins; the next run's fix for the same
// page is open again, as a retry, with the attempt, its outcome, and the analyst told about it.
func TestMarkingAFixAndItsRetry(t *testing.T) {
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
			text = `{"insights":"## Why Popcorn lost\nx","fixes":[{"rank":1,"type":"new-page","title":"Write a credential-gateway guide","target":"https://docs.popcorn.example/guides/keys-off-the-box/","claim":"how","done_when":"page live","evidence":"e (prompt 1)","prompts":[1],"effort":"S","displaces":null}]}`
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
	defer api.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = api.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("POPCORN_TOKENS")

	db, _ := store.Open(t.TempDir())
	defer db.Close()
	a := newApp(t, testConfig(t), db)
	set, _ := a.Set("ai-search-visibility")
	execute := func() string {
		r, err := a.Runs.Create(context.Background(), set, "manual")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Runs.Execute(context.Background(), r.ID); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
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
	// open, as a retry carrying the attempt and its outcome against the new run.
	second := execute()
	mu.Lock()
	last := analystSaw[len(analystSaw)-1]
	mu.Unlock()
	if !strings.Contains(last, "Already tried by the Popcorn team") || !strings.Contains(last, pr) {
		t.Errorf("the analyst wasn't told what was tried:\n%s", limit(last))
	}
	var open []app.QuickWin
	json.Unmarshal([]byte(get("/api/quick-wins?status=open")), &open)
	if len(open) != 1 || open[0].RunID != second || len(open[0].Earlier) != 1 || open[0].Earlier[0].URL != pr {
		t.Fatalf("the retry isn't open with its earlier attempt: %+v", open)
	}
	if o := open[0].Earlier[0].Outcome; o.RunID != second || len(o.Prompts) != 1 || o.Prompts[0].Before.Named == 0 || o.Status() != "unmoved" {
		t.Errorf("outcome: %+v %s", o, o.Status())
	}
	if body := get("/quick-wins"); !strings.Contains(body, `<span class="chip">retry</span>`) || !strings.Contains(body, "Tried before for this page") {
		t.Errorf("the retry isn't marked:\n%s", limit(body))
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
