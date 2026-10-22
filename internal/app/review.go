package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/grade"
	"github.com/metcalfc/popcorn/internal/queries"
	"github.com/metcalfc/popcorn/internal/report"
	"github.com/metcalfc/popcorn/internal/run"
	"github.com/metcalfc/popcorn/internal/sets"
	"github.com/metcalfc/popcorn/internal/store"
)

// The prompt review reads a set's stored answers over a window for what they say about the prompts
// themselves: which ones never move, which ones move for no reason, which ones are answered about
// something else, which ones ask the same thing twice, which ones the assistants search for in other
// words, and what the set doesn't ask about. Everything comes from results, grades and searches
// already stored; nothing here calls a model (SuggestReview, which does, is asked for separately).

// ReviewWindows are the windows a review reads, in days; the first is the default.
var ReviewWindows = []int{30, 60, 90}

// A prompt is reviewed once it has ReviewMinRuns runs and ReviewMinAnswers graded answers in the
// window, under one wording: fewer and every flag below is a guess.
const (
	ReviewMinRuns    = 3
	ReviewMinAnswers = 6
)

// Flag thresholds. Rates are proportions (0.15 is 15 points).
const (
	// Noisy, drift: the run rates differ more than one binomial rate per cell explains (a χ² test
	// of homogeneity, summed over the prompt's cells, at p < NoisyDriftP) and the between-run
	// variance D (SplitVariance) has a standard deviation of at least NoisyDriftSD. Disagreement: a single answer's
	// within-run variance S is at least NoisySingle (answers to the same question in the same run
	// split about 30/70 or closer) and the per-run sampling error sqrt(S/n) is still at least
	// NoisySamplingSD.
	NoisyDriftP     = 0.01
	NoisyDriftSD    = 0.15
	NoisySingle     = 0.20
	NoisySamplingSD = 0.15
	// Off target: with at least OffTargetMinCompetitors configured and OffTargetMinMentions product
	// mentions, under OffTargetKnownShare of them are the brand or a configured competitor; or one other product is named in at
	// least OffTargetDominant of the answers and more than the brand and every competitor; or at
	// least OffTargetOtherProduct of the answers use the brand's name for something else.
	OffTargetMinCompetitors = 3
	OffTargetMinMentions    = 5
	OffTargetKnownShare     = 0.30
	OffTargetDominant       = 0.50
	OffTargetOtherProduct   = 0.25
	// Duplicate: two prompts' named products (each weighted by the share of answers naming it)
	// overlap at least DuplicateSimilarity (weighted Jaccard), and their verdict shares differ by at
	// most DuplicateVerdictGap (total variation distance).
	DuplicateSimilarity = 0.75
	DuplicateVerdictGap = 0.15
	// Rewritten: over at least RewriteMinSearches searches, both the share of a search's words from
	// the prompt and the share of the prompt's words kept average under RewriteOverlap.
	RewriteMinSearches = 5
	RewriteOverlap     = 0.25
	// Branded: outside head-to-heads, the prompt names the brand, or at least BrandedMinSearches
	// searches and BrandedSearchShare of its searches add the brand's name.
	BrandedMinSearches = 2
	BrandedSearchShare = 0.20
	// Gaps: a product named in at least GapAnswerShare of the set's answers, or a competitor added to
	// GapMinInjected searches, that no prompt names; a word searches add at least GapTopicSearches
	// times across GapTopicPrompts prompts that no prompt uses. At most GapMax of each kind.
	GapAnswerShare   = 0.20
	GapMinInjected   = 3
	GapTopicSearches = 5
	GapTopicPrompts  = 2
	GapMax           = 5
)

// Finding kinds, in the order the review lists them.
const (
	Flat      = "flat"
	Noisy     = "noisy"
	OffTarget = "off_target"
	Duplicate = "duplicate"
	Rewritten = "rewritten"
	Branded   = "branded"
)

// ReviewKinds are the kinds with a title and what the flag means, in order.
var ReviewKinds = []struct{ Kind, Title, What string }{
	{Flat, "Flat", "Named and recommended never varied across the runs, and no fix attempt for it moved it."},
	{Noisy, "Noisy", "The named rate moves more from run to run than sampling explains, or answers in one run disagree."},
	{OffTarget, "Off target", "The answers mostly name products outside the category."},
	{Duplicate, "Duplicates", "Two prompts whose answers name nearly the same products, with the same verdicts."},
	{Rewritten, "Rewritten by the assistant", "The assistants search for this question in quite different words."},
	{Branded, "Branded leak", "Outside head-to-heads, the prompt or its searches name the brand."},
}

// Review is a set's prompts reviewed over the last Days days.
type Review struct {
	SetID   string `json:"set_id"`
	SetName string `json:"set_name"`
	File    string `json:"file"`
	Days    int    `json:"days"`
	Since   string `json:"since"`
	Runs    int    `json:"runs"`    // finished runs in the window
	Answers int    `json:"answers"` // graded answers read, every prompt under its current wording
	// Every prompt in the set as configured today, in its order.
	Prompts  []PromptReview `json:"prompts"`
	Findings []Finding      `json:"findings"` // in ReviewKinds order
	Gaps     []Gap          `json:"gaps"`
	// Prompts with enough data and no flag, and prompts without enough data yet.
	Healthy   []string `json:"healthy"`
	NotEnough []string `json:"not_enough_data"`
}

