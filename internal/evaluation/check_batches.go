package evaluation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/state"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

type CheckBatchCase struct {
	CaseID   string  `json:"case_id"`
	CheckID  string  `json:"check_id"`
	Revision int     `json:"revision"`
	MaxUSD   float64 `json:"check_admission_budget_usd"`
}
type CheckBatchPlan struct {
	ID        string           `json:"id"`
	ImportID  string           `json:"import_id"`
	SessionID string           `json:"session_id,omitempty"`
	Model     string           `json:"model"`
	Transport string           `json:"transport"`
	Evaluator string           `json:"evaluator"`
	Rubric    string           `json:"rubric"`
	Cases     []CheckBatchCase `json:"cases"`
	MaxUSD    float64          `json:"admission_budget_usd"`
	PriorUSD  float64          `json:"prior_usd"`
}

func (p CheckBatchPlan) kinds() []string {
	out := make([]string, 0, len(p.Cases))
	for _, c := range p.Cases {
		out = append(out, "provider-attempt/evaluation/check/"+c.CheckID)
	}
	return out
}
func (p CheckBatchPlan) identity() string {
	p.ID = ""
	p.PriorUSD = 0
	b, _ := json.Marshal(p)
	return "eb-" + digest(b)
}

type CheckBatchOffer struct {
	Plan               CheckBatchPlan
	PlanJSON           string
	Total, Cached, New int
	Estimate           float64
	ReadyError         string
	Subscription       bool
}

