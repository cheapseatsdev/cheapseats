// Package run executes visibility runs: every prompt in a set, on every answerer, on both tracks,
// N samples each, graded as they land. Spend is checked against the daily budget before every
// answer, so a run stops cleanly when the day's money is gone.
package run

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/opinion"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/state"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/version"
)

// Snapshot is everything that shaped a run, frozen when it was created.
type Snapshot struct {
	AnswerPrompt string `json:"answer_prompt"`
	SearchPolicy string `json:"search_policy"`
	GraderPrompt string `json:"grader_prompt"`
	// How each CLI answerer's provider was called (answer.CLIArgs), by provider; nil without one
	// and for runs from before it was recorded.
	CLIArgs map[string]string `json:"cli_args,omitempty"`

	Set       sets.Set          `json:"set"`
	Brand     config.Brand      `json:"brand"`
	Answerers []config.Answerer `json:"answerers"` // only the answerers whose API key is set
	Grader    string            `json:"grader"`
	Skipped   []string          `json:"skipped,omitempty"` // configured answerers left out for a missing key
	// The extra answers adaptive sampling gave this run, decided before it started; nil when it's
	// off. Not part of the design or the method (see Design).
	Sampling *Sampling `json:"sampling,omitempty"`
}

type Run struct {
	Profile string `json:"profile"`
	Design  string `json:"design"`
	// How answers were asked and graded; runs only trend together within one method.
	Method string `json:"method"`
	// The config repo commit and Cheap Seats version that produced it; empty for older runs.
	ConfigRev  string   `json:"config_rev"`
	AppVersion string   `json:"app_version"`
	ID         string   `json:"id"`
	SetID      string   `json:"set_id"`
	SetVersion string   `json:"set_version"`
	Name       string   `json:"name"`
	Trigger    string   `json:"trigger"`
	Status     Status   `json:"status"`
	Snapshot   Snapshot `json:"snapshot"`
	CreatedAt  string   `json:"created_at"`
	StartedAt  *string  `json:"started_at"`
	FinishedAt *string  `json:"finished_at"`
	Error      *string  `json:"error"`
	// Each time its answers were graded again (see Regrade), newest last; the method is the latest.
	Regrades []Regrade `json:"regrades,omitempty"`
}

type Result struct {
	ID        int64  `json:"id"`
	PromptIdx int    `json:"prompt_idx"`
	Prompt    string `json:"prompt"`
	// The prompt's stable id, and its wording's hash: a new hash under the same id is a reword.
	PromptID   string         `json:"prompt_id"`
	PromptSHA  string         `json:"prompt_sha"`
	Answerer   string         `json:"answerer"`
	Model      string         `json:"model"`
	Track      answer.Track   `json:"track"`
	Sample     int            `json:"sample"`
	Status     ResultStatus   `json:"status"`
	Answer     *answer.Answer `json:"answer,omitempty"`
	Grade      *grade.Grade   `json:"grade,omitempty"`
	Error      *string        `json:"error,omitempty"`
	CostUSD    float64        `json:"cost_usd"`
	FinishedAt *string        `json:"finished_at,omitempty"`
	// The prompt's intent was info when the run was made (see sets.Prompt); set by Intents.
	Info bool `json:"info,omitempty"`
}

type Engine struct {
	Cfg    *config.Config
	DB     *store.DB
	Budget *budget.Controller

	mu     sync.Mutex
	active map[string]context.CancelFunc
	// Runs executing now, and whether the process is shutting down: a run cut off by shutdown
	// records itself as interrupted, to resume on the next start, rather than cancelled.
	running  sync.WaitGroup
	stopping atomic.Bool

	// OnFinished is called after a run completes (the analyst hooks in here for the fix list).
	OnFinished func(ctx context.Context, id string)
}

func New(cfg *config.Config, db *store.DB) *Engine {
	return &Engine{Cfg: cfg, DB: db, Budget: budget.New(cfg, db), active: map[string]context.CancelFunc{}}
}

