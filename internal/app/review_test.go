package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metcalfc/popcorn/internal/analysis"
	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/grade"
	"github.com/metcalfc/popcorn/internal/run"
	"github.com/metcalfc/popcorn/internal/sets"
)

// reviewPrompt is one synthetic prompt: what its six answers a run say, run by run (oldest first).
type reviewPrompt struct {
	p sets.Prompt
	// named[i] is how many of run i's six answers name the brand (as mentioned, or recommended
	// with rec).
	named  []int
	rec    bool
	others []string // products every answer names
	other  int      // answers per run graded as about another product
	search string   // the query every answer searched, if any
	// The wording runs before this one (0-based, oldest first) asked it in.
	oldText string
	oldRuns int
}

func reviewFixture() (sets.Set, []reviewPrompt) {
	set := sets.Set{ID: "tools", Name: "Tools", File: "tools.toml", Competitors: []sets.Competitor{{Name: "Profound"}, {Name: "Peec AI"}, {Name: "Otterly"}}}
	ps := []reviewPrompt{
		{p: sets.Prompt{ID: "flat", Text: "Which enterprise AI visibility suite should a bank use?"}, named: []int{0, 0, 0, 0, 0}, others: []string{"Profound", "Peec AI", "Semrush"},
			oldText: "Which AI visibility suite for a bank?", oldRuns: 1},
		{p: sets.Prompt{ID: "noisy", Text: "What should I use to see if ChatGPT mentions my company?"}, named: []int{6, 0, 6, 0, 6}, others: []string{"Profound", "Semrush"}},
		{p: sets.Prompt{ID: "binom", Text: "What's a good AI visibility tracker for a startup?"}, named: []int{0, 1, 1, 0, 1}, others: []string{"Peec AI", "Otterly"},
			search: "ai visibility tracker startup pricing"},
		{p: sets.Prompt{ID: "off", Text: "Is popcorn a good snack for an office?"}, named: []int{0, 0, 0, 0, 0}, others: []string{"Bad Popcorn", "Butter Co"}, other: 3},
		{p: sets.Prompt{ID: "dup-a", Text: "Which tools track AI search visibility?"}, named: []int{1, 1, 1, 1, 1}, others: []string{"Otterly", "Profound", "Peec AI"}},
		{p: sets.Prompt{ID: "dup-b", Text: "What software monitors how AI assistants rank brands?"}, named: []int{1, 1, 1, 1, 1}, others: []string{"Otterly", "Profound", "Peec AI"}},
		{p: sets.Prompt{ID: "rewrite", Text: "Which tool tracks brand mentions in chatbots?"}, named: []int{1, 1, 1, 1, 1}, others: []string{"Profound"},
			search: "geo software pricing comparison 2026"},
		{p: sets.Prompt{ID: "leak", Text: "Which AI visibility tool is easiest to self-host?"}, named: []int{1, 1, 1, 1, 1}, others: []string{"Otterly"},
			search: "popcorn self-host ai visibility tool"},
		{p: sets.Prompt{ID: "thin", Text: "Is there a free AI visibility checker?"}, named: []int{0, 0, 0, 3, 3}}, // asked only the last two runs
	}
	for _, x := range ps {
		set.Prompts = append(set.Prompts, x.p)
	}
	return set, ps
}

// reviewRuns makes five daily runs of the fixture, newest first, each prompt answered six times on
// the search track.
func reviewRuns(ps []reviewPrompt) []ReviewRun {
	var out []ReviewRun
	for i := 0; i < 5; i++ {
		day := time.Now().UTC().AddDate(0, 0, -5+i).Format("2006-01-02")
		rr := ReviewRun{Run: run.Run{ID: fmt.Sprintf("r%d", i), CreatedAt: day + "T12:00:00Z", Method: "m", Status: "done"}}
		for idx, x := range ps {
			if x.p.ID == "thin" && i < 3 {
				continue
			}
			text := x.p.Text
			if i < x.oldRuns {
				text = x.oldText
			}
			for s := 0; s < 6; s++ {
				g := &grade.Grade{Verdict: grade.Absent, Named: append([]string(nil), x.others...)}
				if s < x.named[i] {
					g.Verdict = grade.Mentioned
					if x.rec {
						g.Verdict = grade.Recommended
					}
					g.Named = append(g.Named, "Popcorn")
				}
				g.OtherProduct = s < x.other
				ans := &answer.Answer{Text: "an answer"}
				if x.search != "" {
					ans.Searches = []answer.Query{{Query: x.search, Completed: true}}
				}
				rr.Results = append(rr.Results, run.Result{PromptIdx: idx, Prompt: text, PromptID: x.p.ID, PromptSHA: sets.Prompt{Text: text}.SHA(), Info: x.p.Info(),
					Answerer: "claude", Track: answer.Search, Sample: s, Status: "done", Answer: ans, Grade: g})
			}
		}
		out = append([]ReviewRun{rr}, out...)
	}
	return out
}

