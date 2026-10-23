package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The run page, the API and get_run with no baseline rows render as before, and with rows show
// the host table and the pages under each prompt, without any call to Exa.
func TestRunPageWithAndWithoutTheBaseline(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	id := execute()
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d\n%s", path, rec.Code, limit(rec.Body.String()))
		}
		return rec.Body.String()
	}
	ctx := context.Background()
	ts := httptest.NewServer(h)
	defer ts.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	getRun := func() string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_run", Arguments: map[string]any{"run": id}})
		if err != nil || res.IsError {
			t.Fatalf("get_run: %v %v", res, err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		return string(b)
	}

	// Without rows: no section, no key.
	if page := get("/runs/" + id); strings.Contains(page, "Neutral retrieval") || strings.Contains(page, "by a neutral index (Exa), no domain filter") {
		t.Error("with no rows the run page has no neutral retrieval")
	}
	if api := get("/api/runs/" + id); strings.Contains(api, `"baseline"`) {
		t.Error("with no rows the API has no baseline")
	}
	if digest := getRun(); strings.Contains(digest, "neutral_retrieval") {
		t.Error("with no rows get_run has no neutral_retrieval")
	}

	// With rows, written as the pass would, for three of the prompts: the answers' search returned
	// docs.docker.com for every prompt; the index ranks a competitor's page for the first two
	// prompts and the brand's docs for the first, and nothing for the third.
	first := `[{"title":"Profound on sandboxes","url":"https://tryprofound.com/sandboxes"},{"title":"Security","url":"https://cheapseats.dev/docs/security"}]`
	second := `[{"title":"Profound on keys","url":"https://tryprofound.com/keys"}]`
	if _, err := db.Exec(`INSERT INTO retrieval_baseline(run_id, prompt_idx, pages, cost_usd, checked_at) VALUES (?,0,?,0.004,?), (?,1,?,0.004,?), (?,2,'[]',0.004,?)`,
		id, first, store.Now(), id, second, store.Now(), id, store.Now()); err != nil {
		t.Fatal(err)
	}
	page := get("/runs/" + id)
	for _, want := range []string{
		`<h2 id="neutral">Neutral retrieval</h2>`,
		`over 3 prompts`,
		`<td class="mono">tryprofound.com</td><td>competitor · Profound</td><td class="num">2</td><td class="num">0</td>`,
		`<td class="mono">docs.docker.com</td><td>reference</td><td class="num">0</td><td class="num">3</td>`,
		`<tr class="us"><td class="mono">cheapseats.dev</td><td>own</td><td class="num">1</td><td class="num">0</td>`,
		`>content</span>`, `>query</span>`, `>both</span>`,
		`<a href="https://tryprofound.com/sandboxes">https://tryprofound.com/sandboxes</a> <span class="chip">competitor · Profound</span> <span class="chip" title="no search for this prompt returned it">unseen</span>`,
		`<a href="https://cheapseats.dev/docs/security">https://cheapseats.dev/docs/security</a> <span class="chip">ours</span>`,
		`none: the index returned nothing for this question`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("run page: want %s", want)
		}
	}
	api := get("/api/runs/" + id)
	for _, want := range []string{`"baseline": {`, `"prompt_count": 3`, `"neutral": 2`, `"reading": "content"`, `"reading": "query"`, `"reading": "both"`, `"status": "unseen"`, `"competitor": "Profound"`} {
		if !strings.Contains(api, want) {
			t.Errorf("api: want %s", want)
		}
	}
	// The digest's keys come back sorted; the rows keep their order, the clearest first.
	digest := getRun()
	for _, want := range []string{
		`"neutral_retrieval":[{"assistants":3,"class":"reference","host":"docs.docker.com","neutral":0,"reading":"query"},{"assistants":0,"class":"competitor","competitor":"Profound","host":"tryprofound.com","neutral":2,"reading":"content"}`,
		`{"assistants":0,"class":"own","host":"cheapseats.dev","neutral":1,"reading":"both"}`,
	} {
		if !strings.Contains(digest, want) {
			t.Errorf("get_run: want %s in %s", want, limit(digest))
		}
	}
}
