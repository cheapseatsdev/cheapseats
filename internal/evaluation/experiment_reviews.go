package evaluation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"sort"
	"strings"
)

type MaskedVariant struct {
	Label       string
	Name        string
	Predictions []Prediction
}
type BlindedReview struct {
	Member   Member
	Variants []MaskedVariant
	Review   Review
	Revealed bool
}

func (w *Workbench) BlindedReview(ctx context.Context, experimentID, caseID, actor string) (BlindedReview, error) {
	e, err := w.Experiment(ctx, experimentID)
	if err != nil {
		return BlindedReview{}, err
	}
	d, err := w.Dataset(ctx, e.Plan.DatasetID)
	if err != nil {
		return BlindedReview{}, err
	}
	if e.Plan.Evaluator != grade.EvaluatorDigest() || e.Plan.Rubric != Rubric() {
		return BlindedReview{}, errors.New("use the original evaluator build and rules for reviewing this comparison")
	}
	selected := false
	for _, id := range e.Plan.Cases {
		if id == caseID {
			selected = true
		}
	}
	if !selected {
		return BlindedReview{}, sql.ErrNoRows
	}
	out := BlindedReview{Review: Review{State: "unreviewed"}, Variants: []MaskedVariant{}}
	for _, m := range d.Members {
		if m.CaseID == caseID {
			out.Member = m
			break
		}
	}
	var raw string
	err = w.DB.QueryRowContext(ctx, `SELECT revision,judgment,state,created_at,EXISTS(SELECT 1 FROM evaluation_experiment_reviews h WHERE h.experiment_id=? AND h.case_id=? AND h.actor=? AND h.state='reviewed') FROM evaluation_experiment_reviews WHERE experiment_id=? AND case_id=? AND actor=? ORDER BY revision DESC LIMIT 1`, experimentID, caseID, actor, experimentID, caseID, actor).Scan(&out.Review.Revision, &raw, &out.Review.State, &out.Review.UpdatedAt, &out.Revealed)
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &out.Review.Judgment); err != nil {
			return out, err
		}
		out.Review.Actor = actor
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	predictions, err := w.Predictions(ctx, experimentID)
	if err != nil {
		return out, err
	}
	order := make([]int, len(e.Plan.Variants))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		return digest([]byte(experimentID+caseID+string(rune('a'+order[i])))) < digest([]byte(experimentID+caseID+string(rune('a'+order[j]))))
	})
	for index, variant := range order {
		v := MaskedVariant{Label: string(rune('A' + index)), Predictions: []Prediction{}}
		if out.Revealed {
			v.Name = e.Plan.Variants[variant].Name
		}
		for _, p := range predictions {
			if p.CaseID == caseID && p.Variant == variant {
				p.Variant = 0
				v.Predictions = append(v.Predictions, p)
			}
		}
		out.Variants = append(out.Variants, v)
	}
	// The reference is intentionally removed from the unsubmitted review object,
	// not merely hidden in a disclosure. The server gets only its evidence.
	if !out.Revealed {
		out.Member.Record = evidenceRecord(out.Member.Record)
		out.Member.Judgment = Judgment{}
		out.Member.Actor = ""
		out.Member.Resolutions = nil
	}
	return out, nil
}
func (w *Workbench) SaveExperimentReview(ctx context.Context, experimentID, caseID, actor string, revision int, kind string, j Judgment) (Review, error) {
	if kind != "judgment" && kind != "unresolved" {
		return Review{}, errors.New("choose a comparison judgment or leave it unresolved")
	}
	if strings.TrimSpace(actor) == "" || len(actor) > 200 || strings.TrimSpace(j.Note) == "" {
		return Review{}, errors.New("identify the reviewer and explain this comparison judgment in the review note")
	}
	b, err := w.BlindedReview(ctx, experimentID, caseID, actor)
	if err != nil {
		return Review{}, err
	}
	j, err = validate(j, b.Member.Record.Answer, kind)
	if err != nil {
		return Review{}, err
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return Review{}, err
	}
	defer tx.Rollback()
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(revision),0) FROM evaluation_experiment_reviews WHERE experiment_id=? AND case_id=? AND actor=?`, experimentID, caseID, actor).Scan(&current); err != nil {
		return Review{}, err
	}
	if current != revision {
		return Review{}, ErrConflict
	}
	status := kind
	if kind == "judgment" {
		status = "reviewed"
	}
	from := ReviewState(b.Review.State)
	if current == 0 {
		from = ""
	}
	ReviewTransitions.Check("evaluation_experiment_reviews", from, ReviewState(status))
	saved := Review{Revision: revision + 1, State: status, Judgment: j, Actor: actor, UpdatedAt: store.Now()}
	raw, _ := json.Marshal(j)
	if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_experiment_reviews VALUES (?,?,?,?,?,?,?)`, experimentID, caseID, actor, saved.Revision, string(raw), saved.State, saved.UpdatedAt); err != nil {
		return Review{}, err
	}
	if err := tx.Commit(); err != nil {
		return Review{}, err
	}
	return saved, nil
}