func NewID(prefix string) string {
	b := make([]byte, 3)
	rand.Read(b)
	return prefix + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Samples for one answerer on one track, after the set's tier is applied.
func (e *Engine) samples(set sets.Set, a config.Answerer, t answer.Track) int {
	n := a.Samples.Search
	if t == answer.Recall {
		n = a.Samples.Recall
	}
	if set.Tier == sets.PLG && n > e.Cfg.Tiers.PLG.MaxSamples {
		n = e.Cfg.Tiers.PLG.MaxSamples
	}
	return n
}

// BusyError is returned by Create when the set already has a run in progress: one run per set
// at a time, so a double click or an overlapping schedule doesn't pay for the same answers twice.
type BusyError struct{ RunID string }

func (e *BusyError) Error() string { return "this set already has a run in progress: " + e.RunID }

// Create records a queued run over the set with the answerers whose keys are present.
func (e *Engine) Create(ctx context.Context, set sets.Set, trigger string) (*Run, error) {
	if e.stopping.Load() {
		return nil, errors.New("cheapseats is shutting down; start the run after it restarts")
	}
	snap := NewSnapshot(e.Cfg, set)
	if e.Cfg.Sampling.Adaptive && set.Tier != sets.PLG {
		sp, err := e.plan(ctx, set, snap)
		if err != nil {
			return nil, stored(err)
		}
		snap.Sampling = sp
	}
	if len(snap.Answerers) == 0 {
		return nil, errors.New("no answerer is available: set ANTHROPIC_API_KEY or OPENAI_API_KEY, or install the claude or codex CLI")
	}
	if m := config.Missing(e.Cfg.Grader.Via()); m != "" {
		return nil, fmt.Errorf("grading needs the grader's provider (%s): %s", e.Cfg.Grader.Via(), m)
	}
	r := &Run{Design: Design(snap), Method: Method(snap), ConfigRev: e.Cfg.Rev, AppVersion: version.String(), ID: NewID("r-"), SetID: set.ID, SetVersion: set.Version, Trigger: trigger, Status: Queued,
		Name: fmt.Sprintf("%s %s", set.Name, time.Now().UTC().Format("2006-01-02 15:04")), Snapshot: snap, CreatedAt: store.Now()}
	RunTransitions.Check("runs", state.Start, r.Status)
	ResultTransitions.Check("results", state.Start, ResultPending)
	js, _ := json.Marshal(snap)
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The check and the insert are one statement, so two requests at once can't both get in.
	res, err := tx.ExecContext(ctx, `INSERT INTO runs (id, set_id, set_version, name, trigger, status, snapshot, created_at, design, method, config_rev, app_version)
		SELECT ?,?,?,?,?,?,?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM runs WHERE set_id = ? AND status IN ('queued','running'))`,
		r.ID, r.SetID, r.SetVersion, r.Name, r.Trigger, r.Status, string(js), r.CreatedAt, r.Design, r.Method, r.ConfigRev, r.AppVersion, r.SetID)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		busy := &BusyError{}
		tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE set_id = ? AND status IN ('queued','running') ORDER BY created_at LIMIT 1`, r.SetID).Scan(&busy.RunID)
		return nil, busy
	}
	for i, p := range set.Prompts {
		for _, a := range snap.Answerers {
			for _, t := range []answer.Track{answer.Search, answer.Recall} {
				for s := 0; s < e.samples(set, a, t)+snap.Sampling.Extra(p.ID, a.ID, t); s++ {
					if _, err := tx.ExecContext(ctx, `INSERT INTO results (run_id, prompt_idx, prompt, prompt_id, prompt_sha, answerer, model, track, sample, status) VALUES (?,?,?,?,?,?,?,?,?,?)`,
						r.ID, i, p.Text, p.ID, p.SHA(), a.ID, a.Model, string(t), s, ResultPending); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return r, tx.Commit()
}

// Start runs it in the background. Returns false if it's already running.
func (e *Engine) Start(id string) bool {
	e.mu.Lock()
	if _, ok := e.active[id]; ok {
		e.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.active[id] = cancel
	e.running.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.running.Done()
		defer func() {
			e.mu.Lock()
			delete(e.active, id)
			e.mu.Unlock()
			cancel()
		}()
		if err := e.execute(ctx, id); err != nil {
			slog.Error("run failed", "run", id, "err", err)
		}
	}()
	return true
}

// Cancel stops a run: an active one where it is, or one queued to resume before it starts.
func (e *Engine) Cancel(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.active[id]; ok {
		c()
		return true
	}
	moved, err := e.moveRunFrom(context.Background(), id, Queued, Cancelled, `, error=?, finished_at=?`, string(Cancelled), store.Now())
	return err == nil && moved
}

// Stop is for shutdown: no new runs start, and every active run is cut off and records itself as
// interrupted, to resume on the next start. Waits up to wait for them to finish recording.
func (e *Engine) Stop(wait time.Duration) {
	e.stopping.Store(true)
	e.mu.Lock()
	for _, c := range e.active {
		c()
	}
	e.mu.Unlock()
	done := make(chan struct{})
	go func() { e.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
		slog.Warn("shutting down with runs still recording; they resume on the next start")
	}
}

func (e *Engine) IsActive(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.active[id]
	return ok
}

var errBudget = budget.ErrExhausted

// Execute runs every pending result. Finished results are kept, so this also resumes a run.
func (e *Engine) Execute(ctx context.Context, id string) error {
	e.mu.Lock()
	if _, ok := e.active[id]; ok {
		e.mu.Unlock()
		return fmt.Errorf("run is already active")
	}
	ctx, cancel := context.WithCancel(ctx)
	e.active[id] = cancel
	e.running.Add(1)
	e.mu.Unlock()
	defer e.running.Done()
	defer func() { cancel(); e.mu.Lock(); delete(e.active, id); e.mu.Unlock() }()
	return e.execute(ctx, id)
}
func (e *Engine) execute(ctx context.Context, id string) error {
	ctx = llm.WithOperation(llm.WithAccounting(ctx, e.Budget), "visibility/"+id)
	r, err := e.Get(ctx, id)
	if err != nil {
		return err
	}
	if !r.Snapshot.Current(e.Cfg) {
		return fmt.Errorf("run used older answer or grading prompts; start a new run")
	}
	if err := e.moveRun(ctx, id, Running, `, started_at=COALESCE(started_at, ?), error=NULL, finished_at=NULL`, store.Now()); err != nil {
		return stored(err)
	}
	// A result left running by a crash is just pending again.
	if _, err := e.moveResultsFrom(ctx, id, []ResultStatus{ResultRunning, ResultSkipped, ResultError}, ResultPending); err != nil {
		return stored(err)
	}

	answerers := map[string]config.Answerer{}
	for _, a := range r.Snapshot.Answerers {
		answerers[a.ID] = a
	}
	type job struct {
		id       int64
		prompt   string
		answerer string
		track    answer.Track
		extra    bool // an adaptive extra sample: asked after every planned one
	}
	var stopOnce sync.Once
	var stopReason error
	stopCtx, stop := context.WithCancel(ctx)
	defer stop()
	// pass answers every pending result.
	pass := func() error {
		rows, err := e.DB.QueryContext(ctx, `SELECT id, prompt, prompt_id, answerer, track, sample FROM results WHERE run_id=? AND status='pending' ORDER BY prompt_idx, answerer, track, sample`, id)
		if err != nil {
			return err
		}
		var jobs []job
		for rows.Next() {
			var j job
			var t, pid string
			var sample int
			if err := rows.Scan(&j.id, &j.prompt, &pid, &j.answerer, &t, &sample); err != nil {
				rows.Close()
				return err
			}
			j.track = answer.Track(t)
			j.extra = r.Snapshot.Sampling.IsExtra(pid, j.answerer, j.track, sample)
			jobs = append(jobs, j)
		}
		rows.Close()
		// Extras last, so a budget that runs out costs them rather than a planned answer.
		sort.SliceStable(jobs, func(a, b int) bool { return !jobs[a].extra && jobs[b].extra })
		ch := make(chan job)
		var wg sync.WaitGroup
		for w := 0; w < min(e.Cfg.Budget.Concurrency, len(jobs)); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range ch {
					cfg := *e.Cfg
					cfg.Brand = r.Snapshot.Brand
					cfg.Grader = config.ModelFromRef(r.Snapshot.Grader)
					callErr := e.one(stopCtx, &cfg, r.Snapshot.Set, answerers[j.answerer], j.id, j.prompt, j.track)
					var storage *StorageError
					if errors.Is(callErr, errBudget) || errors.As(callErr, &storage) {
						stopOnce.Do(func() { stopReason = callErr; stop() })
					}
				}
			}()
		}
	feed:
		for _, j := range jobs {
			select {
			case <-stopCtx.Done():
				break feed
			case ch <- j:
			}
		}
		close(ch)
		wg.Wait()
		return nil
	}
	if err := pass(); err != nil {
		return err
	}
	// One more try for answers that failed, unless the run was stopped: a provider's transient
	// error would otherwise leave a scheduled run partial, and without a fix list, until someone
	// retried it by hand. Answers that were saved but not graded are reused, so this costs little.
	if stopCtx.Err() == nil {
		n, err := e.moveResultsFrom(ctx, id, []ResultStatus{ResultError}, ResultPending)
		if err != nil {
			return err
		}
		if n > 0 {
			slog.Info("retrying failed answers", "run", id, "count", n)
			if err := pass(); err != nil {
				return err
			}
		}
	}

	// Anything not reached is skipped, so the page says why and Retry picks it up.
	bg := context.Background()
	if _, err := e.moveResultsFrom(bg, id, []ResultStatus{ResultPending, ResultRunning}, ResultSkipped); err != nil && stopReason == nil {
		stopReason = stored(err)
	}
	// Extras are a bonus: once every planned answer is in, any extra not done (failed, or cut off by
	// the budget) is dropped, so the run is done and counts in rankings, trends and the fix list.
	if r.Snapshot.Sampling != nil && ctx.Err() == nil && (stopReason == nil || errors.Is(stopReason, errBudget)) {
		complete, err := e.dropExtras(bg, id, r.Snapshot.Sampling)
		switch {
		case err != nil:
			stopReason = stored(err)
		case complete:
			stopReason = nil
		}
	}
	// If this count fails the run can't say it's done, so it's failed instead.
	var unfinished int
	if err := e.DB.QueryRow(`SELECT COUNT(*) FROM results WHERE run_id=? AND (status!='done' OR grade IS NULL)`, id).Scan(&unfinished); err != nil && stopReason == nil {
		stopReason = stored(err)
	}
	status, msg := Done, ""
	if unfinished > 0 {
		status = Partial
		msg = fmt.Sprintf("%d unfinished results; retry to complete", unfinished)
	}
	switch {
	case errors.Is(stopReason, errBudget):
		status, msg = OverBudget, fmt.Sprintf("stopped: today's spend reached the $%.0f daily budget; retry tomorrow to finish", e.Cfg.Budget.DailyUSD)
	case stopReason != nil:
		status, msg = Failed, stopReason.Error()
	case ctx.Err() != nil && e.stopping.Load():
		status, msg = Queued, Interrupted
	case ctx.Err() != nil:
		status, msg = Cancelled, string(Cancelled)
	}
	var errArg any
	if msg != "" {
		errArg = msg
	}
	err = e.moveRun(bg, id, status, `, error=?, finished_at=?`, errArg, store.Now())
	if status == Done {
		var models []string
		rows, qerr := e.DB.Query(`SELECT DISTINCT answerer||':'||model FROM results WHERE run_id=? ORDER BY answerer,model`, id)
		if qerr != nil {
			return qerr
		}
		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				rows.Close()
				return stored(err)
			}
			models = append(models, m)
		}
		rows.Close()
		profile := Digest(struct {
			Design string
			Models []string
		}{r.Design, models})
		if _, err := e.DB.Exec(`UPDATE runs SET profile=? WHERE id=?`, profile, id); err != nil {
			return stored(err)
		}
		if e.Cfg.ThemesOn() && ctx.Err() == nil {
			if _, err := e.BuildThemes(ctx, id); err != nil {
				slog.Error("themes", "run", id, "err", err)
			}
		}
		if e.OnFinished != nil && ctx.Err() == nil {
			e.OnFinished(ctx, id)
		}
	}
	return err
}

// GradingErrorPrefix starts a result's stored error when its answer arrived but grading failed.
const GradingErrorPrefix = "grading: "

// StorageError is a failed read or write of the run's own records, as opposed to a provider
// failing to answer or grade. It stops the run: carrying on could lose a paid answer, or mark
// the run done when it isn't.
type StorageError struct{ Err error }

func (e *StorageError) Error() string { return "storage: " + e.Err.Error() }
func (e *StorageError) Unwrap() error { return e.Err }

// stored wraps a storage failure, or passes nil through.
func stored(err error) error {
	if err == nil {
		return nil
	}
	return &StorageError{err}
}

func (e *Engine) one(ctx context.Context, cfg *config.Config, set sets.Set, a config.Answerer, rid int64, prompt string, track answer.Track) error {
	bg := context.Background()
	if m := config.Missing(a.Provider); m != "" {
		return stored(e.moveResult(bg, rid, ResultSkipped, `,error=?`, m))
	}
	if err := e.moveResult(bg, rid, ResultRunning, `,started_at=COALESCE(started_at,?),finished_at=NULL,error=NULL`, store.Now()); err != nil {
		return stored(err)
	}
	// An answer saved but not yet graded is reused, so a failed read must not look like "none".
	var saved sql.NullString
	if err := e.DB.QueryRow(`SELECT answer FROM results WHERE id=?`, rid).Scan(&saved); err != nil {
		return stored(err)
	}
	var ans answer.Answer
	reuse := saved.Valid && json.Unmarshal([]byte(saved.String), &ans) == nil && ans.Text != "" && (track != answer.Search || ans.Searched())
	if !reuse {
		var err error
		ans, err = answer.Ask(ctx, a, track, answer.System(cfg.Brand.Audience), prompt)
		cost := cfg.Cost(a.Model, ans.Usage)
		if config.Subscription(a.Provider) {
			cost = 0 // paid by the subscription, not per token
		}
		js, _ := json.Marshal(ans)
		if err != nil {
			if werr := e.moveResult(bg, rid, ResultError, `,error=?,answer=NULL,answer_cost_usd=answer_cost_usd+?,finished_at=?`, err.Error(), cost, store.Now()); werr != nil {
				return stored(werr)
			}
			return err
		}
		model := a.Model
		if ans.Model != "" {
			model = ans.Model
		}
		if _, err = e.DB.ExecContext(bg, `UPDATE results SET answer=?,model=?,answer_cost_usd=answer_cost_usd+? WHERE id=?`, string(js), model, cost, rid); err != nil {
			return stored(err)
		}
	}
	g, cost, err := grade.Answer(ctx, cfg, set, prompt, ans.Text)
	if err != nil {
		if werr := e.moveResult(bg, rid, ResultError, `,error=?,grade_cost_usd=grade_cost_usd+?,finished_at=?`, GradingErrorPrefix+err.Error(), cost, store.Now()); werr != nil {
			return stored(werr)
		}
		return err
	}
	js, _ := json.Marshal(g)
	return stored(e.moveResult(bg, rid, ResultDone, `,error=NULL,grade=?,verdict=?,top_pick=?,grade_cost_usd=grade_cost_usd+?,finished_at=?`, string(js), string(g.Verdict), g.IsTopPick(cfg.Brand.Name), cost, store.Now()))
}

// NewSnapshot freezes a run's design with the answerers this environment can call, so a
// Claude-only run completes and compares only with other Claude-only runs.
func NewSnapshot(c *config.Config, s sets.Set) Snapshot {
	answerers := []config.Answerer{}
	var skipped []string
	for _, a := range c.Answerers {
		if config.Available(a.Provider) {
			answerers = append(answerers, a)
		} else {
			skipped = append(skipped, a.ID)
		}
	}
	return Snapshot{Set: s, Brand: c.Brand, Answerers: answerers, Skipped: skipped, Grader: c.Grader.Ref(), AnswerPrompt: answer.System(c.Brand.Audience), SearchPolicy: answer.SearchPolicy, GraderPrompt: grade.System,
		CLIArgs: cliArgs(answerers)}
}

// Current reports whether the run was answered and graded under the prompts c would use today,
// so a retry can't mix two rubrics in one run.
func (s Snapshot) Current(c *config.Config) bool {
	return s.AskedAsToday(c) && s.GraderPrompt == grade.System
}

// AskedAsToday reports whether the run asked its answers as c would today: the part of a method a
// re-grade can't change.
func (s Snapshot) AskedAsToday(c *config.Config) bool {
	return s.AskedAlike(Snapshot{AnswerPrompt: answer.System(c.Brand.Audience), SearchPolicy: answer.SearchPolicy, CLIArgs: cliArgs(s.Answerers)})
}

// AskedAlike reports whether two runs asked their answers the same way: the answer prompt, search
// policy and CLI arguments.
func (s Snapshot) AskedAlike(o Snapshot) bool {
	return s.AnswerPrompt == o.AnswerPrompt && s.SearchPolicy == o.SearchPolicy && maps.Equal(s.CLIArgs, o.CLIArgs)
}

// cliArgs is answer.CLIArgs for each CLI provider among answerers, or nil when there is none.
func cliArgs(answerers []config.Answerer) map[string]string {
	var m map[string]string
	for _, a := range answerers {
		if args := answer.CLIArgs(a.Provider); args != "" {
			if m == nil {
				m = map[string]string{}
			}
			m[a.Provider] = args
		}
	}
	return m
}
func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Design is what makes two runs comparable: the set, answerers, prompts and grader. Facts only
// shape the fix list, so changing them doesn't restart the rankings; the audience is already in
// AnswerPrompt.
//
// Adaptive sampling's extras aren't part of it either: they're decided from the runs before, so
// they differ run to run, and more answers to a question measure the same thing more closely.
func Design(s Snapshot) string {
	s.Skipped, s.Brand.Facts, s.Brand.Audience, s.Sampling = nil, nil, "", nil
	return Digest(s)
}

// Method is how a run's answers were asked and graded: the answer, search and grader prompts,
// the arguments CLI answerers were called with, the grader model, and the brand as the grader
// knows it (name, aliases, about). Two runs with
// the same method measure the same thing, so their rates trend together even when prompts,
// competitors or answerers changed between them (those changes are annotated); a different
// method starts a new trend. The brand's sites aren't part of it: they shape which pages count
// as its own, not how answers are graded. Stored methods are recomputed from snapshots at every
// start (app.Backfill), so changing this definition never splits existing history: a run with no
// CLI arguments recorded keeps the method it had before they were.
func Method(s Snapshot) string {
	b := s.Brand
	return Digest(struct {
		AnswerPrompt, SearchPolicy, GraderPrompt, Grader string
		Name, About                                      string
		Aliases                                          []string
		CLIArgs                                          map[string]string `json:",omitempty"`
	}{s.AnswerPrompt, s.SearchPolicy, s.GraderPrompt, s.Grader, b.Name, b.About, b.Aliases, s.CLIArgs})
}

func (e *Engine) Get(ctx context.Context, id string) (*Run, error) {
	var r Run
	var snap string
	var regrades sql.NullString
	err := e.DB.QueryRowContext(ctx, `SELECT id, set_id, set_version, name, trigger, status, snapshot, created_at, started_at, finished_at, error, design, profile, method, config_rev, app_version, regrades FROM runs WHERE id=?`, id).
		Scan(&r.ID, &r.SetID, &r.SetVersion, &r.Name, &r.Trigger, &r.Status, &snap, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.Error, &r.Design, &r.Profile, &r.Method, &r.ConfigRev, &r.AppVersion, &regrades)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(snap), &r.Snapshot); err != nil {
		return nil, err
	}
	if regrades.Valid {
		if err := json.Unmarshal([]byte(regrades.String), &r.Regrades); err != nil {
			return nil, stored(fmt.Errorf("run %s: regrades: %w", id, err))
		}
	}
	return &r, nil
}

// List returns recent runs, newest first, optionally for one set.
func (e *Engine) List(ctx context.Context, setID string, limit int) ([]Run, error) {
	q := `SELECT id FROM runs`
	args := []any{}
	if setID != "" {
		q += ` WHERE set_id=?`
		args = append(args, setID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := e.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	var out []Run
	for _, id := range ids {
		r, err := e.Get(ctx, id)
		if err == nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (e *Engine) Results(ctx context.Context, id string) ([]Result, error) {
	rows, err := e.DB.QueryContext(ctx, `SELECT id, prompt_idx, prompt, prompt_id, prompt_sha, answerer, model, track, sample, status, answer, grade, error, answer_cost_usd+grade_cost_usd, finished_at FROM results WHERE run_id=? ORDER BY prompt_idx, answerer, track, sample`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var r Result
		var t string
		var ans, g sql.NullString
		if err := rows.Scan(&r.ID, &r.PromptIdx, &r.Prompt, &r.PromptID, &r.PromptSHA, &r.Answerer, &r.Model, &t, &r.Sample, &r.Status, &ans, &g, &r.Error, &r.CostUSD, &r.FinishedAt); err != nil {
			return nil, err
		}
		r.Track = answer.Track(t)
		// Unknown fields are fine (newer versions add them); JSON that doesn't decode is an error,
		// not an answer that quietly disappears from the rates.
		if ans.Valid {
			r.Answer = &answer.Answer{}
			if err := json.Unmarshal([]byte(ans.String), r.Answer); err != nil {
				return nil, stored(fmt.Errorf("result %d: answer: %w", r.ID, err))
			}
		}
		if g.Valid {
			r.Grade = &grade.Grade{}
			if err := json.Unmarshal([]byte(g.String), r.Grade); err != nil {
				return nil, stored(fmt.Errorf("result %d: grade: %w", r.ID, err))
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Intents marks the results whose prompt was an info prompt in the run's snapshot, so a prompt is
// scored the way it was asked, whatever its intent is now.
func (r *Run) Intents(results []Result) {
	info := map[string]bool{}
	for _, p := range r.Snapshot.Set.Prompts {
		if p.Info() {
			info[p.ID] = true
		}
	}
	for i := range results {
		results[i].Info = info[results[i].PromptID]
	}
}

// Interrupted is the error a run carries while it waits to resume after a restart.
const Interrupted = "interrupted by a restart; resuming"

// Recover readies the runs a restart interrupted, and returns them oldest first to resume: any
// still marked running (cut off) or queued (recorded on the way down, or never started). Their
// answers so far are kept; only what's unfinished runs again.
func (e *Engine) Recover(ctx context.Context) ([]string, error) {
	if _, err := e.moveResultsFrom(ctx, "", []ResultStatus{ResultRunning}, ResultPending); err != nil {
		return nil, err
	}
	if err := e.moveRunsFrom(ctx, []Status{Running, Queued}, Queued, `, error=?`, Interrupted); err != nil {
		return nil, err
	}
	rows, err := e.DB.QueryContext(ctx, `SELECT id FROM runs WHERE status='queued' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Fail ends a run that can't continue, saying why.
func (e *Engine) Fail(ctx context.Context, id, why string) {
	e.moveRun(ctx, id, Failed, `, error=?, finished_at=?`, why, store.Now())
}

// Status is the run's status, or "" if there's no such run.
func (e *Engine) Status(ctx context.Context, id string) Status {
	var s Status
	e.DB.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=?`, id).Scan(&s)
	return s
}

// BuildThemes runs the theme pass over a run's graded answers with the run's grader (see
// opinion.Group) and stores what came of it, an error included, with the run.
func (e *Engine) BuildThemes(ctx context.Context, id string) (opinion.Themes, error) {
	r, err := e.Get(ctx, id)
	if err != nil {
		return opinion.Themes{}, err
	}
	results, err := e.Results(ctx, id)
	if err != nil {
		return opinion.Themes{}, err
	}
	var in []opinion.Answer
	for _, x := range results {
		if x.Graded() {
			in = append(in, opinion.Answer{ID: x.ID, Grade: x.Grade})
		}
	}
	cfg := *e.Cfg
	cfg.Brand = r.Snapshot.Brand
	cfg.Grader = config.ModelFromRef(r.Snapshot.Grader)
	ctx = llm.WithOperation(llm.WithAccounting(ctx, e.Budget), "themes/"+id)
	t, gerr := opinion.Group(ctx, &cfg, r.Snapshot.Set, in)
	js, _ := json.Marshal(t)
	_, err = e.DB.ExecContext(context.Background(), `UPDATE runs SET themes=? WHERE id=?`, string(js), id)
	return t, errors.Join(gerr, stored(err))
}

// Themes is the run's stored theme pass, or nil if it has none.
func (e *Engine) Themes(ctx context.Context, id string) (*opinion.Themes, error) {
	var js sql.NullString
	if err := e.DB.QueryRowContext(ctx, `SELECT themes FROM runs WHERE id=?`, id).Scan(&js); err != nil || !js.Valid {
		return nil, err
	}
	var t opinion.Themes
	if err := json.Unmarshal([]byte(js.String), &t); err != nil {
		return nil, stored(fmt.Errorf("run %s: themes: %w", id, err))
	}
	return &t, nil
}

// Opinion is what the run's answers say about the brand: the sentiment of results, which are the
// run's (or a slice of them), and its stored theme pass.
func (e *Engine) Opinion(ctx context.Context, id string, results []Result) opinion.Report {
	return e.OpinionWith(ctx, id, results, opinion.Options{})
}

// OpinionWith is Opinion with the sentiment counted through opt (app.PhraseOptions).
func (e *Engine) OpinionWith(ctx context.Context, id string, results []Result, opt opinion.Options) opinion.Report {
	var grades []*grade.Grade
	for _, x := range results {
		if x.Status == ResultDone {
			grades = append(grades, x.Grade)
		}
	}
	rep := opinion.Report{Sentiment: opinion.SummarizeWith(grades, opt)}
	t, err := e.Themes(ctx, id)
	if err != nil {
		slog.Warn("reading themes", "run", id, "err", err)
	}
	rep.Themes = t
	return rep
}

// dropExtras deletes the run's unfinished extra samples if every planned one is done, and reports
// whether they all were.
func (e *Engine) dropExtras(ctx context.Context, id string, sp *Sampling) (bool, error) {
	rows, err := e.DB.QueryContext(ctx, `SELECT id, prompt_id, answerer, track, sample FROM results WHERE run_id=? AND (status!='done' OR grade IS NULL)`, id)
	if err != nil {
		return false, err
	}
	var drop []int64
	for rows.Next() {
		var rid int64
		var pid, ans, t string
		var sample int
		if err := rows.Scan(&rid, &pid, &ans, &t, &sample); err != nil {
			rows.Close()
			return false, err
		}
		if !sp.IsExtra(pid, ans, answer.Track(t), sample) {
			rows.Close()
			return false, nil // a planned answer is unfinished: retry finishes the extras too
		}
		drop = append(drop, rid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, rid := range drop {
		if _, err := e.DB.ExecContext(ctx, `DELETE FROM results WHERE id=?`, rid); err != nil {
			return false, err
		}
	}
	if len(drop) > 0 {
		slog.Info("dropped unfinished extra samples", "run", id, "count", len(drop))
	}
	return true, nil
}
