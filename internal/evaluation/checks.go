package evaluation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/state"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

type Assessor struct {
	Cfg    *config.Config
	Budget *budget.Controller
}

// Only evidence and grading context reach the provider. Imported and human
// labels, notes and proposed quotes are not part of grade.Input.
func (a Assessor) Ready() error {
	if why := config.Missing(a.Cfg.Grader.Via()); why != "" {
		return errors.New(why)
	}
	if a.Cfg.Grader.Via() != config.ClaudeCode {
		p, ok := a.Cfg.Pricing[a.Cfg.Grader.Model]
		if !ok || p.Input <= 0 || p.Output <= 0 {
			return errors.New("configure positive input/output pricing for the grader before running a check")
		}
	}
	return nil
}
func (a Assessor) Transport() string {
	args, _ := json.Marshal(answer.CLIArgs)
	return digest([]byte(a.Cfg.Grader.Via() + "\x00" + config.AnthropicBaseURL() + "\x00" + string(args)))
}
func (a Assessor) Estimate(r grade.GoldenRecord, system string) float64 {
	return a.Cfg.Cost(a.Cfg.Grader.Model, config.Usage{Input: int64(len(system)+len(grade.Input(r.Config(a.Cfg), r.SetFor(), r.Prompt, r.Answer)))/4 + 400, Output: 16000})
}
func (a Assessor) Assess(ctx context.Context, r grade.GoldenRecord, system, operation string, limit float64) (grade.Grade, float64, error) {
	if err := a.Ready(); err != nil {
		return grade.Grade{}, 0, err
	}
	scoped, err := a.Budget.Scoped("provider-attempt/"+operation, limit)
	if err != nil {
		return grade.Grade{}, 0, err
	}
	ctx = llm.WithOperation(llm.WithAccounting(ctx, scoped), operation)
	return grade.AnswerWithSystem(ctx, r.Config(a.Cfg), r.SetFor(), r.Prompt, r.Answer, system)
}

type CheckPlan struct {
	ID        string  `json:"id"`
	CaseID    string  `json:"case_id"`
	Rubric    string  `json:"rubric"`
	Model     string  `json:"model"`
	Transport string  `json:"transport"`
	Evaluator string  `json:"evaluator"`
	Prompt    string  `json:"prompt"`
	MaxUSD    float64 `json:"admission_budget_usd"`
}

func (a Assessor) CheckPlan(caseID string, maxUSD float64) CheckPlan {
	p := CheckPlan{CaseID: caseID, Rubric: Rubric(), Model: a.Cfg.Grader.Ref(), Transport: a.Transport(), Evaluator: grade.EvaluatorDigest(), Prompt: grade.System}
	b, _ := json.Marshal(p)
	p.ID = "ek-" + digest(b)
	p.MaxUSD = maxUSD
	return p
}

func evidenceRecord(r grade.GoldenRecord) grade.GoldenRecord {
	r.Expected = grade.GoldenExpected{}
	r.Note = ""
	r.Evidence = ""
	r.LabeledBy = ""
	r.Source = nil
	return r
}

type Check struct {
	Plan       CheckPlan    `json:"plan"`
	State      CheckState   `json:"state"`
	Attempt    int          `json:"attempt"`
	Grade      *grade.Grade `json:"grade,omitempty"`
	Error      string       `json:"error,omitempty"`
	Actor      string       `json:"actor"`
	UpdatedAt  string       `json:"updated_at"`
	LeaseUntil string       `json:"lease_until,omitempty"`
}

// A lost worker has no prediction. Once its lease expires it must not block
// approval forever, including when it crashed on its final allowed attempt.
func (c *Check) expired() {
	if c.State == CheckRunning && c.LeaseUntil < store.Now() {
		c.State = CheckFailed
		c.Error = "The worker's lease expired without a saved assessment. The result is unknown."
	}
}

func (w *Workbench) Check(ctx context.Context, id string) (Check, error) {
	var c Check
	var plan, result string
	err := w.DB.QueryRowContext(ctx, `SELECT plan,state,attempt,result,error,actor,updated_at,lease_until FROM evaluation_checks WHERE id=?`, id).Scan(&plan, &c.State, &c.Attempt, &result, &c.Error, &c.Actor, &c.UpdatedAt, &c.LeaseUntil)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal([]byte(plan), &c.Plan); err != nil {
		return c, err
	}
	if result != "" {
		c.Grade = &grade.Grade{}
		err = json.Unmarshal([]byte(result), c.Grade)
	}
	c.expired()
	return c, err
}

