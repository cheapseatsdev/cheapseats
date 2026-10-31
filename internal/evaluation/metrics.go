package evaluation

import (
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"strings"
)

// Counts retain denominators and missing predictions. A provider failure is
// never converted to an absent prediction or dropped from the benchmark.
type ClassMetric struct {
	Field     string `json:"field"`
	Class     string `json:"class"`
	Reference int    `json:"reference"`
	Predicted int    `json:"predicted"`
	Correct   int    `json:"correct"`
}

func (m ClassMetric) Precision() *float64 {
	if m.Reference < 2 || m.Predicted < 2 {
		return nil
	}
	v := float64(m.Correct) / float64(m.Predicted)
	return &v
}
func (m ClassMetric) Recall() *float64 {
	if m.Reference < 2 {
		return nil
	}
	v := float64(m.Correct) / float64(m.Reference)
	return &v
}

type Metrics struct {
	Cases            int                       `json:"cases"`
	Predictions      int                       `json:"predictions"`
	Missing          int                       `json:"missing"`
	VerdictCorrect   int                       `json:"verdict_correct"`
	Classes          []ClassMetric             `json:"classes"`
	Confusion        map[string]map[string]int `json:"verdict_confusion"`
	Coverage         []string                  `json:"insufficient_coverage"`
	CriticalFailures []string                  `json:"critical_failures"`
	Gate             string                    `json:"gate"`
	MacroRecall      *float64                  `json:"verdict_macro_recall,omitempty"`
}

func Measure(ms []Member, predictions map[string]grade.GoldenExpected) Metrics {
	out := Metrics{Cases: len(ms), Classes: []ClassMetric{}, Confusion: map[string]map[string]int{}, Coverage: []string{}, CriticalFailures: []string{}}
	expected := map[string]map[string]int{}
	predicted := map[string]map[string]int{}
	correct := map[string]map[string]int{}
	for _, field := range []string{"verdict", "top_pick", "namesake_only", "tone"} {
		expected[field] = map[string]int{}
		predicted[field] = map[string]int{}
		correct[field] = map[string]int{}
	}
	labels := func(e grade.GoldenExpected) map[string]string {
		fields := map[string]string{"verdict": string(e.Verdict), "top_pick": "false", "namesake_only": "false"}
		if e.TopPick {
			fields["top_pick"] = "true"
		}
		if e.OtherProduct {
			fields["namesake_only"] = "true"
		}
		if e.Verdict != grade.Absent {
			fields["tone"] = string(e.Tone)
		}
		return fields
	}
	for _, m := range ms {
		want := m.Record.Expected
		got, ok := predictions[m.CaseID]
		target := labels(want)
		actual := labels(got)
		for field, class := range target {
			expected[field][class]++
		}
		name := "missing"
		if ok {
			out.Predictions++
			name = string(got.Verdict)
			for field, class := range actual {
				predicted[field][class]++
			}
			if got.Verdict == want.Verdict {
				out.VerdictCorrect++
			}
			for field, class := range target {
				if actual[field] == class {
					correct[field][class]++
				}
			}
		} else {
			out.Missing++
		}
		if out.Confusion[string(want.Verdict)] == nil {
			out.Confusion[string(want.Verdict)] = map[string]int{}
		}
		out.Confusion[string(want.Verdict)][name]++
	}
	// Coverage requirements are deliberately conservative diagnostics, not a
	// statistical power claim. Two examples are a minimum floor, not reliability.
	for _, field := range []string{"verdict", "top_pick", "namesake_only", "tone"} {
		classes := []string{"false", "true"}
		if field == "verdict" {
			classes = []string{"absent", "mentioned", "recommended"}
		}
		if field == "tone" {
			classes = []string{"positive", "neutral", "mixed", "negative"}
		}
		for _, class := range classes {
			row := ClassMetric{Field: field, Class: class, Reference: expected[field][class], Predicted: predicted[field][class], Correct: correct[field][class]}
			out.Classes = append(out.Classes, row)
			if row.Reference < 2 {
				out.Coverage = append(out.Coverage, field+"="+class)
			}
			// A zero-recall positive class cannot pass a release gate behind a high
			// overall accuracy. Tone and all other errors remain visible separately.
			if row.Reference > 0 && row.Correct == 0 && (field == "verdict" && class != "absent" || field != "verdict" && field != "tone" && class == "true") {
				out.CriticalFailures = append(out.CriticalFailures, field+"="+class)
			}
		}
	}
	out.Gate = "diagnostic only; release acceptance is not established"
	macro, covered := 0.0, true
	for _, class := range []string{"absent", "mentioned", "recommended"} {
		n := expected["verdict"][class]
		if n < 2 {
			covered = false
			break
		}
		macro += float64(correct["verdict"][class]) / float64(n) / 3
	}
	if covered {
		out.MacroRecall = &macro
	}
	if out.Missing > 0 {
		out.Gate = "incomplete predictions; no release verdict"
	} else if len(out.CriticalFailures) > 0 {
		out.Gate = "fails critical class checks"
	} else if len(out.Coverage) > 0 {
		out.Gate = "insufficient coverage; no release verdict"
	}
	return out
}

type Baseline struct {
	Name    string
	Metrics Metrics
}

func Baselines(d Dataset) []Baseline {
	absent := map[string]grade.GoldenExpected{}
	lexical := map[string]grade.GoldenExpected{}
	for _, m := range d.Members {
		absent[m.CaseID] = grade.GoldenExpected{Verdict: grade.Absent}
		guess := grade.GoldenExpected{Verdict: grade.Absent}
		answer := strings.Join(words(m.Record.Answer), " ")
		for _, name := range append([]string{m.Record.Brand.Name}, m.Record.Brand.Aliases...) {
			normalized := strings.Join(words(name), " ")
			if normalized != "" && strings.Contains(" "+answer+" ", " "+normalized+" ") {
				guess.Verdict = grade.Mentioned
				guess.Tone = grade.Neutral
				break
			}
		}
		lexical[m.CaseID] = guess
	}
	return []Baseline{{"Always absent", Measure(d.Members, absent)}, {"Lexical presence (mentioned, neutral when a normalized name occurs)", Measure(d.Members, lexical)}}
}
