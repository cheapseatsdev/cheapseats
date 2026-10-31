package evaluation

import "fmt"

// ComparisonGate is a conservative prerequisite for human release review, not
// automatic release authorization or a claim of statistical reliability.
func ComparisonGate(r ExperimentReport, candidate int) []string {
	issues := []string{}
	if candidate < 1 || candidate >= len(r.Variants) {
		return []string{"choose a candidate variant, not the production control"}
	}
	if r.Complete != r.Total || r.Failed > 0 {
		issues = append(issues, "all planned assessments must complete without failed or missing predictions")
	}
	if r.Experiment.Plan.Partition != "held-out" || !r.Experiment.Plan.Frozen {
		issues = append(issues, "independent validation requires frozen candidates on untouched held-out evidence")
	}
	v := r.Variants[candidate]
	for i, m := range v.Runs {
		missedClass := false
		for _, c := range m.Classes {
			if c.Reference > 0 && c.Correct == 0 {
				missedClass = true
			}
		}
		if len(m.Coverage) > 0 || len(m.CriticalFailures) > 0 || m.Missing > 0 || missedClass {
			issues = append(issues, fmt.Sprintf("repetition %d has insufficient coverage, missed critical classes or missing predictions", i+1))
		}
	}
	if v.QuoteFailures > 0 {
		issues = append(issues, "non-absent assessments have missing or invalid supporting quotes")
	}
	if v.Regressed > 0 {
		issues = append(issues, "paired regressions require human review and a new resolved comparison")
	}
	if v.Improved == 0 {
		issues = append(issues, "this comparison has not demonstrated a grading improvement over the control")
	}
	if v.RepeatDifferences > r.Variants[0].RepeatDifferences {
		issues = append(issues, "candidate repeat variation exceeds the control; reliability remains unresolved")
	}
	return issues
}
