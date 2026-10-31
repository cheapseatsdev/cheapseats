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

	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/state"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

type Variant struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
	Digest string `json:"digest"`
}
type ExperimentPlan struct {
	ID          string    `json:"id"`
	DatasetID   string    `json:"dataset_id"`
	Partition   string    `json:"partition"`
	Model       string    `json:"model"`
	Transport   string    `json:"transport"`
	Evaluator   string    `json:"evaluator"`
	Rubric      string    `json:"rubric"`
	Variants    []Variant `json:"variants"`
	Cases       []string  `json:"cases"`
	Repetitions int       `json:"repetitions"`
	Calls       int       `json:"assessment_calls"`
	MaxUSD      float64   `json:"admission_budget_usd"`
	Frozen      bool      `json:"candidates_frozen"`
}
type Experiment struct {
	Plan      ExperimentPlan `json:"plan"`
	Actor     string         `json:"actor"`
	CreatedAt string         `json:"created_at"`
}

func (w *Workbench) PrepareExperiment(ctx context.Context, a Assessor, datasetID, partition string, candidates []Variant, maxUSD float64, frozen bool) (ExperimentPlan, error) {
	p := ExperimentPlan{ID: "ex-" + rand.Text(), DatasetID: datasetID, Partition: partition, Model: a.Cfg.Grader.Ref(), Transport: a.Transport(), Evaluator: grade.EvaluatorDigest(), Rubric: Rubric(), Repetitions: 2, MaxUSD: maxUSD, Frozen: frozen}
	if partition != "development" && partition != "held-out" || len(candidates) < 1 || len(candidates) > 2 || maxUSD <= 0 || maxUSD > 1000 || math.IsNaN(maxUSD) || math.IsInf(maxUSD, 0) {
		return p, errors.New("choose development or held-out, one or two candidates, and a positive admission budget of at most $1,000")
	}
	if partition == "held-out" && !frozen {
		return p, errors.New("freeze all candidates before reserving held-out evaluation")
	}
	d, err := w.Dataset(ctx, datasetID)
	if err != nil {
		return p, err
	}
	if d.Rubric != Rubric() || d.Rules != grade.System {
		return p, errors.New("approve a dataset under the current rubric before comparing graders")
	}
	p.Variants = []Variant{{Name: "Production control", Prompt: d.Rules, Digest: digest([]byte(d.Rules))}}
	names := map[string]bool{"Production control": true}
	prompts := map[string]bool{d.Rules: true}
	for _, v := range candidates {
		v.Name = strings.TrimSpace(v.Name)
		if v.Name == "" || len(v.Name) > 100 || strings.TrimSpace(v.Prompt) == "" || len(v.Prompt) > 65536 || names[v.Name] || prompts[v.Prompt] {
			return p, errors.New("give each candidate a unique name and a distinct prompt of at most 64 KiB")
		}
		names[v.Name] = true
		prompts[v.Prompt] = true
		v.Digest = digest([]byte(v.Prompt))
		p.Variants = append(p.Variants, v)
	}
	p.Cases = []string{}
	for _, m := range d.Members {
		if m.Group.Partition == partition {
			p.Cases = append(p.Cases, m.CaseID)
		}
	}
	sort.Strings(p.Cases)
	if len(p.Cases) == 0 || len(p.Cases) > 500 {
		return p, errors.New("the selected partition must contain 1–500 approved cases; cases are never silently sampled away")
	}
	p.Calls = len(p.Cases) * len(p.Variants) * p.Repetitions
	return p, nil
}
func holdoutSignatures(m Member) []string {
	return []string{"group:" + m.Group.ID, "question:" + digest([]byte(strings.Join(words(m.Record.Prompt), " "))), "answer:" + digest([]byte(strings.Join(words(m.Record.Answer), " ")))}
}

