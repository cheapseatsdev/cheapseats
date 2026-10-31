package app

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
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
		"How runs, results, fix attempts, budget reservations and evaluation checks move between statuses. The",
		"status columns are text; these are their values, and the arrows are every move the code makes.",
		"",
		"Generated from the transition tables in the code (`run.RunTransitions`, `run.ResultTransitions`,",
		"`app.AttemptTransitions`, `budget.Transitions`, `evaluation.CheckTransitions`, `evaluation.ReviewTransitions`) by",
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
		"state and moved to any other, since a pull request can be reopened or a merge reverted. A fix",
		"the team won't do is **dismissed**, with the reason as its note and no link; a dismissal can",
		"only be withdrawn, which undoes it, and merged work must be withdrawn before its fix is dismissed.",
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
		"",
		"## Independent checks (`evaluation_checks.state`)",
		"",
		"An explicit request creates a **ready** check. A worker claims it as **running**, then records",
		"**done** with a model assessment or **failed** without a fabricated prediction. Done results",
		"are reused. A failed check can be retried explicitly once; a crashed running worker can be",
		"reclaimed after its lease expires. The lease fences late results, and the fixed admission",
		"budget covers both assessment attempts and their transport retries. Human judgments and",
		"resolution events are stored separately; none of these states is human approval.",
		"Cancellation, timeout and exhausted admission budgets return work to **ready** without using",
		"a logical attempt. Recorded provider charges remain. Expired running checks are unknown",
		"failures for approval, even after a crash on the second attempt; late results are fenced.",
		"",
		strings.TrimRight(evaluation.CheckTransitions.Mermaid(evaluation.CheckStates), "\n"),
		"",
		"Comparison predictions (`evaluation_predictions.state`) use the same check lifecycle, with",
		"each case, grader and repetition claimed and saved separately. Done predictions survive resume.",
		"",
		"## Independent check batches (`evaluation_check_batches.state`)",
		"",
		"A confirmed set starts **ready**, runs under a renewable fenced lease, and becomes **done**",
		"after its sweep, **ready** when paused or **failed** after another worker error. Done means the sweep finished, not that",
		"every individual assessment succeeded. Failed items remain unknown. An explicit resume can",
		"retry failed items while preserving completed checks, shared budgets and per-check limits.",
		"Batch items use the independent-check lifecycle. These states never approve human labels.",
		"",
		strings.TrimRight(evaluation.CheckBatchTransitions.Mermaid(evaluation.CheckStates), "\n"),
		"",
		"## Evaluation decisions (`evaluation_reviews.state`)",
		"",
		"A human decision starts as **draft**, **reviewed** or **unresolved**. Draft is unfinished work,",
		"reviewed is an explicit validated judgment, and unresolved records the decision still needed.",
		"Each change appends history and requires the previous revision. Comparison reviews use reviewed",
		"or unresolved in `evaluation_experiment_reviews.state`; their unfinished edits stay in the",
		"browser rather than autosaving over the human reference. Approved dataset versions stay frozen.",
		"",
		strings.TrimRight(evaluation.ReviewTransitions.Mermaid(evaluation.ReviewStates), "\n"),
	)
	return b.String()
}
