package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

func evaluationWorkbench(t *testing.T) *Workbench {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db.DB)
}

func evaluationRecord() grade.GoldenRecord {
	answer := "Use **Cheap Seats** for this small team's tracker."
	return grade.GoldenRecord{ID: grade.GoldenID(answer), Set: "s", PromptID: "choose", Prompt: "Which tracker should a small team use?", Brand: grade.GoldenBrand{Name: "Cheap Seats"}, Answer: answer, Expected: grade.GoldenExpected{Verdict: grade.Recommended, TopPick: true}, LabeledBy: "claude"}
}

func evaluationJSON(r grade.GoldenRecord) []byte { b, _ := json.Marshal(r); return append(b, '\n') }

func TestImportPreservesBlockedRowsAndContextWithoutTrustingModelLabels(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	r := evaluationRecord()
	other := r
	other.Prompt = "Which tracker should a large organization use?"
	other.LabeledBy = "human"
	data := append(evaluationJSON(r), evaluationJSON(r)...)
	data = append(data, []byte("not json\n")...)
	data = append(data, evaluationJSON(other)...)
	a, err := w.ImportJSONL(ctx, "public fixtures", data)
	if err != nil || a.Cases != 2 || a.Duplicate != 1 || a.Blocked != 1 || len(a.Rows) != 4 {
		t.Fatalf("import: %+v %v", a, err)
	}
	if a.Rows[0].CaseID == a.Rows[3].CaseID {
		t.Fatal("same answer under a different question collapsed")
	}
	again, err := w.ImportJSONL(ctx, "public fixtures", data)
	if err != nil || again.ID != a.ID || len(again.Rows) != 4 {
		t.Fatalf("repeat import: %+v %v", again, err)
	}
	c, err := w.Case(ctx, a.Rows[0].CaseID)
	if err != nil || c.FirstReviewed || c.Review.State != "unreviewed" || c.Record.Expected != r.Expected || c.Record.Answer != r.Answer {
		t.Fatalf("model labels changed review state or evidence: %+v %v", c, err)
	}
	// Another proposal for the same saved evidence cannot overwrite its import.
	r.Expected.Verdict, r.Expected.TopPick = grade.Absent, false
	b, err := w.ImportJSONL(ctx, "later model proposal", evaluationJSON(r))
	if err != nil || b.Rows[0].CaseID != c.ID {
		t.Fatalf("proposed label changed case identity: %+v %v", b, err)
	}
	c, _ = w.Case(ctx, c.ID)
	if c.Record.Expected.Verdict != grade.Recommended {
		t.Fatal("new model proposal overwrote the original snapshot")
	}
	for _, table := range []string{"runs", "results", "spend_events", "evaluation_events"} {
		var n int
		if err := w.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("import changed %s: %d %v", table, n, err)
		}
	}
}

func TestBundledImportContainsAllOriginalRecordsWithoutHumanApproval(t *testing.T) {
	w := evaluationWorkbench(t)
	b, err := grade.BundledGolden()
	if err != nil {
		t.Fatal(err)
	}
	a, err := w.ImportJSONL(context.Background(), "bundled golden", b)
	if err != nil || a.Cases != len(strings.FieldsFunc(string(b), func(r rune) bool { return r == '\n' })) || a.Blocked != 0 {
		t.Fatalf("bundled import: %+v %v", a, err)
	}
	cs, err := w.Cases(context.Background())
	if err != nil || len(cs) != a.Cases {
		t.Fatalf("cases: %d %v", len(cs), err)
	}
	for _, c := range cs {
		if c.FirstReviewed || c.Review.Revision != 0 {
			t.Fatal("import pretended a model label was a human review")
		}
		encoded, _ := json.Marshal(c)
		if strings.Contains(string(encoded), "expected") || strings.Contains(string(encoded), c.Record.Note) && c.Record.Note != "" {
			t.Fatal("default case JSON disclosed the imported label or its note")
		}
	}
}