func (w *Workbench) CreateExperiment(ctx context.Context, a Assessor, p ExperimentPlan, actor string) (Experiment, error) {
	if strings.TrimSpace(actor) == "" || len(actor) > 200 || !strings.HasPrefix(p.ID, "ex-") || len(p.ID) < 12 || len(p.ID) > 100 {
		return Experiment{}, errors.New("an explicit reviewer and valid comparison preview are required")
	}
	if len(p.Variants) < 2 {
		return Experiment{}, errors.New("a comparison needs a candidate")
	}
	expected, err := w.PrepareExperiment(ctx, a, p.DatasetID, p.Partition, p.Variants[1:], p.MaxUSD, p.Frozen)
	if err != nil {
		return Experiment{}, err
	}
	expected.ID = p.ID
	b, _ := json.Marshal(p)
	wanted, _ := json.Marshal(expected)
	if string(b) != string(wanted) {
		return Experiment{}, errors.New("the comparison configuration changed; create a fresh preview")
	}
	d, err := w.Dataset(ctx, p.DatasetID)
	if err != nil {
		return Experiment{}, err
	}
	members := map[string]Member{}
	for _, m := range d.Members {
		members[m.CaseID] = m
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return Experiment{}, err
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT plan FROM evaluation_experiments WHERE id=?`, p.ID).Scan(&previous)
	if err == nil {
		tx.Rollback()
		if previous != string(b) {
			return Experiment{}, ErrConflict
		}
		return w.Experiment(ctx, p.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Experiment{}, err
	}
	now := store.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_experiments VALUES (?,?,?,?,?)`, p.ID, p.DatasetID, string(b), actor, now); err != nil {
		return Experiment{}, err
	}
	if p.Partition == "held-out" {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c.id,c.record FROM evaluation_cases c JOIN evaluation_predictions p ON p.case_id=c.id LIMIT 10001`)
		if err != nil {
			return Experiment{}, err
		}
		previous := []groupEvidence{}
		for rows.Next() {
			var id, raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return Experiment{}, err
			}
			var record grade.GoldenRecord
			if err := json.Unmarshal([]byte(raw), &record); err != nil {
				rows.Close()
				return Experiment{}, err
			}
			previous = append(previous, prepareGroupEvidence(record))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return Experiment{}, err
		}
		if len(previous) > 10000 {
			return Experiment{}, errors.New("too much prior evidence for the bounded lexical holdout check; arrange a separately reviewed fresh holdout")
		}
		for _, id := range p.Cases {
			current := prepareGroupEvidence(members[id].Record)
			for _, old := range previous {
				if relatedEvidence(current, old) {
					return Experiment{}, errors.New("this question or near-duplicate answer was already exposed in a comparison; collect fresh untouched evidence")
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, id := range p.Cases {
		for _, signature := range holdoutSignatures(members[id]) {
			if seen[signature] {
				continue
			}
			seen[signature] = true
			if p.Partition == "held-out" {
				var used string
				e := tx.QueryRowContext(ctx, `SELECT experiment_id FROM evaluation_evidence_exposures WHERE signature=?`, signature).Scan(&used)
				if e == nil {
					return Experiment{}, errors.New("this held-out evidence was already exposed in a comparison; use development or collect fresh untouched evidence")
				}
				if !errors.Is(e, sql.ErrNoRows) {
					return Experiment{}, e
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO evaluation_evidence_exposures VALUES (?,?)`, signature, p.ID); err != nil {
				return Experiment{}, err
			}
		}
	}
	CheckTransitions.Check("evaluation_predictions", state.Start, CheckReady)
	for _, id := range p.Cases {
		for variant := range p.Variants {
			for repetition := 0; repetition < p.Repetitions; repetition++ {
				if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_predictions(experiment_id,case_id,variant,repetition,state,updated_at) VALUES (?,?,?,?,?,?)`, p.ID, id, variant, repetition, CheckReady, now); err != nil {
					return Experiment{}, err
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Experiment{}, err
	}
	return w.Experiment(ctx, p.ID)
}
func (w *Workbench) Experiment(ctx context.Context, id string) (Experiment, error) {
	var e Experiment
	var raw string
	err := w.DB.QueryRowContext(ctx, `SELECT plan,actor,created_at FROM evaluation_experiments WHERE id=?`, id).Scan(&raw, &e.Actor, &e.CreatedAt)
	if err != nil {
		return e, err
	}
	err = json.Unmarshal([]byte(raw), &e.Plan)
	return e, err
}
func (w *Workbench) Experiments(ctx context.Context, datasetID string) ([]Experiment, error) {
	rows, err := w.DB.QueryContext(ctx, `SELECT plan,actor,created_at FROM evaluation_experiments WHERE dataset_id=? ORDER BY created_at DESC,id`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Experiment{}
	for rows.Next() {
		var e Experiment
		var raw string
		if err := rows.Scan(&raw, &e.Actor, &e.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &e.Plan); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type Prediction struct {
	CaseID     string       `json:"case_id"`
	Variant    int          `json:"variant"`
	Repetition int          `json:"repetition"`
	State      CheckState   `json:"state"`
	Attempt    int          `json:"attempt"`
	Grade      *grade.Grade `json:"grade,omitempty"`
	Error      string       `json:"error,omitempty"`
	Cost       float64      `json:"cost_usd"`
	LatencyMS  int64        `json:"latency_ms"`
}

func (w *Workbench) Predictions(ctx context.Context, id string) ([]Prediction, error) {
	rows, err := w.DB.QueryContext(ctx, `SELECT case_id,variant,repetition,state,attempt,result,error,cost_usd,latency_ms FROM evaluation_predictions WHERE experiment_id=? ORDER BY case_id,variant,repetition`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Prediction{}
	for rows.Next() {
		var p Prediction
		var raw string
		if err := rows.Scan(&p.CaseID, &p.Variant, &p.Repetition, &p.State, &p.Attempt, &raw, &p.Error, &p.Cost, &p.LatencyMS); err != nil {
			return nil, err
		}
		if raw != "" {
			p.Grade = &grade.Grade{}
			if err := json.Unmarshal([]byte(raw), p.Grade); err != nil {
				return nil, err
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RunExperiment resumes persisted work. Completed repetitions are never cached
// together: repeated control/candidate calls are separate measurements.
func (w *Workbench) RunExperiment(ctx context.Context, id string, a Assessor, retry bool, assess AssessFunc) error {
	e, err := w.Experiment(ctx, id)
	if err != nil {
		return err
	}
	p := e.Plan
	if p.Model != a.Cfg.Grader.Ref() || p.Transport != a.Transport() || p.Evaluator != grade.EvaluatorDigest() || p.Rubric != Rubric() {
		return errors.New("model, transport, rules or evaluator changed; resume needs the original build/configuration")
	}
	d, err := w.Dataset(ctx, p.DatasetID)
	if err != nil {
		return err
	}
	members := map[string]Member{}
	for _, m := range d.Members {
		members[m.CaseID] = m
	}
	// One sweep bounds retries. A failed row cannot be selected repeatedly within
	// a request. Expired claims are retried only after explicit resume consent.
	for _, caseID := range p.Cases {
		for variant := range p.Variants {
			for repetition := 0; repetition < p.Repetitions; repetition++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				var previous CheckState
				var attempt int
				var until string
				err := w.DB.QueryRowContext(ctx, `SELECT state,attempt,lease_until FROM evaluation_predictions WHERE experiment_id=? AND case_id=? AND variant=? AND repetition=?`, id, caseID, variant, repetition).Scan(&previous, &attempt, &until)
				if err != nil {
					return err
				}
				if previous == CheckDone || attempt >= 2 || previous != CheckReady && (!retry || previous == CheckRunning && until >= store.Now()) {
					continue
				}
				lease := rand.Text()
				expires := time.Now().UTC().Add(4 * time.Minute).Format(time.RFC3339)
				CheckTransitions.Check("evaluation_predictions", previous, CheckRunning)
				result, err := w.DB.ExecContext(ctx, `UPDATE evaluation_predictions SET state=?,attempt=attempt+1,lease=?,lease_until=?,error='',updated_at=? WHERE experiment_id=? AND case_id=? AND variant=? AND repetition=? AND attempt=? AND (state IN ('ready','failed') OR (state='running' AND lease_until<?))`, CheckRunning, lease, expires, store.Now(), id, caseID, variant, repetition, attempt, store.Now())
				if err != nil {
					return err
				}
				n, _ := result.RowsAffected()
				if n != 1 {
					continue
				}
				start := time.Now()
				callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				g, cost, callErr := assess(callCtx, evidenceRecord(members[caseID].Record), p.Variants[variant].Prompt, "evaluation/experiment/"+id, p.MaxUSD)
				if callErr != nil && callCtx.Err() != nil {
					callErr = errors.Join(callErr, callCtx.Err())
				}
				cancel()
				if callErr == nil {
					r := members[caseID].Record
					r.Expected = grade.GoldenExpectedOf(g, r.Brand.Name)
					callErr = r.Validate()
				}
				next, raw, why := CheckDone, "", ""
				refund := 0
				if callErr != nil {
					next = CheckFailed
					why = llm.Truncate(callErr.Error(), 1500)
					if assessmentPaused(callErr) {
						next, refund = CheckReady, 1
					}
				} else {
					b, _ := json.Marshal(g)
					raw = string(b)
				}
				if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
					cost = 0
				}
				CheckTransitions.Check("evaluation_predictions", CheckRunning, next)
				saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
				result, err = w.DB.ExecContext(saveCtx, `UPDATE evaluation_predictions SET state=?,attempt=attempt-?,result=?,error=?,cost_usd=cost_usd+?,latency_ms=latency_ms+?,lease='',lease_until='',updated_at=? WHERE experiment_id=? AND case_id=? AND variant=? AND repetition=? AND lease=? AND lease_until>=?`, next, refund, raw, why, cost, time.Since(start).Milliseconds(), store.Now(), id, caseID, variant, repetition, lease, store.Now())
				stop()
				if err != nil {
					return err
				}
				n, _ = result.RowsAffected()
				if n != 1 {
					return ErrConflict
				}
				if assessmentPaused(callErr) {
					return callErr
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
		}
	}
	return nil
}

type VariantReport struct {
	Name                                                                       string
	Digest                                                                     string
	Runs                                                                       []Metrics
	RepeatDifferences, RepeatPairs, Improved, Regressed, Paired, QuoteFailures int
	LatencyMS                                                                  int64
	Failures                                                                   int
	ReviewIssues                                                               []string `json:"release_review_issues,omitempty"`
}
type ExperimentReport struct {
	Experiment              Experiment      `json:"experiment"`
	Dataset                 Dataset         `json:"dataset"`
	Variants                []VariantReport `json:"variants"`
	Predictions             []Prediction    `json:"predictions"`
	Baselines               []Baseline      `json:"baselines"`
	Complete, Total, Failed int
	LedgerUSD               float64
	Interpretation          string
}

func (w *Workbench) ExperimentReport(ctx context.Context, id string) (ExperimentReport, error) {
	e, err := w.Experiment(ctx, id)
	if err != nil {
		return ExperimentReport{}, err
	}
	d, err := w.Dataset(ctx, e.Plan.DatasetID)
	if err != nil {
		return ExperimentReport{}, err
	}
	ps, err := w.Predictions(ctx, id)
	if err != nil {
		return ExperimentReport{}, err
	}
	selected := map[string]bool{}
	for _, id := range e.Plan.Cases {
		selected[id] = true
	}
	ms := []Member{}
	for _, m := range d.Members {
		if selected[m.CaseID] {
			ms = append(ms, m)
		}
	}
	d.Members = ms
	r := ExperimentReport{Experiment: e, Dataset: d, Predictions: ps, Variants: []VariantReport{}, Total: e.Plan.Calls, Baselines: Baselines(d), Interpretation: "Diagnostic comparison; no automatic release acceptance. Review disagreements, coverage and repeat variability before choosing a change."}
	predictions := map[string]grade.GoldenExpected{}
	key := func(caseID string, v, rep int) string { return fmt.Sprintf("%s/%d/%d", caseID, v, rep) }
	for _, p := range ps {
		if p.State == CheckDone && p.Grade != nil {
			r.Complete++
			for _, m := range ms {
				if m.CaseID == p.CaseID {
					predictions[key(p.CaseID, p.Variant, p.Repetition)] = grade.GoldenExpectedOf(*p.Grade, m.Record.Brand.Name)
					break
				}
			}
		} else if p.State == CheckFailed {
			r.Failed++
		}
	}
	for variant, v := range e.Plan.Variants {
		vr := VariantReport{Name: v.Name, Digest: v.Digest, Runs: []Metrics{}}
		for rep := 0; rep < e.Plan.Repetitions; rep++ {
			byCase := map[string]grade.GoldenExpected{}
			for _, m := range ms {
				if got, ok := predictions[key(m.CaseID, variant, rep)]; ok {
					byCase[m.CaseID] = got
				}
				if variant > 0 {
					control, cok := predictions[key(m.CaseID, 0, rep)]
					got, gok := predictions[key(m.CaseID, variant, rep)]
					if cok && gok {
						vr.Paired++
						if control != m.Record.Expected && got == m.Record.Expected {
							vr.Improved++
						}
						if control == m.Record.Expected && got != m.Record.Expected {
							vr.Regressed++
						}
					}
				}
			}
			vr.Runs = append(vr.Runs, Measure(ms, byCase))
		}
		for _, m := range ms {
			a, ok := predictions[key(m.CaseID, variant, 0)]
			b, bok := predictions[key(m.CaseID, variant, 1)]
			if ok && bok {
				vr.RepeatPairs++
				if a != b {
					vr.RepeatDifferences++
				}
			}
		}
		for _, p := range ps {
			if p.Variant != variant {
				continue
			}
			vr.LatencyMS += p.LatencyMS
			if p.State == CheckFailed {
				vr.Failures++
			}
			answer := ""
			for _, m := range ms {
				if m.CaseID == p.CaseID {
					answer = m.Record.Answer
					break
				}
			}
			if p.Grade != nil && p.Grade.Verdict != grade.Absent && grade.Quote(answer, p.Grade.Evidence) == "" {
				vr.QuoteFailures++
			}
		}
		r.Variants = append(r.Variants, vr)
	}
	if err := w.DB.QueryRowContext(ctx, `SELECT coalesce(sum(amount_usd),0) FROM spend_events WHERE kind=?`, "provider-attempt/evaluation/experiment/"+id).Scan(&r.LedgerUSD); err != nil {
		return r, err
	}
	if r.Complete != r.Total {
		r.Interpretation = "Comparison incomplete. Missing and failed predictions retain the original denominator; there is no release verdict."
	}
	for i := 1; i < len(r.Variants); i++ {
		r.Variants[i].ReviewIssues = ComparisonGate(r, i)
	}
	return r, nil
}
