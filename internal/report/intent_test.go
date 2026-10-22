package report

import (
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// An info prompt's answers count toward named, and not toward recommended or top pick: a good
// explanation that recommends nothing isn't a loss.
func TestInfoPromptsCountOnlyTowardNamed(t *testing.T) {
	brand := config.Brand{Name: "Cheap Seats"}
	res := []run.Result{
		{PromptIdx: 0, Answerer: "a", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Recommended, TopPick: ptr("Cheap Seats")}},
		{PromptIdx: 1, Answerer: "a", Track: answer.Search, Status: "done", Info: true, Grade: &grade.Grade{Verdict: grade.Mentioned}},
		{PromptIdx: 2, Answerer: "a", Track: answer.Search, Status: "done", Info: true, Grade: &grade.Grade{Verdict: grade.Absent}},
	}
	s := Build(brand, res)
	if o := s.Overall; o.Visible.Hits != 2 || o.Visible.N != 3 || o.Recommended.Hits != 1 || o.Recommended.N != 1 || o.TopPick.N != 1 {
		t.Fatalf("%+v", o)
	}
	if s.Prompts[0].Info || !s.Prompts[1].Info || s.Prompts[1].Rates.Recommended.N != 0 {
		t.Fatalf("%+v", s.Prompts)
	}
}
