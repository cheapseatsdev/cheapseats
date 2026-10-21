package app

import (
	"testing"

	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/grade"
	"github.com/metcalfc/popcorn/internal/run"
	"github.com/metcalfc/popcorn/internal/sets"
)

// A prompt is scored with the intent it had when its run was made, and a day where a prompt's
// intent changed says so and leaves that prompt out of the day-over-day comparison.
func TestIntentIsFrozenAndChangesAreMarked(t *testing.T) {
	brand := config.Brand{Name: "Acme"}
	mk := func(id string, howInfo bool) *run.Run {
		return &run.Run{ID: id, Method: "m", Snapshot: run.Snapshot{Set: sets.Set{Prompts: []sets.Prompt{
			{ID: "which", Text: "Which?"}, {ID: "how", Text: "How?", Intent: map[bool]string{true: sets.Info}[howInfo]}}}}}
	}
	results := func() []run.Result {
		return []run.Result{
			{PromptIdx: 0, PromptID: "which", Prompt: "Which?", Answerer: "a", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Recommended}},
			{PromptIdx: 1, PromptID: "how", Prompt: "How?", Answerer: "a", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Mentioned}},
		}
	}
	day := func(r *run.Run, date string) dayData {
		res := results()
		r.Intents(res)
		return newDay(r, brand, date, res)
	}
	prev, d := day(mk("r1", false), "2026-10-20"), day(mk("r2", true), "2026-10-21")
	if prev.Mean.Recommended != 0.5 || d.Mean.Recommended != 1 || d.Mean.Visible != 1 {
		t.Fatalf("means: before %+v, after %+v", prev.Mean, d.Mean)
	}
	compare(&d, &prev)
	if d.Change == nil || len(d.Change.Intent) != 1 || d.Change.Intent[0] != "how" || !d.Change.PromptsChanged() {
		t.Fatalf("change: %+v", d.Change)
	}
	if d.Compare.Common != 1 {
		t.Errorf("a prompt whose intent changed isn't comparable: %+v", d.Compare)
	}
}
