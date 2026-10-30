package run

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/state"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// Every status move the engine makes during this package's tests is checked against its table,
// and one the tables don't list fails the whole run. So a new UPDATE path has to be written into
// the table, and the tables never drift from the code they describe.
func TestMain(m *testing.M) {
	var mu sync.Mutex
	var unlisted []string
	state.Unlisted = func(machine, from, to string) {
		mu.Lock()
		defer mu.Unlock()
		unlisted = append(unlisted, fmt.Sprintf("%s: %s → %s", machine, from, to))
	}
	code := m.Run()
	if len(unlisted) > 0 {
		fmt.Fprintf(os.Stderr, "status moves the tables don't list:\n  %s\n", strings.Join(unlisted, "\n  "))
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// statesReachedBy pairs every move in the run and result tables with the code that makes it. A
// move listed here and not in a table is a lie about the code; one in a table and not here is in
// the table for no reason (a move the code makes but neither lists fails TestMain).
var statesReachedBy = map[string]string{
	"runs: start → queued":       "Engine.Create",
	"runs: queued → running":     "Engine.execute, from Start, Execute or app.Resume",
	"runs: queued → cancelled":   "Engine.Cancel, when nothing is running it",
	"runs: queued → failed":      "Engine.Fail, from app.Resume when this version asks or grades differently",
	"runs: running → done":       "Engine.execute: every result graded",
	"runs: running → partial":    "Engine.execute: results unfinished after the one more try",
	"runs: running → failed":     "Engine.execute: a storage failure",
	"runs: running → cancelled":  "Engine.execute: its context cancelled by Engine.Cancel",
	"runs: running → budget":     "Engine.execute: the daily budget stopped it",
	"runs: running → queued":     "Engine.execute on shutdown (Stop), or Engine.Recover after a crash",
	"runs: done → running":       "Engine.Start from the API's retry, which doesn't check for unfinished results",
	"runs: partial → running":    "Retry: Engine.Start or Execute",
	"runs: failed → running":     "Retry: Engine.Start or Execute",
	"runs: cancelled → running":  "Retry: Engine.Start or Execute",
	"runs: budget → running":     "Retry: Engine.Start or Execute, another day",
	"results: start → pending":   "Engine.Create",
	"results: pending → running": "Engine.one takes it up",
	"results: pending → skipped": "Engine.one when the answerer's key is missing; Engine.execute for what the run didn't reach",
	"results: running → done":    "Engine.one: graded",
	"results: running → error":   "Engine.one: the answer or the grade failed",
	"results: running → pending": "Engine.Recover after a crash; Engine.execute on a resume",
	"results: running → skipped": "Engine.execute: the run ended with it unfinished after a storage failure",
	"results: error → pending":   "Engine.execute: the one more try, a resume or a retry",
	"results: skipped → pending": "Engine.execute on a resume or retry",
}

func TestStatesEveryMoveHasACodePath(t *testing.T) {
	var edges []string
	for _, e := range RunTransitions.Edges(Statuses) {
		edges = append(edges, "runs: "+e)
	}
	for _, e := range ResultTransitions.Edges(ResultStatuses) {
		edges = append(edges, "results: "+e)
	}
	for _, e := range edges {
		if statesReachedBy[e] == "" {
			t.Errorf("%s is in the table; which code makes it?", e)
		}
	}
	for e := range statesReachedBy {
		found := false
		for _, x := range edges {
			found = found || x == e
		}
		if !found {
			t.Errorf("%s is documented but not in the table", e)
		}
	}
	// The diagram and Edges draw every status: one left out of the order would vanish from both.
	for s := range RunTransitions {
		if s != state.Start && !statesListed(Statuses, s) {
			t.Errorf("run status %q is not in Statuses", s)
		}
	}
	for s := range ResultTransitions {
		if s != state.Start && !statesListed(ResultStatuses, s) {
			t.Errorf("result status %q is not in ResultStatuses", s)
		}
	}
}

func statesListed[S ~string](in []S, s S) bool {
	for _, x := range in {
		if x == s {
			return true
		}
	}
	return false
}

// Every run status is over or in flight, never both or neither: the two predicates are how the
// pages and MCP tell a run that needs watching from one that's finished.
func TestStatesTerminalAndInFlightPartition(t *testing.T) {
	for _, s := range Statuses {
		if s.Terminal() == s.InFlight() {
			t.Errorf("%s: Terminal=%v InFlight=%v", s, s.Terminal(), s.InFlight())
		}
	}
	if !Done.CanBecome(Done) || Done.CanBecome(Cancelled) || !Queued.CanBecome(Running) {
		t.Error("CanBecome disagrees with the table")
	}
	if (Result{Status: ResultDone}).Graded() || (Result{Status: ResultError, Grade: &grade.Grade{}}).Graded() || !(Result{Status: ResultDone, Grade: &grade.Grade{}}).Graded() {
		t.Error("Graded needs both the status and the grade")
	}
}

// SQL that filters by status keeps its literal (`status='done'` reads better than a parameter);
// this checks every such literal in the code is a status the tables know, so a typo can't
// quietly match nothing. Writes go through the move helpers and carry the constants.
func TestStatesSQLLiteralsAreKnown(t *testing.T) {
	known := map[string]bool{}
	for _, s := range Statuses {
		known[string(s)] = true
	}
	for _, s := range ResultStatuses {
		known[string(s)] = true
	}
	for _, s := range budget.States {
		known[string(s)] = true
	}
	for _, s := range evaluation.CheckStates {
		known[string(s)] = true
	}
	for _, s := range evaluation.ReviewStates {
		known[string(s)] = true
	}
	for _, s := range []string{"proposed", "accepted", "withdrawn", "dismissed"} { // app.AttemptStates: app imports this package
		known[s] = true
	}
	column := regexp.MustCompile(`(?i)\b(?:status|state)\s*(?:=|!=|<>|IN\s*\()\s*('[a-z_]+'(?:\s*,\s*'[a-z_]+')*)`)
	word := regexp.MustCompile(`'([a-z_]+)'`)
	found := 0
	for _, dir := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range column.FindAllStringSubmatch(string(src), -1) {
				for _, w := range word.FindAllStringSubmatch(m[1], -1) {
					found++
					if !known[w[1]] {
						t.Errorf("%s: %q in SQL is not a status the tables know", path, w[1])
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if found == 0 {
		t.Fatal("no status literals found in SQL; the pattern is wrong")
	}
}

// statesGate puts the fake Messages API behind a gate: while blocking is set, each answer request
// (never a grading one) reports itself on asked and then waits for release, or for its caller to
// go away. That holds a result at running for as long as a test needs.
type statesGate struct {
	asked    chan struct{}
	release  chan struct{}
	blocking atomic.Bool
}

func statesGated(t *testing.T) *statesGate {
	t.Helper()
	g := &statesGate{asked: make(chan struct{}, 64), release: make(chan struct{})}
	g.blocking.Store(true)
	inner := llm.AnthropicURL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "output_config") && g.blocking.Load() {
			g.asked <- struct{}{}
			select {
			case <-g.release:
			case <-r.Context().Done():
				return
			}
		}
		res, err := http.Post(inner+r.URL.Path, "application/json", strings.NewReader(string(body)))
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer res.Body.Close()
		w.Header().Set("content-type", "application/json")
		io.Copy(w, res.Body)
	}))
	t.Cleanup(srv.Close)
	llm.AnthropicURL = srv.URL
	return g
}

// statesWait receives from ch, or fails the test after ten seconds.
func statesWait(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("waited 10s for %s", what)
	}
}

