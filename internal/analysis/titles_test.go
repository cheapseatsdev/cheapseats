package analysis

import (
	"context"
	"strings"
	"testing"
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