func (w *Workbench) PrepareCheckBatch(ctx context.Context, a Assessor, importID, sessionID, actor string) (CheckBatchOffer, error) {
	out := CheckBatchOffer{Plan: CheckBatchPlan{ImportID: importID, SessionID: sessionID, Model: a.Cfg.Grader.Ref(), Transport: a.Transport(), Evaluator: grade.EvaluatorDigest(), Rubric: Rubric(), Cases: []CheckBatchCase{}}, Subscription: a.Cfg.Grader.Via() == "claude-code"}
	cs, err := w.ImportCases(ctx, importID)
	if err != nil {
		return out, err
	}
	selected := map[string]bool{}
	if sessionID != "" {
		s, e := w.Session(ctx, sessionID, actor)
		if e != nil {
			return out, e
		}
		if s.ImportID != importID {
			return out, ErrConflict
		}
		for _, id := range s.CaseIDs {
			selected[id] = true
		}
	}
	for _, c := range cs {
		if c.Review.State != "reviewed" || sessionID != "" && !selected[c.ID] {
			continue
		}
		plan := a.CheckPlan(c.ID, 5*a.Budget.Allowance())
		cached, e := w.Check(ctx, plan.ID)
		if e == nil {
			plan = cached.Plan
			if cached.State == CheckDone {
				out.Cached++
			}
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
		out.Plan.Cases = append(out.Plan.Cases, CheckBatchCase{c.ID, plan.ID, c.Review.Revision, plan.MaxUSD})
		if e != nil || cached.State != CheckDone {
			out.New++
			out.Estimate += a.Estimate(c.Record, plan.Prompt)
		}
	}
	out.Total = len(out.Plan.Cases)
	if out.Total == 0 || out.Total > 500 {
		return out, errors.New("choose between one and 500 finished judgments for a batch")
	}
	out.Plan.PriorUSD, err = a.Budget.SpentKinds(ctx, out.Plan.kinds())
	if err != nil {
		return out, err
	}
	out.Plan.MaxUSD = math.Min(1000, math.Ceil((out.Plan.PriorUSD+math.Max(5, float64(out.New)*2*a.Budget.Allowance()))*100)/100)
	out.Plan.ID = out.Plan.identity()
	b, _ := json.Marshal(out.Plan)
	out.PlanJSON = string(b)
	if err := a.Ready(); err != nil {
		out.ReadyError = err.Error()
	}
	return out, nil
}

type CheckBatch struct {
	Plan       CheckBatchPlan `json:"plan"`
	Actor      string         `json:"actor"`
	State      CheckState     `json:"state"`
	Error      string         `json:"error,omitempty"`
	LeaseUntil string         `json:"lease_until,omitempty"`
	CreatedAt  string         `json:"created_at"`
}

func (w *Workbench) CheckBatch(ctx context.Context, id, actor string) (CheckBatch, error) {
	var b CheckBatch
	var raw string
	err := w.DB.QueryRowContext(ctx, `SELECT plan,actor,state,error,lease_until,created_at FROM evaluation_check_batches WHERE id=? AND actor=?`, id, actor).Scan(&raw, &b.Actor, &b.State, &b.Error, &b.LeaseUntil, &b.CreatedAt)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &b.Plan)
	}
	return b, err
}
func (w *Workbench) CreateCheckBatch(ctx context.Context, a Assessor, p CheckBatchPlan, actor string) (CheckBatch, error) {
	if strings.TrimSpace(actor) == "" || len(actor) > 200 || p.MaxUSD <= 0 || p.MaxUSD > 1000 || math.IsNaN(p.MaxUSD) || math.IsInf(p.MaxUSD, 0) {
		return CheckBatch{}, errors.New("choose a positive batch admission budget of at most $1,000")
	}
	p.ID = p.identity()
	persistedID := "eb-" + digest([]byte(p.ID+"\x00"+actor))
	if existing, err := w.CheckBatch(ctx, persistedID, actor); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return CheckBatch{}, err
	}
	offer, err := w.PrepareCheckBatch(ctx, a, p.ImportID, p.SessionID, actor)
	if err != nil {
		return CheckBatch{}, err
	}
	canonical := offer.Plan
	canonical.MaxUSD = p.MaxUSD
	canonical.ID = canonical.identity()
	if canonical.ID != p.ID {
		return CheckBatch{}, ErrConflict
	}
	p = canonical
	raw, _ := json.Marshal(p)
	now := store.Now()
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return CheckBatch{}, err
	}
	defer tx.Rollback()
	for _, c := range p.Cases {
		var revision int
		var state string
		if e := tx.QueryRowContext(ctx, `SELECT revision,state FROM evaluation_reviews WHERE case_id=?`, c.CaseID).Scan(&revision, &state); e != nil {
			return CheckBatch{}, e
		}
		if revision != c.Revision || state != "reviewed" {
			return CheckBatch{}, ErrConflict
		}
	}
	CheckBatchTransitions.Check("evaluation_check_batches", state.Start, CheckReady)
	// Actor participates in the persisted identity, so two reviewers can independently
	// start the same selection while still sharing the label-blind check cache.
	p.ID = persistedID
	raw, _ = json.Marshal(p)
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO evaluation_check_batches(id,plan,actor,state,created_at,updated_at) VALUES (?,?,?,'ready',?,?)`, p.ID, string(raw), actor, now, now); err != nil {
		return CheckBatch{}, err
	}
	for _, c := range p.Cases {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO evaluation_check_batch_items(batch_id,case_id,check_id,state) VALUES (?,?,?,'ready')`, p.ID, c.CaseID, c.CheckID); err != nil {
			return CheckBatch{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return CheckBatch{}, err
	}
	return w.CheckBatch(ctx, p.ID, actor)
}
func (w *Workbench) CheckBatches(ctx context.Context, importID, actor string) ([]CheckBatch, error) {
	rows, err := w.DB.QueryContext(ctx, `SELECT id FROM evaluation_check_batches WHERE actor=? AND json_extract(plan,'$.import_id')=? ORDER BY created_at DESC,id DESC LIMIT 20`, actor, importID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []CheckBatch{}
	for _, id := range ids {
		b, e := w.CheckBatch(ctx, id, actor)
		if e != nil {
			return nil, e
		}
		if b.Plan.ImportID == importID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (w *Workbench) RunCheckBatch(ctx context.Context, id, actor string, a Assessor, retry bool, assess AssessFunc) error {
	b, err := w.CheckBatch(ctx, id, actor)
	if err != nil {
		return err
	}
	if b.Plan.Model != a.Cfg.Grader.Ref() || b.Plan.Transport != a.Transport() || b.Plan.Evaluator != grade.EvaluatorDigest() || b.Plan.Rubric != Rubric() {
		return errors.New("resume requires the original model, transport, evaluator and rules")
	}
	if assess == nil {
		grouped, e := a.Budget.WithinKinds(b.Plan.kinds(), b.Plan.MaxUSD)
		if e != nil {
			return e
		}
		a.Budget = grouped
		assess = a.Assess
	}
	CheckBatchTransitions.Check("evaluation_check_batches", b.State, CheckRunning)
	lease := rand.Text()
	now := store.Now()
	until := func() string { return time.Now().UTC().Add(4 * time.Minute).Format(time.RFC3339) }
	result, err := w.DB.ExecContext(ctx, `UPDATE evaluation_check_batches SET state='running',lease=?,lease_until=?,error='',updated_at=? WHERE id=? AND actor=? AND (state!='running' OR lease_until<?)`, lease, until(), now, id, actor, now)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-callCtx.Done():
				return
			case <-ticker.C:
				r, e := w.DB.ExecContext(callCtx, `UPDATE evaluation_check_batches SET lease_until=?,updated_at=? WHERE id=? AND lease=?`, until(), store.Now(), id, lease)
				if e != nil {
					cancel()
					return
				}
				n, _ := r.RowsAffected()
				if n != 1 {
					cancel()
					return
				}
			}
		}
	}()
	var runErr error
	for _, item := range b.Plan.Cases {
		if e := callCtx.Err(); e != nil {
			runErr = e
			break
		}
		var state CheckState
		if e := w.DB.QueryRowContext(callCtx, `SELECT state FROM evaluation_check_batch_items WHERE batch_id=? AND case_id=?`, id, item.CaseID).Scan(&state); e != nil {
			runErr = e
			break
		}
		if state == CheckDone || state == CheckFailed && !retry {
			continue
		}
		// A known exhausted batch budget pauses before claiming another logical
		// assessment, preserving its retry allowance for when admission is possible.
		cached, cacheErr := w.Check(callCtx, item.CheckID)
		if a.Cfg.Grader.Via() != "claude-code" && (cacheErr != nil || cached.State != CheckDone) {
			spent, e := a.Budget.SpentKinds(callCtx, b.Plan.kinds())
			if e != nil {
				runErr = e
				break
			}
			if spent+a.Budget.Allowance() > b.Plan.MaxUSD {
				runErr = budget.ErrExhausted
				break
			}
		}
		CheckTransitions.Check("evaluation_check_batch_items", state, CheckRunning)
		// Only this live batch lease can claim or finish an item.
		r, e := w.DB.ExecContext(callCtx, `UPDATE evaluation_check_batch_items SET state='running',error='' WHERE batch_id=? AND case_id=? AND EXISTS(SELECT 1 FROM evaluation_check_batches WHERE id=? AND lease=?)`, id, item.CaseID, id, lease)
		if e != nil {
			runErr = e
			break
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			runErr = ErrConflict
			break
		}
		plan := a.CheckPlan(item.CaseID, item.MaxUSD)
		_, e = w.RunCheck(callCtx, plan, actor, retry, assess)
		next, why := CheckDone, ""
		if e != nil {
			next, why = CheckFailed, e.Error()
		}
		CheckTransitions.Check("evaluation_check_batch_items", CheckRunning, next)
		saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		r, saveErr := w.DB.ExecContext(saveCtx, `UPDATE evaluation_check_batch_items SET state=?,error=? WHERE batch_id=? AND case_id=? AND EXISTS(SELECT 1 FROM evaluation_check_batches WHERE id=? AND lease=?)`, next, why, id, item.CaseID, id, lease)
		stop()
		if saveErr != nil {
			runErr = saveErr
			break
		}
		n, _ = r.RowsAffected()
		if n != 1 {
			runErr = ErrConflict
			break
		}
		if errors.Is(e, budget.ErrExhausted) {
			runErr = e
			break
		}
		if e != nil && callCtx.Err() != nil {
			runErr = callCtx.Err()
			break
		}
	}
	next, why := CheckDone, ""
	if runErr != nil {
		next, why = CheckFailed, runErr.Error()
	}
	CheckBatchTransitions.Check("evaluation_check_batches", CheckRunning, next)
	saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	result, err = w.DB.ExecContext(saveCtx, `UPDATE evaluation_check_batches SET state=?,error=?,lease='',lease_until='',updated_at=? WHERE id=? AND lease=?`, next, why, store.Now(), id, lease)
	if err != nil {
		return err
	}
	n, _ = result.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return runErr
}

