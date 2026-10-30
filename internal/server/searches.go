package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cheapseatsdev/cheapseats/internal/queries"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// poolRuns is how many of a set's runs, this one included, the searches page pools when asked.
const poolRuns = 7

// SearchData is the searches page: one run's searches, or its set's recent runs' pooled.
type SearchData struct {
	Run    *run.Run
	Pooled []run.Run // the runs read, newest first; just Run unless pooled
	Pool   bool
	PoolN  int // how many runs pooling reads
	queries.Report
}

// searchData reads a run's searches. With pool it reads the set's finished runs up to this one,
// asked and graded the same way (run.Method), so a change of method never mixes into the counts.
func (s *Server) searchData(ctx context.Context, id string, pool bool) (*SearchData, error) {
	r, err := s.App.Runs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &SearchData{Run: r, Pooled: []run.Run{*r}, Pool: pool, PoolN: poolRuns}
	if pool {
		runs, _ := s.App.Runs.List(ctx, r.SetID, 60)
		for _, x := range runs {
			if len(d.Pooled) == poolRuns {
				break
			}
			if x.ID != r.ID && x.Status == run.Done && x.Method == r.Method && x.CreatedAt <= r.CreatedAt {
				d.Pooled = append(d.Pooled, x)
			}
		}
	}
	var res []run.Result
	for _, x := range d.Pooled {
		rr, err := s.App.Runs.Results(ctx, x.ID)
		if err != nil {
			return nil, err
		}
		res = append(res, rr...)
	}
	taken := map[string]bool{}
	for _, st := range s.App.Sets() {
		for _, p := range st.Prompts {
			taken[p.ID] = true
		}
	}
	set := r.Snapshot.Set
	if cur, ok := s.App.Set(r.SetID); ok {
		set.File = cur.File
	}
	d.Report = queries.Build(queries.Input{Brand: s.App.RunBrand(r), Set: set, Results: res, Taken: taken})
	return d, nil
}

func (s *Server) searchesPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.searchData(r.Context(), r.PathValue("id"), r.URL.Query().Get("pool") != "")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "searches", d)
}

// setSearchesPage is the searches page for a set's latest finished run, pooled.
func (s *Server) setSearchesPage(w http.ResponseWriter, r *http.Request) {
	runs, _ := s.App.Runs.List(r.Context(), r.PathValue("id"), 30)
	for _, x := range runs {
		if x.Status == run.Done {
			http.Redirect(w, r, "/runs/"+x.ID+"/searches?pool=1", http.StatusSeeOther)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) apiSearches(w http.ResponseWriter, r *http.Request) {
	d, err := s.searchData(r.Context(), r.PathValue("id"), r.URL.Query().Get("pool") != "")
	if err != nil {
		httpErr(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, d)
}

// searchDigest is get_search_queries' answer: the headline numbers and the top of each list,
// small enough to read in one go. The JSON API has the rest.
func searchDigest(d *SearchData, limit int) any {
	if limit <= 0 {
		limit = 15
	}
	cut := func(n int) int { return min(n, limit) }
	type prompt struct {
		N          int             `json:"n"`
		Prompt     string          `json:"prompt"`
		Answers    int             `json:"answers"`
		Searched   int             `json:"searched"`
		PerAnswer  float64         `json:"searches_per_answer"`
		Distinct   int             `json:"distinct_queries"`
		Top        []queries.Count `json:"top_queries"`
		FromPrompt float64         `json:"from_prompt"`
		PromptKept float64         `json:"prompt_kept"`
	}
	type query struct {
		Query       string          `json:"query"`
		Prompts     []int           `json:"prompts"` // 1-based
		Answers     int             `json:"answers"`
		FirstParty  int             `json:"first_party_runs"`
		Competitors []queries.Count `json:"competitors,omitempty"`
		Named       string          `json:"brand_named"` // of graded answers that ran it
		Injected    []string        `json:"injected,omitempty"`
	}
	type suggest struct {
		Query  string `json:"query"`
		File   string `json:"file"`
		TOML   string `json:"toml,omitempty"`
		Review string `json:"review"`
	}
	ids := []string{}
	for _, x := range d.Pooled {
		ids = append(ids, x.ID)
	}
	prompts := []prompt{}
	for _, p := range d.Prompts {
		prompts = append(prompts, prompt{p.Idx + 1, p.Prompt, p.Answers, p.Searched, round2(p.PerAnswer), p.Distinct, p.Queries[:min(len(p.Queries), 3)], round2(p.FromPrompt), round2(p.PromptKept)})
	}
	qs := []query{}
	for _, q := range d.Queries[:cut(len(d.Queries))] {
		var n []int
		for _, i := range q.Prompts {
			n = append(n, i+1)
		}
		qs = append(qs, query{q.Query, n, q.Answers, q.FirstParty, q.Competitors, fmtFrac(q.Named, q.Graded), q.Injected})
	}
	sg := []suggest{}
	for _, x := range d.Suggested[:min(len(d.Suggested), 5)] {
		sg = append(sg, suggest{x.Query, x.File, x.TOML, x.Review})
	}
	return struct {
		Runs       []string            `json:"runs"`
		Answers    int                 `json:"answers"`
		Searched   int                 `json:"searched"`
		Searches   int                 `json:"searches"`
		Distinct   int                 `json:"distinct_queries"`
		FromPrompt float64             `json:"from_prompt"`
		PromptKept float64             `json:"prompt_kept"`
		Brief      string              `json:"brief"`
		Injected   []queries.Injection `json:"injected"`
		Added      []queries.Word      `json:"added_words"`
		Queries    []query             `json:"top_queries"`
		Prompts    []prompt            `json:"prompts"`
		Suggested  []suggest           `json:"add_as_prompt"`
	}{ids, d.Answers, d.Searched, d.Searches, d.Distinct, round2(d.FromPrompt), round2(d.PromptKept), d.Brief(),
		d.Injected[:cut(len(d.Injected))], d.Added[:cut(len(d.Added))], qs, prompts, sg}
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

func fmtFrac(a, b int) string {
	if b == 0 {
		return "–"
	}
	return fmt.Sprintf("%d/%d", a, b)
}
