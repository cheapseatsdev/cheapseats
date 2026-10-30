package evaluation

import (
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"testing"
)

func TestComparisonGateRejectsDegenerateAndUnresolvedComparisons(t *testing.T) {
	var members []Member
	perfect := map[string]grade.GoldenExpected{}
	absent := map[string]grade.GoldenExpected{}
	classes := []grade.GoldenExpected{{Verdict: grade.Absent}, {Verdict: grade.Absent, OtherProduct: true}, {Verdict: grade.Mentioned, Tone: grade.Neutral}, {Verdict: grade.Mentioned, Tone: grade.Negative}, {Verdict: grade.Recommended, TopPick: true, Tone: grade.Positive}, {Verdict: grade.Recommended, Tone: grade.Mixed}}
	for i, expected := range classes {
		for n := 0; n < 2; n++ {
			id := string(rune('a' + 2*i + n))
			members = append(members, Member{CaseID: id, Record: grade.GoldenRecord{Expected: expected}})
			perfect[id] = expected
			absent[id] = grade.GoldenExpected{Verdict: grade.Absent}
		}
	}
	good, bad := Measure(members, perfect), Measure(members, absent)
	if len(good.Coverage) != 0 || good.MacroRecall == nil || *good.MacroRecall != 1 || len(bad.CriticalFailures) == 0 {
		t.Fatal("invalid gate fixture or lost class checks")
	}
	base := func() ExperimentReport {
		return ExperimentReport{Experiment: Experiment{Plan: ExperimentPlan{Partition: "held-out", Frozen: true}}, Complete: 48, Total: 48, Variants: []VariantReport{{Runs: []Metrics{bad, bad}, RepeatDifferences: 1}, {Runs: []Metrics{good, good}, Improved: 16, Paired: 24}}}
	}
	r := base()
	if issues := ComparisonGate(r, 1); len(issues) != 0 {
		t.Fatalf("conservative prerequisites not met by complete fixture: %v", issues)
	}
	r = base()
	r.Variants[1].Runs = []Metrics{bad, bad}
	r.Variants[1].Improved = 0
	if len(ComparisonGate(r, 1)) == 0 {
		t.Fatal("always-absent candidate cleared gate")
	}
	r = base()
	r.Complete--
	r.Failed = 1
	if len(ComparisonGate(r, 1)) == 0 {
		t.Fatal("missing prediction cleared gate")
	}
	r = base()
	r.Experiment.Plan.Partition = "development"
	if len(ComparisonGate(r, 1)) == 0 {
		t.Fatal("development evidence claimed independent validation")
	}
	r = base()
	r.Variants[1].Regressed = 1
	if len(ComparisonGate(r, 1)) == 0 {
		t.Fatal("unresolved regression cleared gate")
	}
	r = base()
	r.Variants[1].QuoteFailures = 1
	if len(ComparisonGate(r, 1)) == 0 {
		t.Fatal("unsupported quote cleared occurrence prerequisite")
	}
	r = base()
	r.Variants[1].RepeatDifferences = 2
	if len(ComparisonGate(r, 1)) == 0 {
		t.Fatal("increased repeat variation cleared gate")
	}
	toneless := map[string]grade.GoldenExpected{}
	for id, e := range perfect {
		if e.Verdict != grade.Absent {
			e.Tone = ""
		}
		toneless[id] = e
	}
	toneFailure := Measure(members, toneless)
	r = base()
	r.Variants[1].Runs = []Metrics{toneFailure, toneFailure}
	if len(ComparisonGate(r, 1)) == 0 {
		t.Fatal("missing every tone class cleared the review gate")
	}
}
