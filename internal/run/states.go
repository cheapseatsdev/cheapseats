package run

import (
	"context"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/state"
)

// Status is where a run is in its life, as runs.status stores it. The column is TEXT and these
// are its values; docs/states.md draws the moves between them from RunTransitions.
type Status string

const (
	Queued     Status = "queued"    // waiting to run, or interrupted and waiting to resume
	Running    Status = "running"   // a process is answering and grading it
	Done       Status = "done"      // every result graded: it counts in rankings, trends and the fix list
	Partial    Status = "partial"   // ended with results unfinished; Retry completes it
	Failed     Status = "failed"    // a storage failure, or a resume this version can't finish consistently
	Cancelled  Status = "cancelled" // stopped by hand
	OverBudget Status = "budget"    // stopped by the daily budget; Retry tomorrow finishes it
)

// Statuses is every run status, in the order the diagram and the tests read them.
var Statuses = []Status{Queued, Running, Done, Partial, Failed, Cancelled, OverBudget}

// Terminal reports whether the run is over. Retry may start it again (see RunTransitions).
func (s Status) Terminal() bool {
	return s == Done || s == Partial || s == Failed || s == Cancelled || s == OverBudget
}

// InFlight reports whether the run is in progress, so its set can't start another.
func (s Status) InFlight() bool { return s == Queued || s == Running }

// CanBecome reports whether RunTransitions lists the move from s to t.
func (s Status) CanBecome(t Status) bool { return RunTransitions.CanBecome(s, t) }

// RunTransitions is every move the code makes between run statuses, and where it makes it.
var RunTransitions = state.Table[Status]{
	state.Start: {Queued}, // Create
	// execute starts it; Cancel ends one nothing is running; app.Resume fails one this version
	// would ask or grade differently.
	Queued: {Running, Cancelled, Failed},
	// execute's ending. Queued is an interruption: a shutdown records it on the way down, a crash
	// leaves it running and Recover re-queues it on the next start.
	Running: {Done, Partial, OverBudget, Failed, Cancelled, Queued},
	// Retry (Start, Execute) runs a finished run again from where it stopped. The page offers it
	// only with results unfinished; the API takes it for a done run too.
	Done:       {Running},
	Partial:    {Running},
	Failed:     {Running},
	Cancelled:  {Running},
	OverBudget: {Running},
}

// ResultStatus is where one answer is, as results.status stores it.
type ResultStatus string

const (
	ResultPending ResultStatus = "pending" // not asked yet
	ResultRunning ResultStatus = "running" // being answered or graded
	ResultDone    ResultStatus = "done"    // answered and graded
	ResultError   ResultStatus = "error"   // the answer or the grade failed; error says why
	ResultSkipped ResultStatus = "skipped" // the run ended before it, or its answerer has no key
)

// ResultStatuses is every result status, in the order the diagram and the tests read them.
var ResultStatuses = []ResultStatus{ResultPending, ResultRunning, ResultDone, ResultError, ResultSkipped}

// CanBecome reports whether ResultTransitions lists the move from s to t.
func (s ResultStatus) CanBecome(t ResultStatus) bool { return ResultTransitions.CanBecome(s, t) }

// Graded reports whether the result has a grade to count: done, with its grade stored.
func (r Result) Graded() bool { return r.Status == ResultDone && r.Grade != nil }

// ResultTransitions is every move the code makes between result statuses, and where.
var ResultTransitions = state.Table[ResultStatus]{
	state.Start: {ResultPending}, // Create
	// one takes it up, or skips it when its answerer's key is missing; execute skips what the run
	// didn't reach.
	ResultPending: {ResultRunning, ResultSkipped},
	// one records the answer's grade or its failure. Pending: a crash (Recover) or a resume
	// (execute) asks it again. Skipped: the run ended with it unfinished after a storage failure.
	ResultRunning: {ResultDone, ResultError, ResultPending, ResultSkipped},
	// The run's one more try, a resume or a retry asks it again.
	ResultError:   {ResultPending},
	ResultSkipped: {ResultPending},
	// A re-grade rewrites the grade, never the status.
	ResultDone: nil,
}

// Every status write in this package goes through the helpers below: each checks the move against
// its table (state.Table.Check reports a move the table doesn't list) and then writes it as the
// code always has. The SQL guards the data; the table only has to describe it.

// moveRun moves run id to status to, with set (more `, col=?` clauses) and their args. The run's
// status now is read for the check.
func (e *Engine) moveRun(ctx context.Context, id string, to Status, set string, args ...any) error {
	var from Status
	if err := e.DB.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=?`, id).Scan(&from); err == nil {
		RunTransitions.Check("runs", from, to)
	}
	_, err := e.DB.ExecContext(ctx, `UPDATE runs SET status=?`+set+` WHERE id=?`, append(append([]any{to}, args...), id)...)
	return err
}

// moveRunFrom is moveRun for a run in status from only, and reports whether it moved.
func (e *Engine) moveRunFrom(ctx context.Context, id string, from, to Status, set string, args ...any) (bool, error) {
	RunTransitions.Check("runs", from, to)
	res, err := e.DB.ExecContext(ctx, `UPDATE runs SET status=?`+set+` WHERE id=? AND status=?`, append(append([]any{to}, args...), id, from)...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// moveRunsFrom moves every run in one of the from statuses to to.
func (e *Engine) moveRunsFrom(ctx context.Context, from []Status, to Status, set string, args ...any) error {
	args = append([]any{to}, args...)
	for _, f := range from {
		RunTransitions.Check("runs", f, to)
		args = append(args, f)
	}
	_, err := e.DB.ExecContext(ctx, `UPDATE runs SET status=?`+set+` WHERE status IN (`+marks(len(from))+`)`, args...)
	return err
}

// moveResult moves result rid to status to, with set (more `,col=?` clauses) and their args. The
// result's status now is read for the check.
func (e *Engine) moveResult(ctx context.Context, rid int64, to ResultStatus, set string, args ...any) error {
	var from ResultStatus
	if err := e.DB.QueryRowContext(ctx, `SELECT status FROM results WHERE id=?`, rid).Scan(&from); err == nil {
		ResultTransitions.Check("results", from, to)
	}
	_, err := e.DB.ExecContext(ctx, `UPDATE results SET status=?`+set+` WHERE id=?`, append(append([]any{to}, args...), rid)...)
	return err
}

// moveResultsFrom moves every result of run id (of every run, when id is empty) in one of the from
// statuses to to, and reports how many moved.
func (e *Engine) moveResultsFrom(ctx context.Context, id string, from []ResultStatus, to ResultStatus) (int64, error) {
	args, where := []any{to}, ``
	if id != "" {
		args, where = append(args, id), ` run_id=? AND`
	}
	for _, f := range from {
		ResultTransitions.Check("results", f, to)
		args = append(args, f)
	}
	res, err := e.DB.ExecContext(ctx, `UPDATE results SET status=? WHERE`+where+` status IN (`+marks(len(from))+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// marks is n SQL placeholders: `?,?,?`.
func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
