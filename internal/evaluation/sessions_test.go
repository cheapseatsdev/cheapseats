package evaluation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func TestSessionUsesFrozenDevelopmentGroupsAndResumesWithoutSkippingDrafts(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	var data []byte
	for i := 0; i < 50; i++ {
		r := evaluationRecord()
		r.Prompt = fmt.Sprintf("Which tracker for team %d?", i/2)
		r.Answer = fmt.Sprintf("Answer %d", i)
		r.ID = grade.GoldenID(r.Answer)
		r.Set = fmt.Sprintf("set-%d", i%3)
		data = append(data, evaluationJSON(r)...)
		data = append(data, '\n')
	}
	a, err := w.ImportJSONL(ctx, "session fixtures", data)
	if err != nil {
		t.Fatal(err)
	}
	s, err := w.Start(ctx, a.ID, "reviewer", "first")
	if err != nil || len(s.CaseIDs) != 10 {
		t.Fatalf("start: %+v %v", s, err)
	}
	g, err := w.Grouping(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := w.ImportCases(ctx, a.ID)
	for i := 0; i < len(cs); i += 2 {
		if g.Cases[cs[i].ID] != g.Cases[cs[i+1].ID] {
			t.Fatal("related questions split across groups")
		}
	}
	for _, id := range s.CaseIDs {
		if g.Cases[id].Partition != "development" {
			t.Fatal("pilot exposed a held-out case")
		}
	}
	id := s.CaseIDs[0]
	if _, err := w.Save(ctx, id, 0, "draft", Judgment{Note: "unfinished"}, "reviewer", Rubric()); err != nil {
		t.Fatal(err)
	}
	if err := w.Advance(ctx, s.ID, "reviewer", id, 0, true); err != nil {
		t.Fatal(err)
	}
	resumed, err := w.Start(ctx, a.ID, "reviewer", "first")
	if err != nil || resumed.ID != s.ID || resumed.Cursor != 0 {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	if _, err := w.Session(ctx, s.ID, "someone else"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("session disclosed to another actor")
	}
	if err := w.Advance(ctx, s.ID, "reviewer", id, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := w.Advance(ctx, s.ID, "reviewer", id, 0, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cursor: %v", err)
	}
	// Labels and review history cannot reshuffle a partition after annotation.
	if _, err := w.Save(ctx, id, 1, "judgment", Judgment{Verdict: grade.Absent, AbsenceReason: "not-named", FullAnswer: true}, "reviewer", Rubric()); err != nil {
		t.Fatal(err)
	}
	again, err := w.Grouping(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(g, again) {
		t.Fatal("annotation changed the frozen partition")
	}
}

func TestGroupingKeepsNearDuplicateAnswersTogether(t *testing.T) {
	cs := []Case{{ID: "a", Record: evaluationRecord()}, {ID: "b", Record: evaluationRecord()}, {ID: "c", Record: evaluationRecord()}}
	cs[0].Record.Prompt, cs[1].Record.Prompt, cs[2].Record.Prompt = "one", "two", "three"
	cs[0].Record.Answer = "A fairly long answer with many distinct word pairs about the product and its complete onboarding workflow for a small team."
	cs[1].Record.Answer = cs[0].Record.Answer + " Yes."
	cs[2].Record.Answer = "An unrelated short answer."
	g := grouping(cs)
	if g.Cases["a"] != g.Cases["b"] || g.Cases["a"].ID == g.Cases["c"].ID {
		t.Fatalf("groups: %+v", g)
	}
}