func findings(rev Review, kind string) map[string]Finding {
	out := map[string]Finding{}
	for _, f := range rev.Findings {
		if f.Kind == kind {
			out[f.Prompt] = f
		}
	}
	return out
}

func TestReviewFlags(t *testing.T) {
	set, ps := reviewFixture()
	in := ReviewInput{Brand: config.Brand{Name: "Popcorn"}, Set: set, Runs: reviewRuns(ps), Days: 30, Taken: map[string]bool{"elsewhere": true}}
	rev := BuildReview(in)
	if rev.Runs != 5 || len(rev.Prompts) != len(set.Prompts) {
		t.Fatalf("%+v", rev)
	}
	prompt := func(id string) PromptReview {
		for _, p := range rev.Prompts {
			if p.ID == id {
				return p
			}
		}
		t.Fatalf("no prompt %s", id)
		return PromptReview{}
	}

	// Minimum data: two runs aren't enough, whatever they say.
	if !slices.Equal(rev.NotEnough, []string{"thin"}) || prompt("thin").Enough || len(prompt("thin").Flags) != 0 {
		t.Errorf("not enough data: %v %+v", rev.NotEnough, prompt("thin"))
	}
	// The flat prompt reads only its current wording: four runs, the reworded one left out.
	fl := findings(rev, Flat)
	if f, ok := fl["flat"]; !ok || f.Variant != "always_absent" || !strings.Contains(f.Numbers, "named in 0 of 24 answers over 4 runs") || !strings.Contains(f.Suggestion, "most important gap") {
		t.Errorf("flat: %+v", fl)
	}
	if p := prompt("flat"); p.Runs != 4 || len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "reworded") {
		t.Errorf("flat stops at the rewording: %+v", p)
	}
	// Drift between runs is noisy; the same mean moving only as much as sampling would isn't.
	no := findings(rev, Noisy)
	if f, ok := no["noisy"]; !ok || f.Variant != "drift" || !strings.Contains(f.Numbers, "named 0%–100% run to run") {
		t.Errorf("noisy: %+v", no)
	}
	if _, ok := no["binom"]; ok || len(no) != 1 {
		t.Errorf("binomial-only noise is not a flag: %+v", no)
	}
	if v := prompt("binom").Noise; v == nil || v.Between != 0 || v.Runs != 5 {
		t.Errorf("binom's split: %+v", v)
	}
	// Off target: the answers are about a same-named product.
	off := findings(rev, OffTarget)
	if f, ok := off["off"]; !ok || len(f.Products) == 0 || f.Products[0].Name != "Bad Popcorn" || f.Products[0].N != 30 ||
		!strings.Contains(f.Numbers, "0% of the products named") || !strings.Contains(f.Numbers, "15 of 30 answers use the name Popcorn for another product") || !strings.Contains(f.Suggestion, "add Bad Popcorn to the competitors") {
		t.Errorf("off target: %+v", off)
	}
	if len(off) != 1 {
		t.Errorf("only one prompt is off target: %+v", off)
	}
	// Duplicates: the pair, once.
	dup := findings(rev, Duplicate)
	if f, ok := dup["dup-a"]; !ok || f.Other != "dup-b" || f.Similarity != 1 || len(dup) != 1 || !slices.Contains(prompt("dup-b").Flags, Duplicate) {
		t.Errorf("duplicates: %+v", dup)
	}
	// Rewritten: no word of the search is the prompt's.
	rw := findings(rev, Rewritten)
	if f, ok := rw["rewrite"]; !ok || len(rw) != 1 || !slices.Contains(f.Words, "pricing") || !strings.Contains(f.Numbers, "0% of a search's words come from the prompt") {
		t.Errorf("rewritten: %+v", rw)
	}
	// Branded: the brand added to every search; and the snack prompt names it, as a word.
	br := findings(rev, Branded)
	if f, ok := br["leak"]; !ok || f.Variant != "searches" || !strings.Contains(f.Numbers, "30 of 30 searches added Popcorn's name") || br["off"].Variant != "prompt" || len(br) != 2 {
		t.Errorf("branded: %+v", br)
	}
	if !slices.Equal(rev.Healthy, []string{"binom"}) {
		t.Errorf("healthy: %v", rev.Healthy)
	}

	// Gaps: a competitor no prompt names, a product across prompts that isn't a competitor, and a
	// word searches add across prompts, each with a prompt that parses and a fresh id.
	byName := map[string]Gap{}
	for _, g := range rev.Gaps {
		byName[g.Kind+":"+g.Name] = g
	}
	if g, ok := byName["competitor:Profound"]; !ok || g.Answers != 144 || !strings.Contains(g.TOML, `text = "What are the best alternatives to Profound?"`) {
		t.Errorf("competitor gap: %+v", rev.Gaps)
	}
	if g, ok := byName["product:Semrush"]; !ok || g.Prompts != 2 || !strings.Contains(g.Competitor, `name = "Semrush"`) {
		t.Errorf("product gap: %+v", rev.Gaps)
	}
	if _, ok := byName["product:Bad Popcorn"]; ok {
		t.Errorf("a product in one prompt's answers isn't a gap: %+v", rev.Gaps)
	}
	if g, ok := byName["topic:pricing"]; !ok || g.Prompts != 2 || g.Example == "" || !strings.Contains(g.TOML, "[[prompts]]") || strings.Contains(g.TOML, "Popcorn") {
		t.Errorf("topic gap: %+v", rev.Gaps)
	}
	if !strings.Contains(rev.Brief(), "- flat (always_absent) flat:") {
		t.Errorf("brief:\n%s", rev.Brief())
	}

	// A fix attempt that moved the flat prompt keeps it off the list; one still waiting is noted.
	moved := Attempt{State: Accepted, Fix: analysis.Fix{PromptIDs: []string{"flat"}},
		Outcome: Outcome{RunID: "r4", Days: 10, Prompts: []PromptOutcome{{PromptID: "flat", Before: Tally{N: 20}, After: Tally{N: 20, Named: 20}}}}}
	in.Attempts = []Attempt{moved}
	if _, ok := findings(BuildReview(in), Flat)["flat"]; ok {
		t.Error("a fix attempt moved it: not flat")
	}
	in.Attempts = []Attempt{{State: Proposed, Fix: analysis.Fix{PromptIDs: []string{"flat"}}}}
	if f := findings(BuildReview(in), Flat)["flat"]; !strings.Contains(f.Numbers, "1 fix attempts targeted it") || !strings.Contains(f.Suggestion, "still waiting") {
		t.Errorf("pending attempt: %+v", f)
	}
}

