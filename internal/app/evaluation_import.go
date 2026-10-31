package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

const MaxEvaluationRunAnswers = 500

var ErrEvaluationRunChanged = errors.New("this run changed since the import preview; reload and choose the saved answers again")

type EvaluationRunAnswer struct {
	ID                      int64
	Prompt, Answerer, Track string
	Problem                 string
	Record                  *grade.GoldenRecord `json:"-"`
}

type EvaluationRun struct {
	ID, Name, Brand, Digest string
	Answers                 []EvaluationRunAnswer
}

// EvaluationRun reads one coherent database snapshot, including the run's saved
// identity and competitors. It neither prepares a grouping nor imports anything.
// Today's config must not silently reinterpret an old brand or buyer question.
func (a *App) EvaluationRun(ctx context.Context, id string) (EvaluationRun, error) {
	d := EvaluationRun{ID: id}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	var snapshot, method, status string
	if err := tx.QueryRowContext(ctx, `SELECT name,snapshot,method,status FROM runs WHERE id=?`, id).Scan(&d.Name, &snapshot, &method, &status); err != nil {
		return d, err
	}
	if status != string(run.Done) {
		return d, errors.New("choose a finished run before importing evaluation evidence")
	}
	var snap run.Snapshot
	if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
		return d, err
	}
	d.Brand = snap.Brand.Name
	rows, err := tx.QueryContext(ctx, `SELECT id,prompt_idx,prompt,prompt_id,answerer,track,status,COALESCE(answer,''),COALESCE(grade,'') FROM results WHERE run_id=? ORDER BY id LIMIT 10001`, id)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	// Include raw source bytes in the preview digest, even for ineligible rows.
	state := sha256.New()
	enc := json.NewEncoder(state)
	enc.Encode([]string{id, snapshot, method, status})
	for rows.Next() {
		var row EvaluationRunAnswer
		var idx int
		var pid, resultStatus, answerJSON, gradeJSON string
		if err := rows.Scan(&row.ID, &idx, &row.Prompt, &pid, &row.Answerer, &row.Track, &resultStatus, &answerJSON, &gradeJSON); err != nil {
			return d, err
		}
		enc.Encode([]any{row.ID, idx, row.Prompt, pid, row.Answerer, row.Track, resultStatus, answerJSON, gradeJSON})
		var ans answer.Answer
		var g grade.Grade
		switch {
		case resultStatus != string(run.ResultDone):
			row.Problem = "This answer is not finished."
		case json.Unmarshal([]byte(answerJSON), &ans) != nil || strings.TrimSpace(ans.Text) == "":
			row.Problem = "No complete saved answer text is available."
		case json.Unmarshal([]byte(gradeJSON), &g) != nil:
			row.Problem = "No readable saved model grade is available."
		case strings.TrimSpace(snap.Brand.Name) == "":
			row.Problem = "The run did not record its intended brand identity."
		case idx < 0 || idx >= len(snap.Set.Prompts) || snap.Set.Prompts[idx].Text != row.Prompt:
			row.Problem = "The saved question cannot be matched to the run's prompt snapshot."
		default:
			p := snap.Set.Prompts[idx]
			r := grade.GoldenRecord{
				ID: grade.GoldenID(ans.Text), Set: snap.Set.ID, PromptID: pid, Prompt: row.Prompt, Intent: p.Intent,
				Brand: grade.GoldenBrandOf(snap.Brand), Competitors: snap.Set.Competitors,
				Answer: ans.Text, Expected: grade.GoldenExpectedOf(g, snap.Brand.Name),
				Evidence: grade.Quote(ans.Text, g.Evidence), LabeledBy: "claude",
				Source: &grade.GoldenSource{Run: id, Result: row.ID, Answerer: row.Answerer, Track: row.Track, Grader: snap.Grader, Method: method},
			}
			if err := r.Validate(); err != nil {
				row.Problem = "The saved model grade or context is incomplete; repair it before importing."
			} else {
				row.Record = &r
			}
		}
		d.Answers = append(d.Answers, row)
		if len(d.Answers) > 10000 {
			return d, errors.New("this run exceeds the 10,000-answer import preview limit")
		}
	}
	if err := rows.Err(); err != nil {
		return d, err
	}
	if err := rows.Close(); err != nil {
		return d, err
	}
	if err := tx.Commit(); err != nil {
		return d, err
	}
	d.Digest = hex.EncodeToString(state.Sum(nil))
	return d, nil
}

// ImportEvaluationRun requires the exact preview and explicit membership. Its
// immutable records remain proposals; ImportJSONL preserves existing reviews and
// never updates production answers, grades, dataset versions or exposure history.
func (a *App) ImportEvaluationRun(ctx context.Context, id string, ids []int64, preview string) (evaluation.Import, error) {
	if len(ids) == 0 || len(ids) > MaxEvaluationRunAnswers {
		return evaluation.Import{}, fmt.Errorf("select between 1 and %d saved answers", MaxEvaluationRunAnswers)
	}
	d, err := a.EvaluationRun(ctx, id)
	if err != nil {
		return evaluation.Import{}, err
	}
	if preview == "" || preview != d.Digest {
		return evaluation.Import{}, ErrEvaluationRunChanged
	}
	selected := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || selected[id] {
			return evaluation.Import{}, errors.New("each selected answer must appear exactly once")
		}
		selected[id] = true
	}
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	n := 0
	for _, row := range d.Answers {
		if !selected[row.ID] {
			continue
		}
		if row.Record == nil {
			return evaluation.Import{}, errors.New("one of the selected answers cannot be imported; reload the preview")
		}
		if err := enc.Encode(row.Record); err != nil {
			return evaluation.Import{}, err
		}
		n++
	}
	if n != len(ids) {
		return evaluation.Import{}, errors.New("a selected answer does not belong to this run")
	}
	return evaluation.New(a.DB.DB).ImportJSONL(ctx, "saved run "+id, data.Bytes())
}