// PromptReview is one prompt's numbers over the runs that asked it as it was last asked.
type PromptReview struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Info    bool   `json:"info,omitempty"`
	Runs    int    `json:"runs"`
	Answers int    `json:"answers"` // graded
	Enough  bool   `json:"enough"`
	From    string `json:"from,omitempty"` // the first day read: older runs asked it in other words
	// Changes in the window worth knowing when reading the numbers: a rewording since, intent or
	// method changes within.
	Notes       []string    `json:"notes,omitempty"`
	Named       report.Rate `json:"named"`
	Recommended report.Rate `json:"recommended"`
	// Each run's named rate, oldest first.
	RunNamed []float64 `json:"run_named"`
	// The split of its named rate's variance over answerer x track cells with two or more samples;
	// nil when no cell has them.
	Noise *VarianceSplit `json:"noise,omitempty"`
	// The chance of run rates at least this uneven if every run drew from one binomial rate per
	// cell (Homogeneity); 1 when there's nothing to test.
	DriftP float64 `json:"drift_p,omitempty"`
	// Products named, by answers naming them, the brand included; and the ones that are neither the
	// brand nor a configured competitor.
	Products   []report.Count `json:"products,omitempty"`
	Unexpected []report.Count `json:"unexpected,omitempty"`
	// Share of product mentions that are the brand or a configured competitor, how many of the
	// configured competitors were named at all, and answers graded as about another product.
	KnownShare      float64        `json:"known_share"`
	MentionsN       int            `json:"mentions"` // product mentions KnownShare is out of
	CompetitorsSeen int            `json:"competitors_seen"`
	OtherProduct    int            `json:"other_product"`
	Verdicts        map[string]int `json:"verdicts"`
	// Its search-track answers' searches (queries.Overlap).
	Searches      int      `json:"searches"`
	FromPrompt    float64  `json:"from_prompt"`
	PromptKept    float64  `json:"prompt_kept"`
	Added         []string `json:"added,omitempty"`
	BrandSearches int      `json:"brand_searches"` // searches that added the brand's name
	Flags         []string `json:"flags,omitempty"`

	shares map[string]float64 // product key -> share of answers naming it
}

// Finding is one flag with the numbers behind it and what to do.
type Finding struct {
	Kind    string `json:"kind"`
	Variant string `json:"variant,omitempty"` // flat: always_absent, always_recommended or always_named; noisy: drift or disagreement
	Prompt  string `json:"prompt"`
	Text    string `json:"text"`
	// A duplicate's other prompt.
	Other      string  `json:"other,omitempty"`
	OtherText  string  `json:"other_text,omitempty"`
	Similarity float64 `json:"similarity,omitempty"`
	Numbers    string  `json:"numbers"`
	Suggestion string  `json:"suggestion"`
	// Off target: the products named that aren't the brand or a configured competitor. Rewritten:
	// the words the searches add.
	Products []report.Count `json:"products,omitempty"`
	Words    []string       `json:"words,omitempty"`
}

// Gap is something the set's answers and searches keep coming back to that no prompt asks about.
type Gap struct {
	Kind      string `json:"kind"` // competitor, product or topic
	Name      string `json:"name"`
	Answers   int    `json:"answers"`   // answers naming it
	Prompts   int    `json:"prompts"`   // prompts whose answers name it, or whose searches add the word
	Searches  int    `json:"searches"`  // searches adding it
	Citations int    `json:"citations"` // answers citing a page on its site
	Numbers   string `json:"numbers"`
	// A prompt for it, as a [[prompts]] table to paste; for a product, a [[competitors]] table too.
	Example    string `json:"example,omitempty"` // the search it comes from
	TOML       string `json:"toml,omitempty"`
	Competitor string `json:"competitor_toml,omitempty"`
}

// ReviewRun is a finished run with its results.
type ReviewRun struct {
	Run     run.Run
	Results []run.Result
}

// ReviewInput is what BuildReview reads.
type ReviewInput struct {
	Brand config.Brand
	Set   sets.Set // as configured today, the shared competitors folded in
	// The set's finished runs in the window, newest first.
	Runs []ReviewRun
	// Fix attempts on the set, with their outcomes.
	Attempts []Attempt
	// Prompt ids in use in any set, so a suggested prompt doesn't reuse one.
	Taken map[string]bool
	Days  int
	Since string
}

// ReviewDays checks a review window.
func ReviewDays(days int) error {
	for _, d := range ReviewWindows {
		if d == days {
			return nil
		}
	}
	return fmt.Errorf("days must be one of %v, not %d", ReviewWindows, days)
}