type CheckBatchRow struct {
	Case            Case        `json:"case"`
	Check           *CheckView  `json:"check,omitempty"`
	State           CheckState  `json:"state"`
	Error           string      `json:"error,omitempty"`
	InitialRevision int         `json:"initial_revision"`
	Pending         []string    `json:"pending"`
	OtherChecks     []CheckView `json:"other_checks,omitempty"`
}
type CheckBatchReport struct {
	Batch                                                                    CheckBatch      `json:"batch"`
	Rows                                                                     []CheckBatchRow `json:"rows"`
	Total, Done, Failed, Running, Waiting, Agreements, Differences, Resolved int
	SpentUSD                                                                 float64 `json:"spent_usd"`
	Active                                                                   bool    `json:"active"`
}

func (w *Workbench) CheckBatchReport(ctx context.Context, id, actor string, a Assessor) (CheckBatchReport, error) {
	b, err := w.CheckBatch(ctx, id, actor)
	out := CheckBatchReport{Batch: b, Rows: []CheckBatchRow{}, Total: len(b.Plan.Cases)}
	if err != nil {
		return out, err
	}
	out.Active = b.State == CheckRunning && b.LeaseUntil > store.Now()
	for _, item := range b.Plan.Cases {
		row := CheckBatchRow{InitialRevision: item.Revision}
		if err := w.DB.QueryRowContext(ctx, `SELECT state,error FROM evaluation_check_batch_items WHERE batch_id=? AND case_id=?`, id, item.CaseID).Scan(&row.State, &row.Error); err != nil {
			return out, err
		}
		c, e := w.Case(ctx, item.CaseID)
		if e != nil {
			return out, e
		}
		row.Case = c
		views, e := w.CheckViews(ctx, c)
		if e != nil {
			return out, e
		}
		row.Pending = pendingIDs(views)
		for _, v := range views {
			if v.Plan.ID == item.CheckID {
				v := v
				row.Check = &v
			} else if !v.Stale {
				row.OtherChecks = append(row.OtherChecks, v)
			}
		}
		if row.Check != nil && row.Check.State == CheckDone && c.Review.State == "reviewed" {
			row.State, row.Error = CheckDone, ""
		}
		if row.State == CheckDone && (row.Check == nil || row.Check.Grade == nil) {
			row.State, row.Error = CheckFailed, "The saved assessment is unavailable; no prediction is counted."
		}
		switch row.State {
		case CheckDone:
			out.Done++
		case CheckFailed:
			out.Failed++
		case CheckRunning:
			out.Running++
		default:
			out.Waiting++
		}
		if row.State == CheckDone && row.Case.Review.State == "reviewed" && row.Check != nil && !row.Check.Stale && row.Check.Grade != nil {
			if len(row.Pending) > 0 {
				out.Differences++
			} else if row.Check.Resolved {
				out.Resolved++
			} else {
				out.Agreements++
			}
		}
		out.Rows = append(out.Rows, row)
	}
	out.SpentUSD, err = a.Budget.SpentKinds(ctx, b.Plan.kinds())
	return out, err
}
