package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

func evaluationLegacySessionFixture(t *testing.T, w *Workbench) (Import, []string, string) {
	t.Helper()
	r := evaluationRecord()
	other := r
	other.Answer += " A different saved answer."
	other.ID = grade.GoldenID(other.Answer)
	a, err := w.ImportJSONL(context.Background(), "legacy sessions", append(evaluationJSON(r), evaluationJSON(other)...))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{a.Rows[0].CaseID, a.Rows[1].CaseID}
	raw, _ := json.Marshal(ids)
	return a, ids, string(raw)
}

func TestLegacyDuplicateSessionRepairPreservesFurthestCursorAndHistory(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	a, ids, raw := evaluationLegacySessionFixture(t, w)
	for _, s := range []struct {
		id, actor, state string
		cursor           int
		updated          string
	}{
		{"progressed", "human", "paused", 1, "2026-01-01T10:10:00Z"},
		{"leftover", "human", "active", 0, "2026-01-01T10:11:00Z"},
		{"another-actor", "other", "active", 0, "2026-01-01T10:12:00Z"},
	} {
		if _, err := w.DB.Exec(`INSERT INTO evaluation_sessions VALUES (?,?,?,?,?,?,?,?)`, s.id, a.ID, s.actor, raw, s.cursor, s.state, "2026-01-01T10:00:00Z", s.updated); err != nil {
			t.Fatal(err)
		}
	}
	s, err := w.Start(ctx, a.ID, "human", "first")
	if err != nil || s.ID != "progressed" || s.Cursor != 1 {
		t.Fatalf("repair discarded progress: %+v %v", s, err)
	}
	retired, err := w.Session(ctx, "leftover", "human")
	if err != nil || retired.State != "done" || retired.Cursor != 0 || len(retired.CaseIDs) != 2 {
		t.Fatalf("duplicate row or original cursor lost: %+v %v", retired, err)
	}
	if err := w.Advance(ctx, retired.ID, "human", ids[0], 0, false); !errors.Is(err, ErrConflict) {
		t.Fatal("retired bookmark reactivated a duplicate session", err)
	}
	if err := w.Advance(ctx, s.ID, "human", ids[1], 1, false); err != nil {
		t.Fatal(err)
	}
	if leftover, err := w.Resume(ctx, a.ID, "human"); err != nil || leftover != nil {
		t.Fatalf("duplicate resurfaced after completion: %+v %v", leftover, err)
	}
	other, err := w.Session(ctx, "another-actor", "other")
	if err != nil || other.State != "active" {
		t.Fatal("repair changed another reviewer's session", err)
	}
	var rows, events int
	w.DB.QueryRow(`SELECT count(*) FROM evaluation_sessions`).Scan(&rows)
	w.DB.QueryRow(`SELECT count(*) FROM evaluation_events`).Scan(&events)
	if rows != 3 || events != 0 {
		t.Fatalf("repair deleted sessions or invented decisions: rows=%d events=%d", rows, events)
	}
}

func TestLegacyCompletedDuplicateDoesNotHideLaterIntentionalReview(t *testing.T) {
	w := evaluationWorkbench(t)
	ctx := context.Background()
	a, _, raw := evaluationLegacySessionFixture(t, w)
	for _, s := range []struct {
		id, state, created, updated string
		cursor                      int
	}{
		{"finished", "done", "2026-01-01T10:00:00Z", "2026-01-01T11:00:00Z", 2},
		{"leftover", "active", "2026-01-01T10:01:00Z", "2026-01-01T10:01:00Z", 0},
	} {
		if _, err := w.DB.Exec(`INSERT INTO evaluation_sessions VALUES (?,?,?,?,?,?,?,?)`, s.id, a.ID, "human", raw, s.cursor, s.state, s.created, s.updated); err != nil {
			t.Fatal(err)
		}
	}
	if s, err := w.Resume(ctx, a.ID, "human"); err != nil || s != nil {
		t.Fatalf("finished duplicate was offered again: %+v %v", s, err)
	}
	if _, err := w.DB.Exec(`INSERT INTO evaluation_sessions VALUES ('intentional',?,'human',?,0,'paused','2026-01-01T12:00:00Z','2026-01-01T12:00:00Z')`, a.ID, raw); err != nil {
		t.Fatal(err)
	}
	s, err := w.Start(ctx, a.ID, "human", "attention")
	if err != nil || s.ID != "intentional" || s.State != "paused" {
		t.Fatalf("repair retired a later intentional review: %+v %v", s, err)
	}
	old, err := w.Session(ctx, "leftover", "human")
	if err != nil || old.State != "done" || old.Cursor != 0 {
		t.Fatalf("existing completed duplicate was not retired: %+v %v", old, err)
	}
}