// Review reviews a set's prompts over the last days days. It reads each run's results as it goes,
// keeping only grades and the searches and citations of answers, never their text, and drops a
// run's data once it's counted. It records nothing: MarkReviewed does.
func (a *App) Review(ctx context.Context, setID string, days int) (Review, error) {
	if err := ReviewDays(days); err != nil {
		return Review{}, err
	}
	s, ok := a.Set(setID)
	if !ok {
		return Review{}, fmt.Errorf("no prompt set %q", setID)
	}
	since := time.Now().UTC().AddDate(0, 0, -days)
	rows, err := a.DB.QueryContext(ctx, `SELECT id FROM runs WHERE set_id=? AND status='done' AND created_at>=? ORDER BY created_at DESC, rowid DESC`, s.ID, since.Format(time.RFC3339))
	if err != nil {
		return Review{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Review{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Review{}, err
	}
	rows.Close()
	in := ReviewInput{Brand: a.Cfg.Brand, Set: s, Days: days, Since: since.Format("2006-01-02"), Taken: map[string]bool{}}
	// Only attempts made in the window can explain a prompt not moving in it.
	if in.Attempts, err = a.attempts(ctx, s.ID, since.Format(time.RFC3339)); err != nil {
		return Review{}, err
	}
	for _, st := range a.Sets() {
		for _, p := range st.Prompts {
			in.Taken[p.ID] = true
		}
	}
	b := newReviewBuilder(in)
	for _, id := range ids {
		r, err := a.Runs.Get(ctx, id)
		if err != nil {
			return Review{}, err
		}
		res, err := a.reviewResults(ctx, id)
		if err != nil {
			return Review{}, err
		}
		r.Intents(res)
		b.add(ReviewRun{*r, res})
	}
	return b.finish(), nil
}

// reviewResults are a run's results with what a review reads: the grade, and of the answer only
// its searches and citations, decoded from just those parts of the stored JSON.
func (a *App) reviewResults(ctx context.Context, runID string) ([]run.Result, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT prompt_idx, prompt, prompt_id, prompt_sha, answerer, track, sample, status, grade,
		json_extract(answer, '$.searches'), json_extract(answer, '$.citations'), answer IS NOT NULL
		FROM results WHERE run_id=? AND status='done' AND grade IS NOT NULL`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []run.Result
	for rows.Next() {
		var x run.Result
		var t string
		var g, searches, citations sql.NullString
		var has bool
		if err := rows.Scan(&x.PromptIdx, &x.Prompt, &x.PromptID, &x.PromptSHA, &x.Answerer, &t, &x.Sample, &x.Status, &g, &searches, &citations, &has); err != nil {
			return nil, err
		}
		x.Track = answer.Track(t)
		x.Grade = &grade.Grade{}
		if err := json.Unmarshal([]byte(g.String), x.Grade); err != nil {
			return nil, fmt.Errorf("result of run %s: grade: %w", runID, err)
		}
		if has {
			x.Answer = &answer.Answer{}
			if searches.Valid {
				if err := json.Unmarshal([]byte(searches.String), &x.Answer.Searches); err != nil {
					return nil, fmt.Errorf("result of run %s: searches: %w", runID, err)
				}
			}
			if citations.Valid {
				if err := json.Unmarshal([]byte(citations.String), &x.Answer.Citations); err != nil {
					return nil, fmt.Errorf("result of run %s: citations: %w", runID, err)
				}
			}
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// MarkReviewed records that the set's prompts were reviewed now, for get_started's suggestion.
// It's best effort: a review that can't be recorded is still a review.
func (a *App) MarkReviewed(ctx context.Context, setID string) {
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO prompt_reviews (set_id, reviewed_at) VALUES (?, ?) ON CONFLICT (set_id) DO UPDATE SET reviewed_at=excluded.reviewed_at`, setID, store.Now()); err != nil {
		slog.Warn("recording a prompt review", "set", setID, "err", err)
	}
}

// LastReview is when the set's prompts were last reviewed, or "" if never.
func (a *App) LastReview(ctx context.Context, setID string) string {
	var at string
	a.DB.QueryRowContext(ctx, `SELECT reviewed_at FROM prompt_reviews WHERE set_id=?`, setID).Scan(&at)
	return at
}

// FirstRun is when the set's oldest finished run was created, or "" if it has none.
func (a *App) FirstRun(ctx context.Context, setID string) string {
	var at sql.NullString
	a.DB.QueryRowContext(ctx, `SELECT MIN(created_at) FROM runs WHERE set_id=? AND status='done'`, setID).Scan(&at)
	return at.String
}

// promptAcc gathers one prompt's answers, newest run first, as counts: no answer is kept but the
// searches of search-track ones.
type promptAcc struct {
	PromptReview
	sha     string
	search  []run.Result // search-track answers, only their searches
	perRun  []Hits       // named, planned samples, newest first
	cells   map[[2]string][]Hits
	order   [][2]string
	methods map[string]bool
	intents map[bool]bool
	stopped bool // reached a run that asked it in other words

	tally    report.Tally
	products map[string]int    // canonical key -> answers naming it
	spell    map[string]string // canonical key -> display name
	known    int               // mentions of the brand or a configured competitor
	mentions int
	comps    map[string]bool
	cited    map[string]int // competitor -> answers citing a page on its site
}

// reviewBuilder is BuildReview run by run.
type reviewBuilder struct {
	in          ReviewInput
	runs        int
	accs        []*promptAcc
	byID        map[string]*promptAcc
	competitors map[string]string // name or alias, lowercase -> name
	names       []queries.Name
}

func newReviewBuilder(in ReviewInput) *reviewBuilder {
	b := &reviewBuilder{in: in, byID: map[string]*promptAcc{}, competitors: map[string]string{}, names: queries.Names(in.Brand, in.Set)}
	for _, c := range in.Set.Competitors {
		if isBrand(in.Brand, c.Name) {
			continue
		}
		b.competitors[strings.ToLower(strings.TrimSpace(c.Name))] = c.Name
		for _, al := range c.Aliases {
			b.competitors[strings.ToLower(strings.TrimSpace(al))] = c.Name
		}
	}
	for _, p := range in.Set.Prompts {
		acc := &promptAcc{PromptReview: PromptReview{ID: p.ID, Text: p.Text, Info: p.Info(), Verdicts: map[string]int{}}, cells: map[[2]string][]Hits{},
			methods: map[string]bool{}, intents: map[bool]bool{}, products: map[string]int{}, spell: map[string]string{}, comps: map[string]bool{}, cited: map[string]int{}}
		b.accs = append(b.accs, acc)
		b.byID[p.ID] = acc
	}
	return b
}

// BuildReview reviews in.Set's prompts from in.Runs.
func BuildReview(in ReviewInput) Review {
	b := newReviewBuilder(in)
	for _, rr := range in.Runs {
		b.add(rr)
	}
	return b.finish()
}

// add counts one run, newer runs before older ones.
func (b *reviewBuilder) add(rr ReviewRun) {
	b.runs++
	brand := b.in.Brand
	brandKey := strings.ToLower(brand.Name)
	day := rr.Run.CreatedAt
	if len(day) >= 10 {
		day = day[:10]
	}
	byPrompt := map[string][]run.Result{}
	for _, x := range rr.Results {
		if x.PromptID != "" && b.byID[x.PromptID] != nil {
			byPrompt[x.PromptID] = append(byPrompt[x.PromptID], x)
		}
	}
	for pid, xs := range byPrompt {
		p := b.byID[pid]
		if p.stopped {
			continue
		}
		sha := xs[0].PromptSHA
		if sha == "" {
			sha = sets.Prompt{Text: xs[0].Prompt}.SHA()
		}
		if p.sha == "" {
			p.sha = sha
			if cur, _ := b.in.Set.Prompt(pid); cur.SHA() != sha {
				p.Notes = append(p.Notes, "reworded since it was last asked: these numbers are for the wording before")
			}
		} else if sha != p.sha {
			p.stopped = true // asked in other words before this: a different question
			p.Notes = append(p.Notes, "reworded by "+p.From+": the runs before asked it in other words and are left out")
			continue
		}
		var named Hits
		planned := map[[2]string]*Hits{}
		for _, x := range xs {
			if x.Status != "done" || x.Grade == nil {
				continue
			}
			if x.Track == answer.Search && x.Answer != nil {
				p.search = append(p.search, run.Result{PromptIdx: x.PromptIdx, Prompt: x.Prompt, PromptID: x.PromptID, Track: x.Track, Status: x.Status,
					Grade: &grade.Grade{Verdict: x.Grade.Verdict}, Answer: &answer.Answer{Searches: x.Answer.Searches}})
			}
			p.count(x, brand, brandKey, b.competitors)
			if x.Answer != nil {
				for _, n := range b.names[1:] {
					for _, l := range x.Answer.Citations {
						if n.OnHost(l.URL) {
							p.cited[n.Name]++
							break
						}
					}
				}
			}
			if rr.Run.Snapshot.Sampling.IsExtra(x.PromptID, x.Answerer, x.Track, x.Sample) {
				continue
			}
			hit := 0
			if x.Grade.Verdict != grade.Absent {
				hit = 1
			}
			named = named.Plus(Hits{hit, 1})
			k := [2]string{x.Answerer, string(x.Track)}
			if planned[k] == nil {
				planned[k] = &Hits{}
			}
			*planned[k] = planned[k].Plus(Hits{hit, 1})
		}
		if named.N == 0 {
			continue // every answer errored: the run didn't measure it
		}
		p.Runs++
		p.From = day
		p.methods[rr.Run.Method] = true
		p.intents[xs[0].Info] = true
		p.perRun = append(p.perRun, named)
		for k, h := range planned {
			if p.cells[k] == nil {
				p.order = append(p.order, k)
			}
			p.cells[k] = append(p.cells[k], *h)
		}
	}
}

// count adds one graded answer's verdict and the products it names, each once, under its
// canonical name (a competitor's aliases folded onto it).
func (p *promptAcc) count(x run.Result, brand config.Brand, brandKey string, competitors map[string]string) {
	p.tally.Add(x.Grade, brand.Name, x.Info)
	p.Verdicts[string(x.Grade.Verdict)]++
	if x.Grade.OtherProduct {
		p.OtherProduct++
	}
	seen := map[string]bool{}
	for _, n := range x.Grade.Named {
		n = label(n)
		k := strings.ToLower(n)
		if k == "" || isBrand(brand, n) {
			continue
		}
		c, comp := competitors[k]
		if comp {
			k, n = strings.ToLower(c), c
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		p.mentions++
		if comp {
			p.known++
			p.comps[c] = true
		}
		if _, ok := p.spell[k]; !ok {
			p.spell[k] = n
		}
		p.products[k]++
	}
	if x.Grade.Verdict != grade.Absent {
		p.mentions++
		p.known++
		p.products[brandKey]++
		p.spell[brandKey] = brand.Name
	}
}

// finish works out the flags from the runs added.
func (b *reviewBuilder) finish() Review {
	in, accs, byID, competitors, names := b.in, b.accs, b.byID, b.competitors, b.names
	rev := Review{SetID: in.Set.ID, SetName: in.Set.Name, File: in.Set.File, Days: in.Days, Since: in.Since, Runs: b.runs,
		Prompts: []PromptReview{}, Findings: []Finding{}, Gaps: []Gap{}, Healthy: []string{}, NotEnough: []string{}}
	brand := in.Brand
	brandKey := strings.ToLower(brand.Name)
	for _, p := range accs {
		if len(p.methods) > 1 {
			p.Notes = append(p.Notes, fmt.Sprintf("asked or graded %d ways in this window (a method change): the rates pool them", len(p.methods)))
		}
		if len(p.intents) > 1 {
			p.Notes = append(p.Notes, "its intent changed in this window: recommended counts only the answers asked as a choice")
		}
		t := p.tally
		p.Answers = t.N
		p.Named, p.Recommended = report.NewRate(t.Vis, t.N), report.NewRate(t.Rec, t.Sel)
		p.Enough = p.Runs >= ReviewMinRuns && p.Answers >= ReviewMinAnswers
		p.MentionsN = p.mentions
		if p.mentions > 0 {
			p.KnownShare = round3(float64(p.known) / float64(p.mentions))
		}
		p.CompetitorsSeen = len(p.comps)
		p.shares = map[string]float64{}
		for k, n := range p.products {
			c := report.Count{Name: p.spell[k], N: n}
			p.Products = append(p.Products, c)
			if t.N > 0 {
				p.shares[k] = float64(n) / float64(t.N)
			}
			if _, comp := competitors[k]; !comp && k != brandKey {
				p.Unexpected = append(p.Unexpected, c)
			}
		}
		sortCounts(p.Products)
		sortCounts(p.Unexpected)
		for i := len(p.perRun) - 1; i >= 0; i-- {
			p.RunNamed = append(p.RunNamed, round3(p.perRun[i].rate()))
		}
		var cells [][]Hits
		for _, k := range p.order {
			xs := p.cells[k]
			// Oldest first, keeping the runs with the cell's usual count (an error leaves one short).
			n := modalN(xs)
			var keep []Hits
			for i := len(xs) - 1; i >= 0; i-- {
				if xs[i].N == n {
					keep = append(keep, xs[i])
				}
			}
			cells = append(cells, keep)
		}
		if v := SplitVariance(cells); v.Cells > 0 {
			p.Noise = &v
			chi, df := Homogeneity(cells)
			p.DriftP = round4(ChiSquareP(chi, df))
		}
		rev.Answers += p.Answers
	}

	// Searches, prompt by prompt, under the same wordings as the grades.
	var search []run.Result
	for _, p := range accs {
		search = append(search, p.search...)
	}
	qs := queries.Build(queries.Input{Brand: brand, Set: in.Set, Results: search, Taken: in.Taken})
	for _, qp := range qs.Prompts {
		p := byID[qp.ID]
		if p == nil {
			continue
		}
		p.Searches = int(math.Round(qp.PerAnswer * float64(qp.Answers)))
		p.FromPrompt, p.PromptKept = round3(qp.FromPrompt), round3(qp.PromptKept)
		for _, w := range qp.Added {
			p.Added = append(p.Added, label(w.Word))
		}
	}
	for _, p := range accs {
		for _, x := range p.search {
			for _, s := range x.Answer.Searches {
				for _, n := range queries.Injected(names[:1], x.Prompt, s.Query) {
					if n.IsBrand {
						p.BrandSearches++
					}
				}
			}
		}
	}

	// Fix attempts on each prompt: a prompt moved up by one isn't flat, whatever the others in
	// the same fix did.
	moved := map[string]bool{}
	awaiting := map[string]int{}
	tried := map[string]int{}
	for _, at := range in.Attempts {
		if at.State == Withdrawn {
			continue
		}
		st := at.Outcome.Status()
		for _, id := range at.Fix.PromptIDs {
			tried[id]++
			switch st {
			case OutcomePending, OutcomeTooEarly:
				awaiting[id]++
			case OutcomeIncomparable:
			default:
				if promptMovedUp(at.Outcome, id) {
					moved[id] = true
				}
			}
		}
	}

	byKind := map[string][]Finding{}
	add := func(p *promptAcc, f Finding) {
		f.Prompt, f.Text = p.ID, p.Text
		byKind[f.Kind] = append(byKind[f.Kind], f)
		p.Flags = append(p.Flags, f.Kind)
	}
	h2h := in.Set.ID == sets.HeadToHeads
	for _, p := range accs {
		// A branded prompt is worth knowing about however little it has been asked.
		if !h2h && report.Branded(brand, p.Text) {
			add(p, Finding{Kind: Branded, Variant: "prompt", Numbers: fmt.Sprintf("the prompt names %s", brand.Name),
				Suggestion: fmt.Sprintf("only %s may name the brand: reword it the way a buyer who hasn't heard of %s would ask, or move it there", sets.HeadToHeads, brand.Name)})
		}
		if !p.Enough {
			continue
		}
		if f, ok := flat(p, brand.Name, tried[p.ID], awaiting[p.ID]); ok && !moved[p.ID] {
			add(p, f)
		} else if f, ok := noisy(p); ok {
			add(p, f)
		}
		if f, ok := offTarget(p, brand.Name, distinct(competitors)); ok {
			add(p, f)
		}
		if p.Searches >= RewriteMinSearches && p.FromPrompt < RewriteOverlap && p.PromptKept < RewriteOverlap {
			words := p.Added[:min(len(p.Added), 5)]
			sugg := "buyers' assistants search for this in other words; consider phrasing the prompt the way they search"
			if len(words) > 0 {
				sugg = fmt.Sprintf("buyers' assistants search for different words: %s; consider phrasing the prompt that way", quoted(words))
			}
			add(p, Finding{Kind: Rewritten, Words: words,
				Numbers:    fmt.Sprintf("%.0f%% of a search's words come from the prompt and it keeps %.0f%% of the prompt's, over %d searches", 100*p.FromPrompt, 100*p.PromptKept, p.Searches),
				Suggestion: sugg})
		}
		if !h2h && p.BrandSearches >= BrandedMinSearches && float64(p.BrandSearches) >= BrandedSearchShare*float64(p.Searches) {
			add(p, Finding{Kind: Branded, Variant: "searches",
				Numbers:    fmt.Sprintf("%d of %d searches added %s's name", p.BrandSearches, p.Searches, brand.Name),
				Suggestion: fmt.Sprintf("check the prompt doesn't point at %s (a term or feature only it uses); if it doesn't, the assistants found %s on their own and the prompt is fine", brand.Name, brand.Name)})
		}
	}
	// Duplicates, pair by pair in set order.
	for i, a := range accs {
		for _, b := range accs[i+1:] {
			if !a.Enough || !b.Enough || len(a.shares) == 0 || len(b.shares) == 0 || a.Info != b.Info {
				continue
			}
			sim, gap := weightedJaccard(a.shares, b.shares), verdictGap(a.Verdicts, b.Verdicts)
			if sim >= DuplicateSimilarity && gap <= DuplicateVerdictGap {
				add(a, Finding{Kind: Duplicate, Other: b.ID, OtherText: b.Text, Similarity: round3(sim),
					Numbers:    fmt.Sprintf("their answers name the same products %.0f%% (weighted by how often), and their verdict shares differ by %.0f points; named %s vs %s", 100*sim, 100*gap, pctOf(a.Named), pctOf(b.Named)),
					Suggestion: fmt.Sprintf("merge them to save answers: keep one, and retire the other (%s)", b.ID)})
				b.Flags = append(b.Flags, Duplicate)
			}
		}
	}
	for _, k := range ReviewKinds {
		rev.Findings = append(rev.Findings, byKind[k.Kind]...)
	}
	for _, p := range accs {
		switch {
		case !p.Enough:
			rev.NotEnough = append(rev.NotEnough, p.ID)
		case len(p.Flags) == 0:
			rev.Healthy = append(rev.Healthy, p.ID)
		}
		rev.Prompts = append(rev.Prompts, p.PromptReview)
	}
	rev.Gaps = gaps(in, accs, qs, names, competitors, rev.Answers)
	return rev
}

// promptMovedUp reports whether an attempt's outcome shows prompt id's named or recommended rate
// rising beyond its 95% margin, on that prompt's answers alone.
func promptMovedUp(o Outcome, id string) bool {
	for _, p := range o.Prompts {
		if p.PromptID != id {
			continue
		}
		named := PairedDiff([]Hits{{p.Before.Named, p.Before.N}}, []Hits{{p.After.Named, p.After.N}})
		rec := PairedDiff([]Hits{{p.Before.Recommended, p.Before.Choice}}, []Hits{{p.After.Recommended, p.After.Choice}})
		return named.Sign() > 0 || rec.Sign() > 0
	}
	return false
}

// flat is a prompt whose answers never varied: never named, or always recommended (named, for
// an info prompt).
func flat(p *promptAcc, brand string, tried, awaiting int) (Finding, bool) {
	f := Finding{Kind: Flat}
	runs := fmt.Sprintf("%d answers over %d runs", p.Answers, p.Runs)
	switch {
	case p.Named.Hits == 0:
		f.Variant = "always_absent"
		f.Numbers = fmt.Sprintf("named in 0 of %s", runs)
		f.Suggestion = fmt.Sprintf("tells you little; consider rewording or retiring it. Either %s can't win this question (reword it toward what %s does, or retire it), or it's the most important gap in the set (keep it, and work the fix list for it)", brand, brand)
	case !p.Info && p.Recommended.N > 0 && p.Recommended.Hits == p.Recommended.N:
		f.Variant = "always_recommended"
		f.Numbers = fmt.Sprintf("recommended in all %s", runs)
		f.Suggestion = "at the ceiling, it tells you little beyond when something breaks; consider rewording it to a harder question, or retiring it to save answers"
	case p.Info && p.Named.Hits == p.Named.N:
		f.Variant = "always_named"
		f.Numbers = fmt.Sprintf("named in all %s", runs)
		f.Suggestion = "at the ceiling, it tells you little beyond when something breaks; consider rewording it to a harder question, or retiring it to save answers"
	default:
		return f, false
	}
	if tried > 0 {
		f.Numbers += fmt.Sprintf("; %d fix attempts targeted it and none moved it", tried)
	}
	if awaiting > 0 {
		f.Suggestion += fmt.Sprintf(". %d fix attempts for it are still waiting for an outcome: wait for them first", awaiting)
	}
	return f, true
}

// noisy is a prompt whose named rate drifts run to run beyond sampling, or whose answers in one
// run disagree.
func noisy(p *promptAcc) (Finding, bool) {
	v := p.Noise
	if v == nil || v.Runs < ReviewMinRuns {
		return Finding{}, false
	}
	lo, hi := 1.0, 0.0
	for _, x := range p.RunNamed {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	spread := fmt.Sprintf("named %.0f%%–%.0f%% run to run over %d runs", 100*lo, 100*hi, p.Runs)
	switch {
	case p.DriftP < NoisyDriftP && math.Sqrt(v.Between) >= NoisyDriftSD:
		return Finding{Kind: Noisy, Variant: "drift",
			Numbers:    fmt.Sprintf("%s; drift between runs ±%.0f pts (1 SD) against ±%.0f pts from sampling, p = %.3f that sampling alone spreads them this much", spread, 100*math.Sqrt(v.Between), 100*math.Sqrt(v.Within), p.DriftP),
			Suggestion: "reword it to be more specific (who is asking, for what): more samples won't steady drift between runs"}, true
	case v.Single >= NoisySingle && math.Sqrt(v.Within) >= NoisySamplingSD:
		return Finding{Kind: Noisy, Variant: "disagreement",
			Numbers:    fmt.Sprintf("%s; answers in the same run disagree (a single answer's variance %.2f, ±%.0f pts per run from sampling)", spread, v.Single, 100*math.Sqrt(v.Within)),
			Suggestion: "reword it to be more specific, or add samples: answers to it split within a run"}, true
	}
	return Finding{}, false
}

// offTarget is a prompt whose answers are mostly about other products.
func offTarget(p *promptAcc, brand string, configured int) (Finding, bool) {
	var why []string
	if configured >= OffTargetMinCompetitors && p.MentionsN >= OffTargetMinMentions && p.KnownShare < OffTargetKnownShare {
		why = append(why, fmt.Sprintf("%.0f%% of %d product mentions are %s or a configured competitor (%d of %d competitors named at all)", 100*p.KnownShare, p.MentionsN, brand, p.CompetitorsSeen, configured))
	}
	if len(p.Unexpected) > 0 {
		top := p.Unexpected[0]
		lead := true
		for _, c := range p.Products {
			if c.Name != top.Name && c.N >= top.N && !containsCount(p.Unexpected, c.Name) {
				lead = false
			}
		}
		if lead && float64(top.N) >= OffTargetDominant*float64(p.Answers) {
			why = append(why, fmt.Sprintf("%q, not a configured competitor, is named in %d of %d answers, more than %s or any competitor", top.Name, top.N, p.Answers, brand))
		}
	}
	if p.Answers > 0 && float64(p.OtherProduct) >= OffTargetOtherProduct*float64(p.Answers) {
		why = append(why, fmt.Sprintf("%d of %d answers use the name %s for another product", p.OtherProduct, p.Answers, brand))
	}
	if len(why) == 0 {
		return Finding{}, false
	}
	top := p.Unexpected[:min(len(p.Unexpected), 5)]
	var names []string
	for _, c := range top {
		names = append(names, c.Name)
	}
	sugg := "add context to the prompt (the category, who is asking) so it's answered about the products you compete with"
	if len(names) > 0 {
		sugg += fmt.Sprintf(", or add %q to the competitors if it is in fact one", names[0])
	}
	return Finding{Kind: OffTarget, Numbers: strings.Join(why, "; "), Suggestion: sugg, Products: top}, true
}

// gaps are the competitors, products and search words the set's answers come back to that no
// prompt asks about, each with a prompt to paste.
func gaps(in ReviewInput, accs []*promptAcc, qs queries.Report, names []queries.Name, competitors map[string]string, answers int) []Gap {
	out := []Gap{}
	if answers == 0 {
		return out
	}
	brand := in.Brand
	h2h := in.Set.ID == sets.HeadToHeads
	var texts []string
	for _, p := range in.Set.Prompts {
		texts = append(texts, p.Text)
	}
	all := strings.Join(texts, "\n")
	asked := func(name string) bool { return sets.Names(all, name) }
	taken := map[string]bool{}
	for k := range in.Taken {
		taken[k] = true
	}
	for _, p := range in.Set.Prompts {
		taken[p.ID] = true
	}
	promptFor := func(text string) string {
		id := queries.PromptID(text, taken)
		taken[id] = true
		return queries.Snippet(sets.Prompt{ID: id, Text: text})
	}
	alternatives := func(name string) string {
		if h2h {
			return fmt.Sprintf("Should I choose %s or %s?", brand.Name, name)
		}
		return fmt.Sprintf("What are the best alternatives to %s?", name)
	}

	// Products named across the set: answers and prompts naming each.
	type tally struct {
		name             string
		answers, prompts int
	}
	byKey := map[string]*tally{}
	for _, p := range accs {
		for _, c := range p.Products {
			k := strings.ToLower(c.Name)
			if k == strings.ToLower(brand.Name) {
				continue
			}
			if byKey[k] == nil {
				byKey[k] = &tally{name: c.Name}
			}
			byKey[k].answers += c.N
			byKey[k].prompts++
		}
	}
	injected := map[string]int{}
	for _, x := range qs.Injected {
		if !x.IsBrand {
			injected[x.Name] = x.Searches
		}
	}
	cited := map[string]int{}
	for _, p := range accs {
		for n, c := range p.cited {
			cited[n] += c
		}
	}
	var comp, prod []Gap
	for _, n := range names[1:] {
		t := byKey[strings.ToLower(n.Name)]
		g := Gap{Kind: "competitor", Name: n.Name, Searches: injected[n.Name], Citations: cited[n.Name]}
		if t != nil {
			g.Answers, g.Prompts = t.answers, t.prompts
		}
		if n.In(all) || float64(g.Answers) < GapAnswerShare*float64(answers) && g.Searches < GapMinInjected {
			continue
		}
		g.Numbers = fmt.Sprintf("named in %d of %d answers across %d prompts, added to %d searches, its pages cited in %d answers; no prompt names it", g.Answers, answers, g.Prompts, g.Searches, g.Citations)
		g.TOML = promptFor(alternatives(n.Name))
		comp = append(comp, g)
	}
	for k, t := range byKey {
		if _, ok := competitors[k]; ok || t.prompts < 2 || float64(t.answers) < GapAnswerShare*float64(answers) || asked(t.name) {
			continue
		}
		g := Gap{Kind: "product", Name: t.name, Answers: t.answers, Prompts: t.prompts}
		g.Numbers = fmt.Sprintf("named in %d of %d answers across %d prompts, but neither a configured competitor nor in any prompt", t.answers, answers, t.prompts)
		g.TOML = promptFor(alternatives(t.name))
		g.Competitor = fmt.Sprintf("[[competitors]]\nname = %q", t.name)
		prod = append(prod, g)
	}
	byAnswers := func(gs []Gap) {
		sort.Slice(gs, func(i, j int) bool {
			if gs[i].Answers+gs[i].Searches != gs[j].Answers+gs[j].Searches {
				return gs[i].Answers+gs[i].Searches > gs[j].Answers+gs[j].Searches
			}
			return gs[i].Name < gs[j].Name
		})
	}
	byAnswers(comp)
	byAnswers(prod)
	out = append(out, comp[:min(len(comp), GapMax)]...)
	out = append(out, prod[:min(len(prod), GapMax)]...)

	// Topics: words the searches add, across prompts, that no prompt uses, with the most-run
	// search that has the word, in a buyer's words (no search operators) and not naming the brand
	// outside head-to-heads.
	topics := 0
	for _, w := range qs.Added {
		if topics == GapMax || w.Searches < GapTopicSearches {
			break
		}
		if w.Prompts < GapTopicPrompts {
			continue
		}
		if from, _, _, _ := queries.Overlap(all, w.Word); from > 0 {
			continue
		}
		g := Gap{Kind: "topic", Name: label(w.Word), Searches: w.Searches, Prompts: w.Prompts,
			Numbers: fmt.Sprintf("added to %d searches across %d prompts; no prompt uses the word", w.Searches, w.Prompts)}
		for _, q := range qs.Queries {
			for _, part := range strings.Split(q.Query, " | ") {
				part = strings.TrimSpace(part)
				if from, _, _, _ := queries.Overlap(w.Word, part); from == 0 || !buyerWords(part) || !h2h && report.Branded(brand, part) {
					continue
				}
				g.Example = part
				g.TOML = promptFor(question(part))
				break
			}
			if g.Example != "" {
				break
			}
		}
		out = append(out, g)
		topics++
	}
	return out
}

// buyerWords reports whether a search query reads as a buyer's words: no search operators or
// exact-match quotes, and at least three words.
func buyerWords(q string) bool {
	return !strings.Contains(q, "site:") && !strings.Contains(q, "site.") && !strings.Contains(q, `"`) && len(strings.Fields(q)) >= 3
}

// question turns a search into the start of a buyer's question: capitalised, ending in a question
// mark. It stays a suggestion to reword.
func question(q string) string {
	q = strings.TrimRight(strings.TrimSpace(q), "?.!")
	if q == "" {
		return q
	}
	r, n := utf8.DecodeRuneInString(q)
	return string(unicode.ToUpper(r)) + q[n:] + "?"
}

// weightedJaccard is Σ min / Σ max over two weightings: 1 when they're the same, 0 when they
// share nothing.
func weightedJaccard(a, b map[string]float64) float64 {
	var lo, hi float64
	for k, x := range a {
		lo += math.Min(x, b[k])
		hi += math.Max(x, b[k])
	}
	for k, y := range b {
		if _, ok := a[k]; !ok {
			hi += y
		}
	}
	if hi == 0 {
		return 0
	}
	return lo / hi
}

// verdictGap is the total variation distance between two prompts' verdict shares.
func verdictGap(a, b map[string]int) float64 {
	share := func(m map[string]int, v grade.Verdict) float64 {
		n := 0
		for _, c := range m {
			n += c
		}
		if n == 0 {
			return 0
		}
		return float64(m[string(v)]) / float64(n)
	}
	var d float64
	for _, v := range []grade.Verdict{grade.Recommended, grade.Mentioned, grade.Absent} {
		d += math.Abs(share(a, v) - share(b, v))
	}
	return d / 2
}

func modalN(xs []Hits) int {
	count := map[int]int{}
	best, n := 0, 0
	for _, x := range xs {
		count[x.N]++
		if count[x.N] > best || count[x.N] == best && x.N > n {
			best, n = count[x.N], x.N
		}
	}
	return n
}

// distinct is how many competitors there are, aliases folded.
func distinct(competitors map[string]string) int {
	out := map[string]bool{}
	for _, n := range competitors {
		out[n] = true
	}
	return len(out)
}

func sortCounts(c []report.Count) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].N != c[j].N {
			return c[i].N > c[j].N
		}
		return c[i].Name < c[j].Name
	})
}

