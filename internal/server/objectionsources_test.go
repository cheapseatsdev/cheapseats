package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// The pages behind an objection reach the API, get_run (cut to two), the run page and the
// analyst's prompt. One objection is in three search answers citing the brand's pricing page (all
// three), a review page and a forum thread (one each), and in one recall answer.
func TestObjectionSourcesReachEverySurface(t *testing.T) {
	var mu sync.Mutex
	var analystSaw string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if strings.Contains(string(body), "evidence-led product and market analyst") {
			analystSaw = string(body)
		}
		mu.Unlock()
		text := `{"insights":"x","fixes":[]}`
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	t.Cleanup(api.Close)
	old := llm.AnthropicURL
	llm.AnthropicURL = api.URL
	t.Cleanup(func() { llm.AnthropicURL = old })
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("CHEAPSEATS_TOKENS")

	db, _ := store.Open(t.TempDir())
	t.Cleanup(func() { db.Close() })
	a := newApp(t, testConfig(t), db)
	set, _ := a.Set("ai-search-visibility")
	ctx := context.Background()
	r, err := a.Runs.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	const own, review, forum = "https://cheapseats.dev/pricing", "https://www.g2.com/products/cheap-seats/reviews", "https://www.reddit.com/r/seo/comments/abc/pricing"
	update := func(where string, g grade.Grade, ans answer.Answer, args ...any) {
		gj, _ := json.Marshal(g)
		aj, _ := json.Marshal(ans)
		if _, err := db.Exec(`UPDATE results SET status='done', grade=?, answer=?, verdict=?, finished_at=? WHERE run_id=? AND `+where,
			append([]any{string(gj), string(aj), string(g.Verdict), store.Now(), r.ID}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	citing := func(urls ...string) answer.Answer {
		a := answer.Answer{Text: "x"}
		for _, u := range urls {
			a.Citations = append(a.Citations, answer.Link{URL: u})
		}
		return a
	}
	objecting := grade.Grade{Verdict: grade.Mentioned, Named: []string{"Cheap Seats"}, Objections: []string{"Pricing unclear"}, Praise: []string{"Open source"}}
	update("1=1", grade.Grade{Verdict: grade.Absent}, answer.Answer{Text: "x"})
	update("prompt_idx=0 AND track='search'", objecting, citing(own, review))
	update("prompt_idx=1 AND track='search'", objecting, citing(own, forum))
	update("prompt_idx=2 AND track='search'", objecting, citing(own))
	update("prompt_idx=0 AND track='recall'", objecting, answer.Answer{Text: "x"})
	if _, err := db.Exec(`UPDATE runs SET status='done' WHERE id=?`, r.ID); err != nil {
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
	type source struct {
		URL     string `json:"url"`
		Host    string `json:"host"`
		Type    string `json:"page_type"`
		Owned   bool   `json:"owned"`
		Answers int    `json:"answers"`
	}
	type row struct {
		Sources []source `json:"sources"`
		Recall  *int     `json:"recall_answers"`
	}
	var got struct {
		Summary struct {
			Objections []row `json:"objections"`
		}
		Opinion struct {
			Sentiment struct{ Praise, Objections []row } `json:"sentiment"`
		} `json:"opinion"`
	}
	if err := json.Unmarshal([]byte(get("/api/runs/"+r.ID)), &got); err != nil {
		t.Fatal(err)
	}
	want := []source{{own, "cheapseats.dev", "", true, 3}, {review, "g2.com", "", false, 1}, {forum, "reddit.com", "", false, 1}} // ties by URL
	check := func(what string, rows []row, n int) {
		t.Helper()
		if len(rows) != 1 || rows[0].Recall == nil || *rows[0].Recall != 1 || len(rows[0].Sources) != n {
			t.Fatalf("%s: %+v", what, rows)
		}
		for i, src := range rows[0].Sources {
			w := want[i]
			if src.URL != w.URL || src.Host != w.Host || src.Owned != w.Owned || src.Answers != w.Answers || src.Type == "" {
				t.Errorf("%s source %d = %+v, want %+v", what, i, src, w)
			}
		}
	}
	check("api objections", got.Summary.Objections, 3)
	check("api sentiment objections", got.Opinion.Sentiment.Objections, 3)
	check("api praise", got.Opinion.Sentiment.Praise, 3)

	// get_run carries the top two.
	d, err := s.runData(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	dj, _ := json.Marshal(runDigest(d))
	var digest struct {
		Objections []row `json:"top_objections"`
		Opinion    struct {
			Sentiment struct{ Praise []row } `json:"sentiment"`
		} `json:"opinion"`
	}
	json.Unmarshal(dj, &digest)
	check("get_run objections", digest.Objections, 2)
	check("get_run praise", digest.Opinion.Sentiment.Praise, 2)
	if len(d.Summary.Objections[0].Sources) != 3 {
		t.Error("trimming get_run must not cut the run's own rows")
	}

	page := get("/runs/" + r.ID)
	for _, w := range []string{"Pages behind it", `<a class="mono" href="` + own + `"`, "owned · ×3", "earned · ×1", "1 from memory"} {
		if !strings.Contains(page, w) {
			t.Errorf("run page lacks %q", w)
		}
	}

	if err := a.BuildFixes(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(analystSaw, "- pricing unclear (4 answers): "+own+" (owned, ") || !strings.Contains(analystSaw, "cited by 3); 1 from memory") {
		t.Errorf("the analyst's prompt lacks the page behind the objection:\n%s", limit(analystSaw))
	}
}
