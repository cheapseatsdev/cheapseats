package app

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/state"
)

var statesUpdate = flag.Bool("update", false, "rewrite docs/states.md from the transition tables")

// statesDocPath is docs/states.md from this package's directory, where go test runs.
var statesDocPath = filepath.Join("..", "..", "docs", "states.md")

// docs/states.md is drawn from the transition tables, so it can't say something the code doesn't.
// It fails when the file is stale: regenerate it with
//
//	go test ./internal/app -run TestStatesDocIsCurrent -update
func TestStatesDocIsCurrent(t *testing.T) {
	want := statesDoc()
	if *statesUpdate {
		if err := os.WriteFile(statesDocPath, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(statesDocPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("docs/states.md is stale; regenerate it: go test ./internal/app -run TestStatesDocIsCurrent -update")
	}
}

// Every attempt state is in the order the diagram draws, so none of its moves vanish from the doc.
func TestStatesEveryAttemptStateIsDrawn(t *testing.T) {
	for s := range AttemptTransitions {
		if s == newAttempt {
			continue
		}
		found := false
		for _, x := range AttemptStates {
			found = found || x == s
		}
		if !found {
			t.Errorf("attempt state %q is not in AttemptStates", s)
		}
	}
	for s := range budget.Transitions {
		if s == state.Start {
			continue
		}
		found := false
		for _, x := range budget.States {
			found = found || x == s
		}
		if !found {
			t.Errorf("budget state %q is not in budget.States", s)
		}
	}
}

// statesDoc is docs/states.md: the prose here, the diagrams from the tables.
func statesDoc() string {
	var b strings.Builder
	w := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
	}
	w("# States",
		"",
		"How a run, its results, a fix attempt and a budget reservation move between their statuses. The",
		"status columns are text; these are their values, and the arrows are every move the code makes.",
		"",
		"Generated from the transition tables in the code (`run.RunTransitions`, `run.ResultTransitions`,",
		"`app.AttemptTransitions`, `budget.Transitions`) by",
		"`go test ./internal/app -run TestStatesDocIsCurrent -update`; the test fails while this file is",
		"stale. Every status write goes through a helper that checks the move against its table, and the",
		"`run` package's tests fail on a move the table doesn't list.",
		"",
		"## Runs (`runs.status`)",
		"",
		"A run is **queued** when created and **running** while a process answers and grades it; one run",
		"per set is queued or running at a time. It ends **done** (every result graded: it counts in",
		"rankings, trends and the fix list), **partial** (results unfinished after the one more try at",
		"failed answers), **budget** (the daily budget stopped it), **failed** (a storage failure, or a",
		"resume this version would ask or grade differently) or **cancelled**.",
		"",
		"A shutdown records a running run as queued with the error `"+run.Interrupted+"`;",
		"a crash leaves it running, and the next start's Recover re-queues it the same way. Resume takes",
		"queued runs from there, keeping the answers they have. Retry runs an ended run again from where",
		"it stopped; the page offers it when results are unfinished, the API takes it for any run.",
		"",
		strings.TrimRight(run.RunTransitions.Mermaid(run.Statuses), "\n"),
		"",
		"## Results (`results.status`)",
		"",
		"Each answer to ask for is **pending** when the run is created, **running** while it's answered",
		"and graded, then **done** (graded) or **error** (the answer or the grade failed; `error` says",
		"why). What the run ends before reaching is **skipped**, as is an answer whose answerer has no",
		"key. A resume or retry asks again for whatever isn't done; an answer saved but not graded is",
		"graded without being asked again. A re-grade rewrites grades and never a status.",
		"",
		strings.TrimRight(run.ResultTransitions.Mermaid(run.ResultStatuses), "\n"),
		"",
		"## Fix attempts (`fix_attempts.state`)",
		"",
		"Work submitted for a fix is **proposed** (a commit or pull request is up), **accepted** (it",
		"merged) or **withdrawn** (closed, which reopens the fix). An attempt can be recorded in any",
		"state and moved to any other, since a pull request can be reopened or a merge reverted.",
		"",
		strings.TrimRight(AttemptTransitions.Mermaid(AttemptStates), "\n"),
		"",
		"## Budget reservations (`spend_events.state`)",
		"",
		"Every billable call is **reserved** against today's budget before it's made, then **settled** at",
		"its known cost, or left **unknown** at the larger of the cost and the reservation when the cost",
		"can't be known. A process that dies between the two leaves the reservation standing.",
		"",
		strings.TrimRight(budget.Transitions.Mermaid(budget.States), "\n"),
	)
	return b.String()
}
