package evaluation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func TestApprovalAccountsForEveryRowAndFreezesHumanEvidence(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	r := evaluationRecord()
	r.Note = "OLD_MODEL_NOTE"
	r.Evidence = "OLD_MODEL_QUOTE"
	second := evaluationRecord()
	second.Answer = "Another answer"
	second.ID = grade.GoldenID(second.Answer)
	data := append(evaluationJSON(r), evaluationJSON(second)...)
	data = append(data, evaluationJSON(r)...)
	data = append(data, []byte("{bad-json}\n")...)
	a, err := w.ImportJSONL(ctx, "dataset source", data)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	j := Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{r.Answer}, Note: "Explicit human explanation"}
	id := a.Rows[0].CaseID
	other := a.Rows[1].CaseID
	if _, err := w.Save(ctx, id, 0, "judgment", j, "human reviewer", Rubric()); err != nil {
		t.Fatal(err)
	}
	p := Approval{ImportID: a.ID, Name: "pilot", Members: map[string]int{id: 1}, Exclusions: map[string]string{}}
	if _, err := w.Approve(ctx, p, "approver"); err == nil {
		t.Fatal("omitted valid and blocked evidence silently disappeared")
	}
	p.Exclusions[other] = "Not yet reviewed"
	p.Exclusions["line:4"] = "Malformed source row needs repair"
	p.Members[id] = 0
	if _, err := w.Approve(ctx, p, "approver"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale approval: %v", err)
	}
	p.Members[id] = 1
	d, err := w.Approve(ctx, p, "approver")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Members) != 1 || d.TotalCases != 2 || d.TotalRows != 4 || d.Duplicate != 1 || d.Blocked != 1 || len(d.Exclusions) != 2 || d.Members[0].Actor != "human reviewer" || d.Members[0].Revision != 1 {
		t.Fatalf("accounting: %+v", d)
	}
	exported, err := d.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"OLD_MODEL_NOTE", "OLD_MODEL_QUOTE"} {
		if strings.Contains(string(exported), old) {
			t.Fatal("export promoted old model evidence")
		}
	}
	path := filepath.Join(t.TempDir(), "approved.jsonl")
	if err := os.WriteFile(path, exported, 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := grade.ReadGoldenFile(path)
	if err != nil || len(records) != 1 || records[0].LabeledBy != "human" || records[0].Expected != j.Expected() || records[0].Answer != r.Answer || records[0].Evidence != r.Answer {
		t.Fatalf("roundtrip: %+v %v", records, err)
	}
	if _, err := w.Save(ctx, id, 1, "unresolved", Judgment{Issue: "rubric", Note: "Reopened after approval"}, "human reviewer", Rubric()); err != nil {
		t.Fatal(err)
	}
	frozen, err := w.Dataset(ctx, d.ID)
	if err != nil || frozen.Members[0].Revision != 1 || frozen.Members[0].Record.Expected != j.Expected() {
		t.Fatal("editing current review rewrote a frozen benchmark")
	}
	p.Members[id] = 2
	if _, err := w.Approve(ctx, p, "approver"); err == nil {
		t.Fatal("unresolved member approved")
	}
}

func TestApprovalRefusesLegacyAnswerIDCollisionAcrossContexts(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	r := evaluationRecord()
	other := r
	other.Brand.Name = "Different Tracker"
	data := append(evaluationJSON(r), '\n')
	data = append(data, evaluationJSON(other)...)
	a, err := w.ImportJSONL(ctx, "contexts", data)
	if err != nil {
		t.Fatal(err)
	}
	p := Approval{ImportID: a.ID, Name: "contexts", Members: map[string]int{}}
	for _, row := range a.Rows {
		if _, err := w.Save(ctx, row.CaseID, 0, "judgment", Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true}, "human reviewer", Rubric()); err != nil {
			t.Fatal(err)
		}
		p.Members[row.CaseID] = 1
	}
	if _, err := w.Approve(ctx, p, "approver"); err == nil || !strings.Contains(err.Error(), "two contexts") {
		t.Fatalf("lossy legacy export allowed: %v", err)
	}
}

func TestClassDiagnosticsRejectComfortableAlwaysAbsentAccuracy(t *testing.T) {
	var members []Member
	guesses := map[string]grade.GoldenExpected{}
	for i := 0; i < 80; i++ {
		id := string(rune('a' + i))
		e := grade.GoldenExpected{Verdict: grade.Absent}
		if i < 4 {
			e.Verdict = grade.Recommended
			e.TopPick = true
			e.Tone = grade.Positive
		} else if i < 6 {
			e.Verdict = grade.Mentioned
			e.Tone = grade.Negative
		}
		members = append(members, Member{CaseID: id, Record: grade.GoldenRecord{Expected: e}})
		guesses[id] = grade.GoldenExpected{Verdict: grade.Absent}
	}
	m := Measure(members, guesses)
	if m.VerdictCorrect != 74 || m.Cases != 80 || m.Gate != "fails critical class checks" || len(m.CriticalFailures) < 2 {
		t.Fatalf("always absent looked reassuring: %+v", m)
	}
	delete(guesses, members[0].CaseID)
	m = Measure(members, guesses)
	if m.Cases != 80 || m.Predictions != 79 || m.Missing != 1 || m.Gate != "incomplete predictions; no release verdict" {
		t.Fatalf("missing result dropped or guessed: %+v", m)
	}
	perfect := map[string]grade.GoldenExpected{}
	for _, m := range members {
		perfect[m.CaseID] = m.Record.Expected
	}
	m = Measure(members, perfect)
	if !strings.Contains(m.Gate, "insufficient coverage") {
		t.Fatalf("perfect accuracy concealed absent namesake/tone coverage: %+v", m)
	}
}
