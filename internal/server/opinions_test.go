package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The second-opinion block, the per-answer line, the API field and the MCP digests appear only
// when grade_opinions has rows for the run; with none, every page is as it was. No [typesafe]
// in the config here: the read path never needs it.
func TestSecondOpinionsRenderOnlyWhenStored(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "x")
	t.Setenv("CHEAPSEATS_TOKENS", "")
	cfg := testConfig(t)
	db, _ := store.Open(t.TempDir())
	defer db.Close()
	a := newApp(t, cfg, db)
	set, _ := a.Set("ai-search-visibility")
	ctx := context.Background()
	r, err := a.Runs.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	// Two graded answers by hand: one recommended, one mentioned.
	res, _ := a.Runs.Results(ctx, r.ID)
	if len(res) < 2 {
		t.Fatal("results")
	}
	finish := func(id int64, text string, g grade.Grade) {
		gj, _ := json.Marshal(g)
		aj, _ := json.Marshal(answer.Answer{Text: text})
		if _, err := db.Exec(`UPDATE results SET status='done', grade=?, answer=?, verdict=?, finished_at=? WHERE id=?`, string(gj), string(aj), string(g.Verdict), store.Now(), id); err != nil {
			t.Fatal(err)
		}
	}
	finish(res[0].ID, "Cheap Seats is the one to start with. Profound is for enterprises.", grade.Grade{Verdict: grade.Recommended, Evidence: "Cheap Seats is the one to start with.", Named: []string{"Cheap Seats", "Profound"}})
	finish(res[1].ID, "Profound, Peec AI and Cheap Seats all track this.", grade.Grade{Verdict: grade.Mentioned, Evidence: "Profound, Peec AI and Cheap Seats all track this.", Named: []string{"Profound", "Peec AI", "Cheap Seats"}})
	db.Exec(`UPDATE runs SET status='done', finished_at=? WHERE id=?`, store.Now(), r.ID)
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
	ts := httptest.NewServer(h)
	defer ts.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) string {
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || out.IsError {
			t.Fatalf("%s: %v %v", name, err, out)
		}
		text := ""
		for _, c := range out.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		return text
	}

	// Nothing stored: no block, no field, no column.
	for path, absent := range map[string]string{
		"/runs/" + r.ID:     `id="second-opinion"`,
		"/api/runs/" + r.ID: `"second_opinion"`,
		"/sets/" + set.ID:   "Jev agrees",
	} {
		if body := get(path); strings.Contains(body, absent) {
			t.Errorf("%s with no opinions should not have %q", path, absent)
		}
	}
	if text := call("get_run", map[string]any{"run": r.ID}); strings.Contains(text, "second_opinion") {
		t.Error("get_run with no opinions carries second_opinion")
	}
	if text := call("get_result", map[string]any{"run": r.ID, "result": res[0].ID}); strings.Contains(text, "second_opinion") {
		t.Error("get_result with no opinions carries second_opinion")
	}

	// Two opinions: Jev agrees on the first and reads the second as recommended at 0.82.
	for _, row := range []struct {
		id              int64
		verdict, grader string
		p               float64
		ev              string
	}{{res[0].ID, "recommended", "recommended", 0.9, "Cheap Seats is the one to start with."}, {res[1].ID, "recommended", "mentioned", 0.82, "Profound, Peec AI and Cheap Seats all track this."}} {
		if _, err := db.Exec(`INSERT INTO grade_opinions(result_id, run_id, verdict, verdict_confidence, probabilities, evidence, evidence_confidence, grader_verdict, grader_evidence, model, cost_usd, decided_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			row.id, r.ID, row.verdict, row.p, `{"recommended":0.9,"mentioned":0.05,"absent":0.05}`, row.ev, 0.9, row.grader, row.ev, "jev-1.14.0", 0.0001, store.Now()); err != nil {
			t.Fatal(err)
		}
	}
	body := get("/runs/" + r.ID)
	for _, want := range []string{
		`<h2 id="second-opinion">Second opinion</h2>`,
		"Agreed 1, disagreed 1, unsure 0",
		"agreement 50% of the 2 it was sure about",
		`<a href="#result-` + itoa(res[1].ID) + `">`,
		`<span class="v-mentioned">mentioned</span></td><td><span class="v-recommended">recommended</span></td><td class="num">0.82</td>`,
		`Second opinion (Jev): <span class="v-recommended">recommended</span> at 0.90, agrees with the grader. Its evidence (the same): “Cheap Seats is the one to start with.”`,
		`at 0.82, disagrees with the grader.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("run page: want %q", want)
		}
	}
	api := get("/api/runs/" + r.ID)
	for _, want := range []string{`"second_opinion": {`, `"agreed": 1`, `"disagreed": 1`, `"rate": 0.5`, `"jev": "recommended"`, `"grader": "mentioned"`} {
		if !strings.Contains(api, want) {
			t.Errorf("api: want %q in %s", want, limit(api[strings.Index(api, "second_opinion")-1:]))
		}
	}
	if body := get("/sets/" + set.ID); !strings.Contains(body, "Jev agrees") || !strings.Contains(body, `<td class="num">50% <span class="muted">(1 differ)</span></td>`) {
		t.Errorf("set page: %s", limit(body[strings.Index(body, "<h2>Runs</h2>"):]))
	}
	if text := call("get_run", map[string]any{"run": r.ID}); !strings.Contains(text, `"second_opinion":{"agreed":1,"disagreed":1,"unsure":0,"evidence_differs":0,"rate":0.5,"disagreements":[{"result_id":`+itoa(res[1].ID)) {
		t.Errorf("get_run: %s", limit(text))
	}
	if text := call("get_result", map[string]any{"run": r.ID, "result": res[1].ID}); !strings.Contains(text, `"second_opinion":{"result_id":`+itoa(res[1].ID)+`,"verdict":"recommended","confidence":0.82`) || !strings.Contains(text, `"agreement":"disagrees","evidence_match":"same"`) {
		t.Errorf("get_result: %s", limit(text))
	}
	// The read path needs no [typesafe]: the default threshold applies.
	cfg.TypeSafe = nil
	if body := get("/runs/" + r.ID); !strings.Contains(body, "Agreed 1, disagreed 1, unsure 0") {
		t.Error("the read path needs no [typesafe]")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