// A ceiling prompt is flat too, read as low information rather than a gap; a branded prompt is
// flagged however little it has run, and nothing reads as anything with no runs.
func TestReviewCeilingAndEmpty(t *testing.T) {
	set := sets.Set{ID: "s", Prompts: []sets.Prompt{{ID: "top", Text: "Best tool?"}, {ID: "named", Text: "Is Popcorn any good?"}}}
	ps := []reviewPrompt{{p: set.Prompts[0], named: []int{6, 6, 6, 6, 6}, rec: true, others: []string{"X"}}, {p: set.Prompts[1], named: []int{1, 1, 1, 1, 1}}}
	rev := BuildReview(ReviewInput{Brand: config.Brand{Name: "Popcorn"}, Set: set, Runs: reviewRuns(ps)})
	if f := findings(rev, Flat)["top"]; f.Variant != "always_recommended" || !strings.Contains(f.Suggestion, "ceiling") {
		t.Errorf("ceiling: %+v", rev.Findings)
	}
	if f := findings(rev, Branded)["named"]; f.Variant != "prompt" {
		t.Errorf("branded prompt: %+v", rev.Findings)
	}
	empty := BuildReview(ReviewInput{Brand: config.Brand{Name: "Popcorn"}, Set: set})
	if len(empty.Findings) != 1 || len(empty.NotEnough) != 2 || len(empty.Gaps) != 0 || empty.Healthy == nil {
		t.Errorf("no runs: %+v", empty)
	}
}

func TestReviewDue(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ago := func(d int) string { return now.AddDate(0, 0, -d).Format(time.RFC3339) }
	for _, c := range []struct {
		first, reviewed string
		want            bool
	}{{ago(40), "", true}, {ago(20), "", false}, {"", "", false}, {ago(40), ago(10), false}, {ago(90), ago(31), true}} {
		if got := ReviewDue(c.first, c.reviewed, now); got != c.want {
			t.Errorf("first %s reviewed %q: %v", c.first, c.reviewed, got)
		}
	}
	if ReviewDays(45) == nil || ReviewDays(60) != nil {
		t.Error("windows are 30, 60 and 90 days")
	}
}