func containsCount(cs []report.Count, name string) bool {
	for _, c := range cs {
		if c.Name == name {
			return true
		}
	}
	return false
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

func pctOf(r report.Rate) string { return fmt.Sprintf("%.0f%%", 100*r.P) }

// Brief is the review in a few lines per finding, for a reader (or a model) that won't look at
// the tables.
func (r Review) Brief() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Prompt review of %s over the last %d days: %d runs, %d graded answers. %d prompts healthy, %d without enough data yet (%d runs and %d answers needed).\n",
		r.SetID, r.Days, r.Runs, r.Answers, len(r.Healthy), len(r.NotEnough), ReviewMinRuns, ReviewMinAnswers)
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "- %s", f.Kind)
		if f.Variant != "" {
			fmt.Fprintf(&b, " (%s)", f.Variant)
		}
		fmt.Fprintf(&b, " %s: %q", f.Prompt, f.Text)
		if f.Other != "" {
			fmt.Fprintf(&b, " and %s: %q", f.Other, f.OtherText)
		}
		fmt.Fprintf(&b, ". %s. Suggestion: %s.", oneLine(f.Numbers), oneLine(f.Suggestion))
		if len(f.Products) > 0 {
			var ns []string
			for _, c := range f.Products {
				ns = append(ns, fmt.Sprintf("%q x%d", c.Name, c.N))
			}
			fmt.Fprintf(&b, " Products: %s.", strings.Join(ns, ", "))
		}
		b.WriteString("\n")
	}
	for _, g := range r.Gaps {
		fmt.Fprintf(&b, "- gap (%s) %q: %s.\n", g.Kind, g.Name, g.Numbers)
	}
	return strings.TrimSpace(b.String())
}