func TestDraftCommitConflictAndUnresolvedKeepIndependentHistory(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	a, _ := w.ImportJSONL(ctx, "fixture", evaluationJSON(evaluationRecord()))
	id := a.Rows[0].CaseID
	if _, err := w.Save(ctx, id, 0, "draft", Judgment{}, "reviewer", "earlier-rubric"); err == nil {
		t.Fatal("a decision made under old rules was silently stamped with the current rubric")
	}
	draft, err := w.Save(ctx, id, 0, "draft", Judgment{Evidence: []string{"unfinished quote"}}, "reviewer", Rubric())
	if err != nil || draft.State != "draft" || draft.Revision != 1 {
		t.Fatalf("draft: %+v %v", draft, err)
	}
	c, _ := w.Case(ctx, id)
	if c.FirstReviewed {
		t.Fatal("autosave counted as a human judgment")
	}
	yes := true
	j := Judgment{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{"Use Cheap Seats for this small team's tracker."}}
	if _, err := w.Save(ctx, id, 0, "judgment", j, "stale tab", Rubric()); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save: %v", err)
	}
	saved, err := w.Save(ctx, id, 1, "judgment", j, "reviewer", Rubric())
	if err != nil || saved.Revision != 2 || saved.Judgment.Evidence[0] != evaluationRecord().Answer {
		t.Fatalf("commit: %+v %v", saved, err)
	}
	if j.Evidence[0] != "Use Cheap Seats for this small team's tracker." {
		t.Fatal("validation changed the caller's evidence slice")
	}
	if _, err := w.Save(ctx, id, 2, "unresolved", Judgment{Issue: "rubric", Note: "A later decision needs a clearer recommendation rule."}, "reviewer", Rubric()); err != nil {
		t.Fatal(err)
	}
	c, _ = w.Case(ctx, id)
	h, err := w.History(ctx, id)
	if err != nil || !c.FirstReviewed || c.Review.State != "unresolved" || len(h) != 3 || h[1].Judgment.Expected() != j.Expected() || h[1].Rubric != Rubric() {
		t.Fatalf("history: %+v %+v %v", c, h, err)
	}
	var rubricText string
	if err := w.DB.QueryRow(`SELECT system FROM evaluation_rubrics WHERE id=?`, h[1].Rubric).Scan(&rubricText); err != nil || rubricText != grade.System {
		t.Fatalf("the review's exact rules were not retained: %q %v", rubricText, err)
	}
}

func TestCommitRejectsMissingOrContradictoryEvidence(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	a, _ := w.ImportJSONL(ctx, "fixture", evaluationJSON(evaluationRecord()))
	id := a.Rows[0].CaseID
	yes := true
	for _, j := range []Judgment{
		{Verdict: grade.Recommended, Tone: grade.Positive, Evidence: []string{evaluationRecord().Answer}},
		{Verdict: grade.Mentioned, TopPick: &yes, Tone: grade.Neutral, Evidence: []string{evaluationRecord().Answer}},
		{Verdict: grade.Recommended, TopPick: &yes, Tone: grade.Positive, Evidence: []string{"An invented endorsement."}},
		{Verdict: grade.Absent, AbsenceReason: "not-named"},
		{Verdict: grade.Absent, AbsenceReason: "echo", FullAnswer: true},
		{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true, Incomplete: true},
	} {
		if _, err := w.Save(ctx, id, 0, "judgment", j, "reviewer", Rubric()); err == nil {
			t.Fatalf("bad judgment committed: %+v", j)
		}
	}
	c, _ := w.Case(ctx, id)
	if c.Review.Revision != 0 || c.FirstReviewed {
		t.Fatal("invalid save changed state")
	}
	if _, err := w.Save(ctx, id, 0, "judgment", Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true}, "reviewer", Rubric()); err != nil {
		t.Fatal(err)
	}
}

func TestSimultaneousTabsCannotOverwriteACommittedRevision(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	a, _ := w.ImportJSONL(ctx, "fixture", evaluationJSON(evaluationRecord()))
	id := a.Rows[0].CaseID
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, actor := range []string{"first tab", "second tab"} {
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			<-start
			_, err := w.Save(ctx, id, 0, "draft", Judgment{Note: actor}, actor, Rubric())
			errs <- err
		}(actor)
	}
	close(start)
	wg.Wait()
	close(errs)
	ok, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("successful=%d conflicts=%d", ok, conflicts)
	}
	h, _ := w.History(ctx, id)
	if len(h) != 1 {
		t.Fatalf("%d history events for one winning save", len(h))
	}
}
