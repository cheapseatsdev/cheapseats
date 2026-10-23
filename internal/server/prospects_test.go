package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// The run page, the API and get_run show a run's stored prospects, and nothing when it has none.
func TestRunShowsProspectsWhenStored(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "fake-review-provider-key-123456")
	cfg := testConfig(t)
	db, _ := store.Open(t.TempDir())
	a := newApp(t, cfg, db)
	set, _ := a.Set("ai-search-visibility")
	ctx := context.Background()
	r, err := a.Runs.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	// One graded answer citing a page, so the run has sources (prospects sit under them).
	gj, _ := json.Marshal(grade.Grade{Verdict: grade.Absent, Named: []string{"Profound"}})
	aj, _ := json.Marshal(answer.Answer{Text: "Profound leads.", Citations: []answer.Link{{URL: "https://blog.example.com/best-tools"}}})
	if _, err := db.Exec(`UPDATE results SET status='done', grade=?, answer=?, verdict='absent', finished_at=? WHERE run_id=? AND prompt_idx=0 AND track='search'`, string(gj), string(aj), store.Now(), r.ID); err != nil {
		t.Fatal(err)
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, limit(rec.Body.String()))
		}
		return rec.Body.String()
	}
	// With no rows the run renders as before, with no prospects at all.
	if page := get("/runs/" + r.ID); strings.Contains(page, "Prospects") {
		t.Error("no rows, no section")
	}
	if api := get("/api/runs/" + r.ID); strings.Contains(api, `"prospects"`) {
		t.Error("no rows, no field")
	}
	if d, err := s.runData(ctx, r.ID); err != nil {
		t.Fatal(err)
	} else if digest, _ := json.Marshal(runDigest(d)); strings.Contains(string(digest), `"prospects"`) {
		t.Error("no rows, no get_run field")
	}
	for _, row := range [][]any{
		{r.ID, "https://news.example.io/top-tools", "Top tools", "https://blog.example.com/best-tools", 0.9, 0, `["Profound"]`},
		{r.ID, "https://other.example.net/compare", "Compare", "https://blog.example.com/best-tools", 0.3, 0, `[]`},
		{r.ID, "https://quiet.example.org/list", "", "https://blog.example.com/best-tools", nil, nil, `[]`},
	} {
		if _, err := db.Exec(`INSERT INTO prospects(run_id, url, title, seed, in_category, names_brand, competitors, cost_usd, found_at) VALUES (?,?,?,?,?,?,?,0.004,?)`, append(row, store.Now())...); err != nil {
			t.Fatal(err)
		}
	}
	page := get("/runs/" + r.ID)
	for _, want := range []string{"<h3>Prospects</h3>", "Top tools", "news.example.io", "Profound", ">outreach<", "in category 90%", "not read"} {
		if !strings.Contains(page, want) {
			t.Errorf("run page should show %q", want)
		}
	}
	if strings.Contains(page, "other.example.net") {
		t.Error("the 0.3 page is below the threshold")
	}
	api := get("/api/runs/" + r.ID)
	if !strings.Contains(api, `"prospects"`) || !strings.Contains(api, `"outreach": true`) || strings.Contains(api, "other.example.net") {
		t.Errorf("api: %s", limit(api))
	}
	d, err := s.runData(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := json.Marshal(runDigest(d))
	if !strings.Contains(string(digest), `"prospects":[{"url":"https://news.example.io/top-tools","names":["Profound"],"why":"similar to https://blog.example.com/best-tools; names Profound and not the brand; in category 0.90"}`) {
		t.Errorf("get_run: %s", digest)
	}
}
