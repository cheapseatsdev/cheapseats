// Package evaluation keeps saved evidence and explicit human judgments apart
// from production grades. Reading/importing evidence never calls a provider.
package evaluation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

var ErrConflict = errors.New("this case changed in another tab; reload and reconcile the saved judgment before saving")

const MaxImportBytes = 16 << 20

type Workbench struct{ DB *sql.DB }

func New(db *sql.DB) *Workbench { return &Workbench{DB: db} }

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func Rubric() string { return digest([]byte(grade.System)) }

// caseID includes grading context, not merely the legacy answer hash. Identical
// answer bytes under different questions/brands/sources must not collapse.
func caseID(r grade.GoldenRecord) string {
	r.Expected = grade.GoldenExpected{}
	r.Evidence, r.Note, r.LabeledBy = "", "", ""
	r.Source = nil // provenance does not change the evidence being judged
	b, _ := json.Marshal(r)
	return "ec-" + digest(b)
}

type ImportRow struct {
	Line   int    `json:"line"`
	CaseID string `json:"case_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Import struct {
	ID        string      `json:"id"`
	Source    string      `json:"source"`
	Rows      []ImportRow `json:"rows"`
	Cases     int         `json:"cases"`
	Blocked   int         `json:"blocked"`
	Duplicate int         `json:"duplicates"`
}

// ImportJSONL is idempotent by source and byte digest. Every nonblank row is
// retained, including malformed records, without touching any production table.
func (w *Workbench) ImportJSONL(ctx context.Context, source string, data []byte) (Import, error) {
	if strings.TrimSpace(source) == "" || len(source) > 500 || len(data) > MaxImportBytes || !utf8.Valid(data) {
		return Import{}, errors.New("import needs a source name and at most 16 MiB of UTF-8 JSONL")
	}
	id := "ei-" + digest(append([]byte(source+"\x00"), data...))
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return Import{}, err
	}
	defer tx.Rollback()
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM evaluation_imports WHERE id=?`, id).Scan(&exists)
	if err == nil {
		tx.Rollback()
		return w.Import(ctx, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Import{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO evaluation_imports VALUES (?,?,?,?)`, id, source, digest(data), store.Now()); err != nil {
		return Import{}, err
	}
	n := 0
	for i, raw := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		n++
		if n > 10000 {
			return Import{}, errors.New("an import can contain at most 10,000 rows")
		}
		var r grade.GoldenRecord
		problem := json.Unmarshal(raw, &r)
		if problem == nil {
			problem = r.Validate()
		}
		var cid any
		why := ""
		if problem != nil {
			why = problem.Error()
		} else {
			cid = caseID(r)
			b, _ := json.Marshal(r)
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO evaluation_cases VALUES (?,?,?)`, cid, string(b), store.Now()); err != nil {
				return Import{}, err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evaluation_import_rows VALUES (?,?,?,?,?)`, id, i+1, string(raw), cid, why); err != nil {
			return Import{}, err
		}
	}
	if n == 0 {
		return Import{}, errors.New("import contains no records")
	}
	if err = tx.Commit(); err != nil {
		return Import{}, err
	}
	return w.Import(ctx, id)
}

func (w *Workbench) Import(ctx context.Context, id string) (Import, error) {
	a := Import{ID: id, Rows: []ImportRow{}}
	if err := w.DB.QueryRowContext(ctx, `SELECT source FROM evaluation_imports WHERE id=?`, id).Scan(&a.Source); err != nil {
		return a, err
	}
	rows, err := w.DB.QueryContext(ctx, `SELECT line,COALESCE(case_id,''),error FROM evaluation_import_rows WHERE import_id=? ORDER BY line`, id)
	if err != nil {
		return a, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var r ImportRow
		if err := rows.Scan(&r.Line, &r.CaseID, &r.Error); err != nil {
			return a, err
		}
		a.Rows = append(a.Rows, r)
		switch {
		case r.Error != "":
			a.Blocked++
		case seen[r.CaseID]:
			a.Duplicate++
		default:
			a.Cases++
			seen[r.CaseID] = true
		}
	}
	return a, rows.Err()
}

type Judgment struct {
	Verdict       grade.Verdict `json:"verdict"`
	TopPick       *bool         `json:"top_pick,omitempty"`
	Tone          grade.Tone    `json:"tone,omitempty"`
	AbsenceReason string        `json:"absence_reason,omitempty"` // not-named | echo | namesake
	Evidence      []string      `json:"evidence,omitempty"`
	FullAnswer    bool          `json:"full_answer,omitempty"`
	Incomplete    bool          `json:"incomplete,omitempty"`
	Issue         string        `json:"issue,omitempty"`
	Note          string        `json:"note,omitempty"`
}

func (j Judgment) Expected() grade.GoldenExpected {
	return grade.GoldenExpected{Verdict: j.Verdict, TopPick: j.TopPick != nil && *j.TopPick, OtherProduct: j.Verdict == grade.Absent && j.AbsenceReason == "namesake", Tone: j.Tone}
}

// validate accepts partial drafts; committing a human judgment is deliberately
// stricter than importing the model's proposal. It checks source spans, not
// semantic correctness, which remains the reviewer's decision.
func validate(j Judgment, answer string, kind string) (Judgment, error) {
	j.Evidence = append([]string(nil), j.Evidence...)
	b, _ := json.Marshal(j)
	if len(b) > 16000 || len(j.Evidence) > 8 || len([]rune(j.Note)) > 4000 {
		return j, errors.New("judgment is too large; use at most eight short evidence passages and a 4,000-character note")
	}
	if kind == "draft" {
		return j, nil
	}
	if kind == "unresolved" {
		if strings.TrimSpace(j.Issue) == "" || strings.TrimSpace(j.Note) == "" {
			return j, errors.New("an unresolved case needs an issue and a note explaining the decision still needed")
		}
		return j, nil
	}
	if kind != "judgment" {
		return j, errors.New("unknown review action")
	}
	if j.Incomplete {
		return j, errors.New("incomplete evidence must remain unresolved")
	}
	if j.Verdict != grade.Absent && j.Verdict != grade.Mentioned && j.Verdict != grade.Recommended {
		return j, errors.New("choose absent, mentioned or recommended")
	}
	if j.Verdict == grade.Recommended && j.TopPick == nil {
		return j, errors.New("say whether the brand is the single strongest recommendation")
	}
	if j.Verdict != grade.Recommended && j.TopPick != nil && *j.TopPick {
		return j, errors.New("only a recommended brand can be the top pick")
	}
	if j.Verdict == grade.Absent {
		if j.Tone != "" {
			return j, errors.New("an absent brand has no tone")
		}
		if j.AbsenceReason != "not-named" && j.AbsenceReason != "echo" && j.AbsenceReason != "namesake" {
			return j, errors.New("choose why the brand is absent")
		}
		if j.AbsenceReason == "not-named" && !j.FullAnswer {
			return j, errors.New("confirm review of the complete answer when there is no qualifying reference")
		}
	} else {
		if j.Tone != grade.Positive && j.Tone != grade.Neutral && j.Tone != grade.Mixed && j.Tone != grade.Negative {
			return j, errors.New("choose positive, neutral, mixed or negative tone")
		}
		if j.AbsenceReason != "" {
			return j, errors.New("a named brand has no absence reason")
		}
	}
	if len(j.Evidence) == 0 && (j.Verdict != grade.Absent || j.AbsenceReason != "not-named") {
		return j, errors.New("select an exact supporting passage from the saved answer")
	}
	for i, quote := range j.Evidence {
		span := grade.Quote(answer, quote)
		if span == "" {
			return j, fmt.Errorf("evidence passage %d is not a short contiguous span of the saved answer", i+1)
		}
		j.Evidence[i] = span
	}
	return j, nil
}

type Review struct {
	Revision  int      `json:"revision"`
	State     string   `json:"state"`
	Judgment  Judgment `json:"judgment"`
	Actor     string   `json:"actor"`
	UpdatedAt string   `json:"updated_at"`
}

type Case struct {
	ID            string             `json:"id"`
	Record        grade.GoldenRecord `json:"-"` // imported labels are never an unreviewed API response
	Review        Review             `json:"review"`
	FirstReviewed bool               `json:"first_reviewed"`
}

func (w *Workbench) Case(ctx context.Context, id string) (Case, error) {
	c := Case{ID: id, Review: Review{State: "unreviewed"}}
	var data string
	if err := w.DB.QueryRowContext(ctx, `SELECT record FROM evaluation_cases WHERE id=?`, id).Scan(&data); err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(data), &c.Record); err != nil {
		return c, err
	}
	err := w.DB.QueryRowContext(ctx, `SELECT revision,state,judgment,actor,updated_at FROM evaluation_reviews WHERE case_id=?`, id).Scan(&c.Review.Revision, &c.Review.State, &data, &c.Review.Actor, &c.Review.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	if err == nil {
		if err := json.Unmarshal([]byte(data), &c.Review.Judgment); err != nil {
			return c, err
		}
	}
	if err := w.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM evaluation_events WHERE case_id=? AND kind='judgment')`, id).Scan(&c.FirstReviewed); err != nil {
		return c, err
	}
	return c, nil
}

func (w *Workbench) Cases(ctx context.Context) ([]Case, error) {
	rows, err := w.DB.QueryContext(ctx, `SELECT id FROM evaluation_cases ORDER BY created_at,id`)
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
	out := []Case{}
	for _, id := range ids {
		c, err := w.Case(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (w *Workbench) Save(ctx context.Context, id string, revision int, kind string, j Judgment, actor, rubric string) (Review, error) {
	return w.save(ctx, id, revision, kind, j, actor, rubric, nil)
}
func (w *Workbench) save(ctx context.Context, id string, revision int, kind string, j Judgment, actor, rubric string, resolution *Resolution) (Review, error) {
	if strings.TrimSpace(actor) == "" || len(actor) > 200 {
		return Review{}, errors.New("an explicit human review actor is required")
	}
	if rubric != Rubric() {
		return Review{}, errors.New("the review rules changed; reload and review this decision under the current rubric")
	}
	c, err := w.Case(ctx, id)
	if err != nil {
		return Review{}, err
	}
	if resolution != nil {
		if c.Review.State != "reviewed" {
			return Review{}, errors.New("save your human judgment before resolving checks")
		}
		if resolution.Action == "keep" {
			j = c.Review.Judgment
		}
	}
	j, err = validate(j, c.Record.Answer, kind)
	if err != nil {
		return c.Review, err
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return c.Review, err
	}
	defer tx.Rollback()
	var current int
	err = tx.QueryRowContext(ctx, `SELECT revision FROM evaluation_reviews WHERE case_id=?`, id).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c.Review, err
	}
	if current != revision {
		return c.Review, ErrConflict
	}
	if resolution != nil {
		vs, err := checkViews(ctx, tx, c)
		if err != nil {
			return c.Review, err
		}
		ids := pendingIDs(vs)
		submitted := append([]string(nil), resolution.Checks...)
		sort.Strings(submitted)
		if len(ids) == 0 || strings.Join(ids, "\n") != strings.Join(submitted, "\n") {
			return c.Review, ErrConflict
		}
	}
	state := kind
	if kind == "judgment" {
		state = "reviewed"
	}
	from := ReviewState(c.Review.State)
	if c.Review.Revision == 0 {
		from = ""
	}
	ReviewTransitions.Check("evaluation_reviews", from, ReviewState(state))
	b, _ := json.Marshal(j)
	next := Review{Revision: current + 1, State: state, Judgment: j, Actor: actor, UpdatedAt: store.Now()}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO evaluation_rubrics VALUES (?,?,?)`, rubric, grade.System, next.UpdatedAt); err != nil {
		return c.Review, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_reviews VALUES (?,?,?,?,?,?) ON CONFLICT(case_id) DO UPDATE SET revision=excluded.revision,state=excluded.state,judgment=excluded.judgment,actor=excluded.actor,updated_at=excluded.updated_at`, id, next.Revision, state, string(b), actor, next.UpdatedAt); err != nil {
		return c.Review, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_events VALUES (?,?,?,?,?,?,?)`, id, next.Revision, kind, string(b), actor, rubric, next.UpdatedAt); err != nil {
		return c.Review, err
	}
	if resolution != nil {
		for _, check := range resolution.Checks {
			if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_resolutions VALUES (?,?,?,?,?,?,?)`, check, next.Revision, id, resolution.Action, resolution.Explanation, actor, next.UpdatedAt); err != nil {
				return c.Review, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return c.Review, err
	}
	return next, nil
}

type Event struct {
	Review
	Kind   string `json:"kind"`
	Rubric string `json:"rubric"`
}

func (w *Workbench) History(ctx context.Context, id string) ([]Event, error) {
	rows, err := w.DB.QueryContext(ctx, `SELECT revision,kind,judgment,actor,rubric,created_at FROM evaluation_events WHERE case_id=? ORDER BY revision`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var data string
		if err := rows.Scan(&e.Revision, &e.Kind, &data, &e.Actor, &e.Rubric, &e.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &e.Judgment); err != nil {
			return nil, err
		}
		e.State = e.Kind
		if e.Kind == "judgment" {
			e.State = "reviewed"
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
