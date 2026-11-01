package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// An analyst titled a market finding "x" in production, and the card showed just that. A title
// that can't name its item drops the item, with a note in the insights that keeps its substance.
func TestDegenerateTitlesAreOmittedWithAReason(t *testing.T) {
	cfg, set, f, _, _ := verificationFixture()
	finding := Finding{Title: "Buyers compare on setup time", Detail: "Answers weigh how long setup takes.", Evidence: "Result 7", Prompts: []int{1}}
	inv := Investigation{Title: "Check setup route", Why: "Startup users cannot follow a retired documentation link.", Check: f.Check, Decision: f.Decision, Evidence: f.Evidence, Prompts: []int{1}, Target: f.Target}
	a := Analysis{Investigations: []Investigation{inv}, Findings: []Finding{finding}}
	for _, title := range []string{"x", "  ", "TBD", "Finding:", "Pricing", "AI UX"} {
		bad := finding
		bad.Title, bad.Detail = title, "Detail of "+title
		a.Findings = append(a.Findings, bad)
		badInv := inv
		badInv.Title = title
		a.Investigations = append(a.Investigations, badInv)
	}
	normalizeNotes(set, &a)
	if len(a.Findings) != 1 || a.Findings[0].Title != finding.Title || len(a.Investigations) != 1 || a.Investigations[0].Title != inv.Title {
		t.Fatalf("degenerate titles kept or good ones lost: %+v %+v", a.Findings, a.Investigations)
	}
	for _, want := range []string{`Omitted market finding "x": Its title is too short to say what it is. It said: Detail of x`, `Omitted market finding "TBD": Its title is a placeholder.`, `Omitted investigation "Pricing": Its title is too short`} {
		if !strings.Contains(a.Insights, want) {
			t.Errorf("insights lack %q:\n%s", want, a.Insights)
		}
	}

	f.Title = "x"
	got, _, err := verifyCandidates(context.Background(), cfg, set, Analysis{Fixes: []Fix{f}}, 0)
	if err != nil || len(got.Fixes)+len(got.Investigations) != 0 || !strings.Contains(got.Insights, `Omitted candidate "x": Its title is too short to say what it is. It claimed: `+f.Claim) {
		t.Fatalf("a candidate titled %q survived: %+v %v", f.Title, got, err)
	}
}

// A rebuild once got empty insights and one untitled candidate back. Verification omitted the
// candidate and the empty result was stored as a success over a good analysis. Output that thin
// is an error now, before the verification call.
func TestDegenerateAnalystOutputIsAnError(t *testing.T) {
	cfg, set, f, _, _ := verificationFixture()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	var reply Analysis
	calls := 0
	structuredCLI = func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
		calls++
		b, _ := json.Marshal(reply)
		return json.Unmarshal(b, out)
	}
	results := []run.Result{{ID: 7, PromptIdx: 0, Answerer: "fake", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Absent}, Answer: &answer.Answer{Text: "An answer."}}}
	untitled := f
	untitled.Title = ""
	insights := "Answers favour tools with a free tier; result 7 names two."
	finding := Finding{Title: "Buyers compare on setup time", Detail: "d", Evidence: "e", Prompts: []int{1}}
	for name, c := range map[string]struct {
		reply Analysis
		want  string
	}{
		"the production case":     {Analysis{Fixes: []Fix{untitled}}, "insights are 0 characters"},
		"near-empty insights":     {Analysis{Insights: "  x ", Findings: []Finding{finding}}, "insights are 1 characters"},
		"only untitled items":     {Analysis{Insights: insights, Fixes: []Fix{untitled}, Findings: []Finding{{Title: "x", Prompts: []int{1}}}}, "none of its 2 items has a usable title"},
		"one bad title among ok":  {Analysis{Insights: insights, Findings: []Finding{finding, {Title: "x", Prompts: []int{1}}}}, ""},
		"no items, real insights": {Analysis{Insights: insights}, ""},
	} {
		reply, calls = c.reply, 0
		_, _, err := Build(context.Background(), cfg, set, results, nil, nil)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if !errors.Is(err, ErrDegenerate) || !strings.Contains(err.Error(), c.want) || calls != 1 {
			t.Errorf("%s: want %q before verification, got %v after %d calls", name, c.want, err, calls)
		}
	}
}
