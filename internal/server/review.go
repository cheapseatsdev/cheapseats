package server

import (
	"net/http"
	"strconv"

	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/suggest"
)

// reviewDays reads ?days=, ReviewWindows[0] when absent. Anything else not a window is refused.
func reviewDays(v string) (int, error) {
	if v == "" {
		return app.ReviewWindows[0], nil
	}
	n, _ := strconv.Atoi(v)
	return n, app.ReviewDays(n)
}

// ReviewData is the prompt review page.
type ReviewData struct {
	app.Review
	Windows []filterOption
	// The analyst's proposed edits, after the button; Err when it failed.
	Edits *suggest.Edits
	Err   string
	// Whether the analyst runs on a subscription (the button costs nothing).
	Subscription bool
	// The minimum data for a prompt to be reviewed.
	MinRuns, MinAnswers int
	// When the set's prompts were last marked reviewed, "" if never.
	LastReview string
}

func (s *Server) reviewData(r *http.Request) (*ReviewData, int, error) {
	id := r.PathValue("id")
	if _, ok := s.App.Set(id); !ok {
		return nil, http.StatusNotFound, errNoSet(id)
	}
	days, err := reviewDays(r.FormValue("days"))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	rev, err := s.App.Review(r.Context(), id, days)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	d := &ReviewData{Review: rev, Subscription: s.App.Cfg.Analyst.Via() == config.ClaudeCode,
		MinRuns: app.ReviewMinRuns, MinAnswers: app.ReviewMinAnswers, LastReview: s.App.LastReview(r.Context(), id)}
	for _, w := range app.ReviewWindows {
		d.Windows = append(d.Windows, filterOption{Label: strconv.Itoa(w) + " days", On: w == days, Href: "?days=" + strconv.Itoa(w)})
	}
	return d, 0, nil
}

func (s *Server) reviewPage(w http.ResponseWriter, r *http.Request) {
	d, code, err := s.reviewData(r)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	s.render(w, "review", d)
}

// reviewedForm is the page's "Mark reviewed" button: it records the review, which quiets
// get_started's monthly reminder. Opening the page records nothing, so a link from another site
// can't (a GET isn't checked for forgery; a POST is).
func (s *Server) reviewedForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.App.Set(id); !ok {
		http.Error(w, errNoSet(id).Error(), http.StatusNotFound)
		return
	}
	days, err := reviewDays(r.FormValue("days"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.App.MarkReviewed(r.Context(), id)
	http.Redirect(w, r, "/sets/"+id+"/review?days="+strconv.Itoa(days), http.StatusSeeOther)
}

// reviewSuggestForm is the page's button: one analyst call for edits, shown on the page. Nothing
// is written.
func (s *Server) reviewSuggestForm(w http.ResponseWriter, r *http.Request) {
	d, code, err := s.reviewData(r)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	if e, err := s.App.SuggestReview(r.Context(), d.Review); err != nil {
		d.Err = err.Error()
	} else {
		d.Edits = &e
	}
	s.render(w, "review", d)
}

func (s *Server) apiReview(w http.ResponseWriter, r *http.Request) {
	d, code, err := s.reviewData(r)
	if err != nil {
		httpErr(w, err, code)
		return
	}
	writeJSON(w, d.Review)
}

// reviewTop is how many items of each flag review_prompts lists.
const reviewTop = 5

// reviewDigest is review_prompts' answer: the counts, the top items of each flag and the gaps,
// numbers rounded. The JSON API has every prompt's numbers.
func reviewDigest(rev app.Review, edits *suggest.Edits) any {
	type item struct {
		Prompt     string  `json:"prompt"`
		Text       string  `json:"text"`
		Variant    string  `json:"variant,omitempty"`
		Other      string  `json:"other,omitempty"`
		Similarity float64 `json:"similarity,omitempty"`
		Numbers    string  `json:"numbers"`
		Suggestion string  `json:"suggestion"`
	}
	type flag struct {
		Count int    `json:"count"`
		Top   []item `json:"top"`
	}
	type gap struct {
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Numbers    string `json:"numbers"`
		TOML       string `json:"toml,omitempty"`
		Competitor string `json:"competitor_toml,omitempty"`
	}
	flags := map[string]*flag{}
	for _, k := range app.ReviewKinds {
		flags[k.Kind] = &flag{Top: []item{}}
	}
	for _, f := range rev.Findings {
		fl := flags[f.Kind]
		fl.Count++
		if len(fl.Top) < reviewTop {
			fl.Top = append(fl.Top, item{f.Prompt, f.Text, f.Variant, f.Other, round2(f.Similarity), f.Numbers, f.Suggestion})
		}
	}
	gaps := []gap{}
	for _, g := range rev.Gaps[:min(len(rev.Gaps), 2*reviewTop)] {
		gaps = append(gaps, gap{g.Kind, g.Name, g.Numbers, g.TOML, g.Competitor})
	}
	out := map[string]any{
		"set": rev.SetID, "days": rev.Days, "since": rev.Since, "runs": rev.Runs, "answers": rev.Answers,
		"prompts": len(rev.Prompts), "healthy": rev.Healthy, "not_enough_data": rev.NotEnough,
		"minimum": map[string]int{"runs": app.ReviewMinRuns, "answers": app.ReviewMinAnswers},
		"flags":   flags, "gaps": gaps,
	}
	if rev.Jev != nil {
		out["jev"] = rev.Jev
	}
	if edits != nil {
		out["suggested"] = map[string]any{"diff": edits.Diff, "rewrites": len(edits.Rewrites), "retire": len(edits.Retire), "add": len(edits.Add), "dropped": edits.Dropped, "cost_usd": round2(edits.CostUSD)}
	}
	return out
}

type errNoSet string

func (e errNoSet) Error() string { return "no prompt set " + strconv.Quote(string(e)) }
