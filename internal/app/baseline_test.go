package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
)

// The baseline against the run's answers. The answers (patched here) retrieved g2.com for both
// prompts and never a competitor page; the neutral index ranks the competitor for both prompts
// and never g2.com. So the competitor reads as content (the web agrees), g2.com as query (only
// the assistants' searches find it), and the hosts both found once each as both.
func TestCheckBaselineStoresTheWebsPagesAndReadsThemAgainstTheAnswers(t *testing.T) {
	a, id := providersApp(t)
	ctx := context.Background()
	setAnswer := func(idx int, ans answer.Answer) {
		t.Helper()
		js, _ := json.Marshal(ans)
		if _, err := a.DB.Exec(`UPDATE results SET answer=? WHERE run_id=? AND prompt_idx=? AND track='search'`, string(js), id, idx); err != nil {
			t.Fatal(err)
		}
	}
	setAnswer(0, answer.Answer{Text: "Scrunch AI is a strong pick.",
		Searches: []answer.Query{{Query: "q", Completed: true, Results: []answer.Link{{URL: "https://www.g2.com/categories/ai-visibility"}}}}})
	setAnswer(1, answer.Answer{Text: "Peec AI measures share of voice.",
		Citations: []answer.Link{{URL: "https://cheapseats.dev/docs/metrics/"}, {URL: "https://reviews.example.com/share-of-voice/"}},
		Searches: []answer.Query{{Query: "q", Completed: true, Results: []answer.Link{
			{URL: "https://cheapseats.dev/docs/metrics/"}, {URL: "https://cheapseats.dev/other"}, {URL: "https://g2.com/compare/sov"},
			{URL: "https://roundup.example.net/sov-tools/"}, {URL: "https://reviews.example.com/share-of-voice/"}}}}})

	var queries []string
	var filtered []string
	var counts []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		q := req["query"].(string)
		queries = append(queries, q)
		counts = append(counts, req["numResults"].(float64))
		for _, k := range []string{"includeDomains", "excludeDomains"} {
			if _, ok := req[k]; ok {
				filtered = append(filtered, k)
			}
		}
		var results []any
		if strings.Contains(q, "visibility tool") {
			results = []any{
				map[string]any{"title": "Best AI visibility tools", "url": "https://docs.tryprofound.com/blog/best-ai-visibility-tools"},
				map[string]any{"title": "Metrics", "url": "https://cheapseats.dev/docs/metrics"},
			}
		} else {
			results = []any{
				map[string]any{"title": "Share of voice", "url": "https://docs.tryprofound.com/learn/share-of-voice"},
				map[string]any{"title": "Measuring share of voice", "url": "https://reviews.example.com/share-of-voice"},
				map[string]any{"title": "SoV tools", "url": "https://roundup.example.net/sov-tools"},
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results, "costDollars": map[string]any{"total": 0.004}})
	}))
	defer srv.Close()
	t.Setenv(exa.KeyVar, "k")
	t.Setenv(config.ExaBaseURLVar, srv.URL)

	p, ok := PassByCommand("baseline")
	if !ok || p.Name != "baseline" || p.Order != 25 || p.Provider != config.ExaProvider || !p.On(a.Cfg) {
		t.Fatalf("the pass: %+v", p)
	}
	res, _ := a.Runs.Results(ctx, id)
	if b := a.Baseline(ctx, id, res); b != nil {
		t.Fatalf("before the pass there is nothing to read: %+v", b)
	}

	n, err := a.CheckBaseline(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(queries) != 2 || queries[0] != "What's the best AI visibility tool?" || queries[1] != "How do I measure share of voice?" {
		t.Fatalf("searched %d: %v", n, queries)
	}
	if len(filtered) != 0 {
		t.Errorf("a neutral search has no domain filter: %v", filtered)
	}
	if counts[0] != 10 {
		t.Errorf("baseline_results defaults to 10: %v", counts)
	}
	var stored int
	a.DB.QueryRow(`SELECT COUNT(*) FROM retrieval_baseline WHERE run_id=?`, id).Scan(&stored)
	if stored != 2 {
		t.Errorf("stored rows %d", stored)
	}

	b := a.Baseline(ctx, id, res)
	if b == nil || len(b.Prompts) != 2 || b.PromptCount != 2 {
		t.Fatalf("baseline %+v", b)
	}
	want := map[int][]BaselinePage{
		0: {{URL: "https://docs.tryprofound.com/blog/best-ai-visibility-tools", Title: "Best AI visibility tools", Status: "unseen", Class: "competitor", Competitor: "Profound"},
			{URL: "https://cheapseats.dev/docs/metrics", Title: "Metrics", Status: "unseen", Class: "own"}},
		1: {{URL: "https://docs.tryprofound.com/learn/share-of-voice", Title: "Share of voice", Status: "unseen", Class: "competitor", Competitor: "Profound"},
			{URL: "https://reviews.example.com/share-of-voice", Title: "Measuring share of voice", Status: "cited", Class: "editorial"},
			{URL: "https://roundup.example.net/sov-tools", Title: "SoV tools", Status: "retrieved", Class: "editorial"}},
	}
	for idx, pages := range want {
		got := b.Prompts[idx]
		if got == nil || got.CostUSD != 0.004 {
			t.Fatalf("prompt %d: %+v", idx, got)
		}
		if fmt.Sprint(got.Pages) != fmt.Sprint(pages) {
			t.Errorf("prompt %d pages:\n got %+v\nwant %+v", idx, got.Pages, pages)
		}
	}
	// The host table: the competitor's domains as one row named for its first configured domain
	// (its pages were all on docs.tryprofound.com), the brand's sites as one, and no row for a
	// host one answer retrieved once (cheapseats.dev/other is ours; nothing else is alone).
	rows := map[string]BaselineHost{}
	for _, h := range b.Hosts {
		rows[h.Host] = h
	}
	for host, w := range map[string]BaselineHost{
		"tryprofound.com":     {Host: "tryprofound.com", Class: "competitor", Competitor: "Profound", Neutral: 2, Assistants: 0, Reading: ReadingContent},
		"g2.com":              {Host: "g2.com", Class: "review", Neutral: 0, Assistants: 2, Reading: ReadingQuery},
		"cheapseats.dev":      {Host: "cheapseats.dev", Class: "own", Neutral: 1, Assistants: 1, Reading: ReadingBoth},
		"reviews.example.com": {Host: "reviews.example.com", Class: "editorial", Neutral: 1, Assistants: 1, Reading: ReadingBoth},
		"roundup.example.net": {Host: "roundup.example.net", Class: "editorial", Neutral: 1, Assistants: 1, Reading: ReadingBoth},
	} {
		if rows[host] != w {
			t.Errorf("%s:\n got %+v\nwant %+v", host, rows[host], w)
		}
	}
	if len(b.Hosts) != 5 || b.Hosts[0].Host != "tryprofound.com" || b.Hosts[1].Host != "g2.com" {
		t.Errorf("the clearest rows first (the larger count, then neutral): %+v", b.Hosts)
	}

	// Searched prompts aren't searched again.
	if n, err := a.CheckBaseline(ctx, id); err != nil || n != 0 || len(queries) != 2 {
		t.Errorf("second pass searched %d (%v)", n, err)
	}
	var spent float64
	a.DB.QueryRow(`SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE kind LIKE 'provider-attempt/baseline/%'`).Scan(&spent)
	if fmt.Sprintf("%.3f", spent) != "0.008" {
		t.Errorf("two searches settled in the ledger: %f", spent)
	}
}

func TestBaselineReading(t *testing.T) {
	for _, c := range []struct {
		neutral, assistants int
		want                string
	}{
		{2, 0, ReadingContent}, {3, 3, ReadingContent}, {2, 1, ReadingContent},
		{0, 2, ReadingQuery}, {0, 5, ReadingQuery},
		{1, 0, ReadingBoth}, {1, 1, ReadingBoth}, {1, 2, ReadingBoth}, {2, 3, ReadingBoth}, {0, 1, ReadingBoth},
	} {
		if got := reading(c.neutral, c.assistants); got != c.want {
			t.Errorf("neutral %d, assistants %d: %s, want %s", c.neutral, c.assistants, got, c.want)
		}
	}
}