// statesCounts is how many of the run's results are in each status.
func statesCounts(t *testing.T, e *Engine, id string) map[ResultStatus]int {
	t.Helper()
	res, err := e.Results(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[ResultStatus]int{}
	for _, x := range res {
		out[x.Status]++
	}
	return out
}

// The process dies mid-run: its run is left running and the answer it was on left running too.
// The next start's Recover re-queues the run and makes the answer pending again, and a resume
// finishes it, keeping the answers it already had.
func TestStatesCrashLeavesRunningAndRecoverResumes(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	e.Cfg.Budget.Concurrency = 1
	gate := statesGated(t)
	ctx := context.Background()
	r, err := e.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	var dbFile string
	if err := e.DB.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbFile); err != nil {
		t.Fatal(err)
	}
	e.Start(r.ID)
	statesWait(t, gate.asked, "the first answer to be asked for")
	// Death: the process's database handle goes with it, so nothing it does from here on lands.
	// Its goroutine is let go at the end, when the gate opens, and finds the database closed.
	e.DB.Close()
	t.Cleanup(func() { close(gate.release); e.running.Wait() })

	db, err := store.Open(filepath.Dir(dbFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	next := New(e.Cfg, db)
	if s := next.Status(ctx, r.ID); s != Running {
		t.Fatalf("after the crash the run is %s, want running", s)
	}
	if c := statesCounts(t, next, r.ID); c[ResultRunning] != 1 || c[ResultPending] != 5 {
		t.Fatalf("after the crash the results are %v, want one running and five pending", c)
	}

	ids, err := next.Recover(ctx)
	if err != nil || len(ids) != 1 || ids[0] != r.ID {
		t.Fatalf("recover: %v %v", ids, err)
	}
	got, _ := next.Get(ctx, r.ID)
	if got.Status != Queued || got.Error == nil || *got.Error != Interrupted {
		t.Fatalf("after recover the run is %s (%v), want queued and interrupted", got.Status, got.Error)
	}
	if c := statesCounts(t, next, r.ID); c[ResultPending] != 6 {
		t.Fatalf("after recover the results are %v, want all pending", c)
	}
	var standing int
	db.QueryRow(`SELECT COUNT(*) FROM spend_events WHERE state=?`, budget.Reserved).Scan(&standing)
	if standing != 1 {
		t.Fatalf("%d reservations left standing by the dead process, want 1", standing)
	}

	gate.blocking.Store(false)
	if err := next.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = next.Get(ctx, r.ID)
	if got.Status != Done || got.Error != nil {
		t.Fatalf("after the resume the run is %s (%v), want done", got.Status, got.Error)
	}
	if c := statesCounts(t, next, r.ID); c[ResultDone] != 6 {
		t.Fatalf("after the resume the results are %v, want all done", c)
	}
}

// Cancelling a queued run ends it where it stands, with nothing asked; a retry runs it after all.
func TestStatesCancelWhileQueued(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	ctx := context.Background()
	r, err := e.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Cancel(r.ID) {
		t.Fatal("a queued run couldn't be cancelled")
	}
	got, _ := e.Get(ctx, r.ID)
	if got.Status != Cancelled || got.Error == nil || *got.Error != string(Cancelled) || got.FinishedAt == nil {
		t.Fatalf("status %s, error %v, finished %v", got.Status, got.Error, got.FinishedAt)
	}
	if c := statesCounts(t, e, r.ID); c[ResultPending] != 6 {
		t.Fatalf("results %v, want all still pending", c)
	}
	if e.Cancel(r.ID) {
		t.Fatal("a cancelled run was cancelled again")
	}
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if s := e.Status(ctx, r.ID); s != Done {
		t.Fatalf("after a retry the run is %s, want done", s)
	}
}

// Cancelling a running run cuts off the answer in flight (an error), skips what wasn't reached
// and records the run cancelled, not interrupted; a retry finishes it.
func TestStatesCancelWhileRunning(t *testing.T) {
	e, set, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	e.Cfg.Budget.Concurrency = 1
	gate := statesGated(t)
	ctx := context.Background()
	r, err := e.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	e.Start(r.ID)
	statesWait(t, gate.asked, "the first answer to be asked for")
	if !e.Cancel(r.ID) {
		t.Fatal("a running run couldn't be cancelled")
	}
	e.running.Wait()
	got, _ := e.Get(ctx, r.ID)
	if got.Status != Cancelled || got.Error == nil || *got.Error != string(Cancelled) {
		t.Fatalf("status %s, error %v, want cancelled", got.Status, got.Error)
	}
	if c := statesCounts(t, e, r.ID); c[ResultError] != 1 || c[ResultSkipped] != 5 {
		t.Fatalf("results %v, want the one in flight errored and five skipped", c)
	}
	gate.blocking.Store(false)
	if err := e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = e.Get(ctx, r.ID)
	if got.Status != Done || statesCounts(t, e, r.ID)[ResultDone] != 6 {
		t.Fatalf("after a retry the run is %s, want done with every result graded", got.Status)
	}
}