// Pausing work is different from an assessment failure. Provider spending is
// still settled by the accounting layer; only the logical retry allowance is
// returned, so resume can select this assessment without explicit retry.
func assessmentPaused(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, budget.ErrExhausted)
}

type AssessFunc func(context.Context, grade.GoldenRecord, string, string, float64) (grade.Grade, float64, error)

func (w *Workbench) RunCheck(ctx context.Context, p CheckPlan, actor string, retry bool, assess AssessFunc) (Check, error) {
	identity := p
	identity.ID = ""
	identity.MaxUSD = 0
	identityJSON, _ := json.Marshal(identity)
	if p.ID != "ek-"+digest(identityJSON) || p.Model == "" || p.Transport == "" {
		return Check{}, errors.New("invalid check plan")
	}
	if p.Rubric != Rubric() || p.Prompt != grade.System || p.Evaluator != grade.EvaluatorDigest() {
		return Check{}, errors.New("the evaluator changed; reload the check preview")
	}
	if strings.TrimSpace(actor) == "" || len(actor) > 200 || p.MaxUSD <= 0 || math.IsNaN(p.MaxUSD) || math.IsInf(p.MaxUSD, 0) || p.MaxUSD > 100 {
		return Check{}, errors.New("identify the reviewer and choose an admission budget above zero and at most $100")
	}
	c, err := w.Case(ctx, p.CaseID)
	if err != nil {
		return Check{}, err
	}
	if !c.FirstReviewed || c.Review.State != "reviewed" {
		return Check{}, errors.New("save a human judgment before requesting an independent check")
	}
	b, _ := json.Marshal(p)
	now := store.Now()
	CheckTransitions.Check("evaluation_checks", state.Start, CheckReady)
	if _, err = w.DB.ExecContext(ctx, `INSERT OR IGNORE INTO evaluation_checks(id,case_id,plan,state,actor,created_at,updated_at) VALUES (?,?,?,'ready',?,?,?)`, p.ID, p.CaseID, string(b), actor, now, now); err != nil {
		return Check{}, err
	}
	previous, err := w.Check(ctx, p.ID)
	if err != nil {
		return Check{}, err
	}
	p = previous.Plan
	if previous.State == "done" {
		return previous, nil
	}
	if previous.State != CheckReady && previous.Attempt > 0 && !retry {
		return previous, errors.New("this check already started; use explicit retry for a failed or interrupted check")
	}
	lease := rand.Text()
	CheckTransitions.Check("evaluation_checks", previous.State, CheckRunning)
	until := time.Now().UTC().Add(4 * time.Minute).Format(time.RFC3339)
	result, err := w.DB.ExecContext(ctx, `UPDATE evaluation_checks SET state='running',attempt=attempt+1,lease=?,lease_until=?,error='',updated_at=? WHERE id=? AND attempt<2 AND (state IN ('ready','failed') OR (state='running' AND lease_until<?))`, lease, until, now, p.ID, now)
	if err != nil {
		return previous, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return previous, errors.New("check is already running or its two-attempt limit was reached")
	}
	// Timeout is shorter than the lease. A crashed process can be retried after
	// expiry; an old worker cannot overwrite a later worker's result.
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	g, _, callErr := assess(callCtx, evidenceRecord(c.Record), p.Prompt, "evaluation/check/"+p.ID, p.MaxUSD)
	if callErr != nil && callCtx.Err() != nil {
		callErr = errors.Join(callErr, callCtx.Err())
	}
	cancel()
	if callErr == nil {
		expected := grade.GoldenExpectedOf(g, c.Record.Brand.Name)
		r := c.Record
		r.Expected = expected
		if e := r.Validate(); e != nil {
			callErr = fmt.Errorf("invalid assessment: %w", e)
		}
	}
	nextState, raw, why := CheckDone, "", ""
	refund := 0
	if callErr != nil {
		nextState = CheckFailed
		why = "Assessment failed: " + llm.Truncate(callErr.Error(), 1500)
		if assessmentPaused(callErr) {
			nextState, refund = CheckReady, 1
			why = "Assessment paused: " + llm.Truncate(callErr.Error(), 1500)
		}
	} else {
		b, _ := json.Marshal(g)
		raw = string(b)
	}
	saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer stop()
	CheckTransitions.Check("evaluation_checks", CheckRunning, nextState)
	result, err = w.DB.ExecContext(saveCtx, `UPDATE evaluation_checks SET state=?,attempt=attempt-?,result=?,error=?,lease='',lease_until='',updated_at=? WHERE id=? AND lease=? AND lease_until>=?`, nextState, refund, raw, why, store.Now(), p.ID, lease, store.Now())
	if err != nil {
		return Check{}, err
	}
	n, _ = result.RowsAffected()
	if n != 1 {
		return Check{}, ErrConflict
	}
	saved, err := w.Check(saveCtx, p.ID)
	if err != nil {
		return saved, err
	}
	return saved, callErr
}

type checkQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type CheckView struct {
	Check
	Fields   []string
	Resolved bool
	Stale    bool
}

func differences(j Judgment, g grade.Grade, brand, answer string) []string {
	a, b := j.Expected(), grade.GoldenExpectedOf(g, brand)
	out := []string{}
	if a.Verdict != b.Verdict {
		out = append(out, "verdict")
	}
	if a.TopPick != b.TopPick {
		out = append(out, "top pick")
	}
	if a.OtherProduct != b.OtherProduct {
		out = append(out, "namesake only")
	}
	if a.Tone != b.Tone {
		out = append(out, "tone")
	}
	if g.Verdict != grade.Absent && grade.Quote(answer, g.Evidence) == "" {
		out = append(out, "missing or invalid supporting quote")
	}
	return out
}
func checkViews(ctx context.Context, q checkQuery, c Case) ([]CheckView, error) {
	rows, err := q.QueryContext(ctx, `SELECT plan,state,attempt,result,error,actor,updated_at,lease_until,EXISTS(SELECT 1 FROM evaluation_resolutions r WHERE r.check_id=k.id AND r.case_id=k.case_id AND r.revision=?) FROM evaluation_checks k WHERE case_id=? ORDER BY created_at,id`, c.Review.Revision, c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CheckView{}
	for rows.Next() {
		var v CheckView
		var p, g string
		if err := rows.Scan(&p, &v.State, &v.Attempt, &g, &v.Error, &v.Actor, &v.UpdatedAt, &v.LeaseUntil, &v.Resolved); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(p), &v.Plan); err != nil {
			return nil, err
		}
		v.Stale = v.Plan.Rubric != Rubric() || v.Plan.Evaluator != grade.EvaluatorDigest()
		v.expired()
		if g != "" {
			v.Grade = &grade.Grade{}
			if err := json.Unmarshal([]byte(g), v.Grade); err != nil {
				return nil, err
			}
			v.Fields = differences(c.Review.Judgment, *v.Grade, c.Record.Brand.Name, c.Record.Answer)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (w *Workbench) CheckViews(ctx context.Context, c Case) ([]CheckView, error) {
	return checkViews(ctx, w.DB, c)
}
func pendingIDs(vs []CheckView) []string {
	ids := []string{}
	for _, v := range vs {
		if v.State == "done" && !v.Resolved && len(v.Fields) > 0 {
			ids = append(ids, v.Plan.ID)
		}
	}
	sort.Strings(ids)
	return ids
}
func (w *Workbench) PendingChecks(ctx context.Context, c Case) ([]string, error) {
	vs, err := w.CheckViews(ctx, c)
	return pendingIDs(vs), err
}

type Resolution struct {
	Checks      []string
	Action      string
	Explanation string
	// scope is set only by the batch operation, from its persisted membership.
	scope []string
}
type ResolutionRecord struct {
	CheckID     string `json:"check_id"`
	Revision    int    `json:"revision"`
	Action      string `json:"action"`
	Explanation string `json:"explanation"`
	Actor       string `json:"actor"`
	CreatedAt   string `json:"created_at"`
}

func (w *Workbench) ResolutionHistory(ctx context.Context, id string) ([]ResolutionRecord, error) {
	rows, err := w.DB.QueryContext(ctx, `SELECT check_id,revision,action,explanation,actor,created_at FROM evaluation_resolutions WHERE case_id=? ORDER BY revision,check_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResolutionRecord{}
	for rows.Next() {
		var r ResolutionRecord
		if err := rows.Scan(&r.CheckID, &r.Revision, &r.Action, &r.Explanation, &r.Actor, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (w *Workbench) Resolve(ctx context.Context, id string, revision int, j Judgment, actor, rubric string, r Resolution) (Review, error) {
	if r.Action != "keep" && r.Action != "change" || strings.TrimSpace(r.Explanation) == "" || len([]rune(r.Explanation)) > 4000 {
		return Review{}, errors.New("choose keep or change and explain the resolution")
	}
	return w.save(ctx, id, revision, "judgment", j, actor, rubric, &r)
}