// Sections are the findings by kind, in ReviewKinds order, for the page.
func (r Review) Sections() []ReviewSection {
	var out []ReviewSection
	for _, k := range ReviewKinds {
		s := ReviewSection{Kind: k.Kind, Title: k.Title, What: k.What}
		for _, f := range r.Findings {
			if f.Kind == k.Kind {
				s.Items = append(s.Items, f)
			}
		}
		out = append(out, s)
	}
	return out
}

type ReviewSection struct {
	Kind, Title, What string
	Items             []Finding
}

// ReviewDue reports whether a set with its first finished run at first, last reviewed at
// reviewed ("" for never), is due a review: it has four weeks of runs, and none in the last 30
// days.
func ReviewDue(first, reviewed string, now time.Time) bool {
	f, err := time.Parse(time.RFC3339, first)
	if err != nil || now.Sub(f) < 28*24*time.Hour {
		return false
	}
	r, err := time.Parse(time.RFC3339, reviewed)
	return err != nil || now.Sub(r) >= 30*24*time.Hour
}

// label is a product name or word from an answer or a search, which come from the web: one
// line, no control characters, at most 60 characters.
func label(s string) string {
	s = clean(s)
	if utf8.RuneCountInString(s) > 60 {
		s = string([]rune(s)[:60])
	}
	return s
}

// clean is s on one line, control characters replaced with spaces.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// quoted is words quoted and joined: "a", "b".
func quoted(ws []string) string {
	var out []string
	for _, w := range ws {
		out = append(out, fmt.Sprintf("%q", w))
	}
	return strings.Join(out, ", ")
}

func oneLine(s string) string { return clean(s) }

func round4(f float64) float64 { return math.Round(f*10000) / 10000 }
