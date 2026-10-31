package evaluation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"

	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

const GroupingMethod = "question-and-answer-bigram-v1"

var ErrNoReviewCases = errors.New("no cases match this session; held-out cases remain available for manual review from the queue")

type Group struct {
	ID        string `json:"id"`
	Partition string `json:"partition"`
}

type Grouping struct {
	Method string           `json:"method"`
	Cases  map[string]Group `json:"cases"`
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func bigrams(s string) map[string]bool {
	ws := words(s)
	bs := map[string]bool{}
	for i := 1; i < len(ws); i++ {
		bs[ws[i-1]+" "+ws[i]] = true
	}
	return bs
}

type groupEvidence struct {
	question, answer string
	pairs            map[string]bool
}

func prepareGroupEvidence(r grade.GoldenRecord) groupEvidence {
	return groupEvidence{strings.Join(words(r.Prompt), " "), r.Answer, bigrams(r.Answer)}
}
func relatedEvidence(a, b groupEvidence) bool {
	if a.question == b.question || a.answer == b.answer {
		return true
	}
	if len(a.pairs) < 10 || len(b.pairs) < 10 {
		return false
	}
	shared := 0
	for pair := range a.pairs {
		if b.pairs[pair] {
			shared++
		}
	}
	union := len(a.pairs) + len(b.pairs) - shared
	return shared > 0 && float64(shared)/float64(union) >= .9
}

// Related questions stay together. High-overlap answer word pairs are an
// additional conservative grouping rule, not a claim of semantic deduplication.
func grouping(cs []Case) Grouping {
	sort.Slice(cs, func(i, j int) bool { return cs[i].ID < cs[j].ID })
	parent := make([]int, len(cs))
	evidence := make([]groupEvidence, len(cs))
	for i := range cs {
		parent[i] = i
		evidence[i] = prepareGroupEvidence(cs[i].Record)
	}
	var root func(int) int
	root = func(i int) int {
		if parent[i] != i {
			parent[i] = root(parent[i])
		}
		return parent[i]
	}
	for i := range cs {
		for j := 0; j < i; j++ {
			if relatedEvidence(evidence[i], evidence[j]) {
				a, b := root(i), root(j)
				if a > b {
					a, b = b, a
				}
				parent[b] = a
			}
		}
	}
	g := Grouping{Method: GroupingMethod, Cases: map[string]Group{}}
	for i, c := range cs {
		id := cs[root(i)].ID
		partition := "development"
		// Assignment is repeatable and independent of any proposed/approved label.
		if digest([]byte(id))[0] < '4' {
			partition = "held-out"
		}
		g.Cases[c.ID] = Group{ID: id, Partition: partition}
	}
	return g
}

func (w *Workbench) Imports(ctx context.Context) ([]Import, error) {
	rows, err := w.DB.QueryContext(ctx, `SELECT id FROM evaluation_imports ORDER BY created_at DESC,id`)
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
	out := []Import{}
	for _, id := range ids {
		a, err := w.Import(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (w *Workbench) ImportCases(ctx context.Context, id string) ([]Case, error) {
	a, err := w.Import(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []Case{}
	seen := map[string]bool{}
	for _, row := range a.Rows {
		if row.CaseID == "" || seen[row.CaseID] {
			continue
		}
		c, err := w.Case(ctx, row.CaseID)
		if err != nil {
			return nil, err
		}
		seen[row.CaseID] = true
		out = append(out, c)
	}
	return out, nil
}

func (w *Workbench) Grouping(ctx context.Context, importID string) (Grouping, error) {
	g, err := w.StoredGrouping(ctx, importID)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return g, err
	}
	cs, err := w.ImportCases(ctx, importID)
	if err != nil {
		return Grouping{}, err
	}
	g = grouping(cs)
	b, _ := json.Marshal(g)
	if _, err := w.DB.ExecContext(ctx, `INSERT OR IGNORE INTO evaluation_groupings VALUES (?,?,?,?)`, importID, GroupingMethod, string(b), store.Now()); err != nil {
		return Grouping{}, err
	}
	return w.StoredGrouping(ctx, importID)
}

func (w *Workbench) StoredGrouping(ctx context.Context, importID string) (Grouping, error) {
	var raw string
	err := w.DB.QueryRowContext(ctx, `SELECT plan FROM evaluation_groupings WHERE import_id=?`, importID).Scan(&raw)
	if err == nil {
		var g Grouping
		err := json.Unmarshal([]byte(raw), &g)
		return g, err
	}
	return Grouping{}, err
}

type Session struct {
	ID       string   `json:"id"`
	ImportID string   `json:"import_id"`
	Actor    string   `json:"actor"`
	CaseIDs  []string `json:"case_ids"`
	Cursor   int      `json:"cursor"`
	State    string   `json:"state"`
}

func (w *Workbench) Session(ctx context.Context, id, actor string) (Session, error) {
	var s Session
	var ids string
	err := w.DB.QueryRowContext(ctx, `SELECT id,import_id,actor,case_ids,cursor,state FROM evaluation_sessions WHERE id=? AND actor=?`, id, actor).Scan(&s.ID, &s.ImportID, &s.Actor, &ids, &s.Cursor, &s.State)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal([]byte(ids), &s.CaseIDs)
	return s, err
}

func (w *Workbench) Resume(ctx context.Context, importID, actor string) (*Session, error) {
	var id string
	err := w.DB.QueryRowContext(ctx, `SELECT id FROM evaluation_sessions WHERE import_id=? AND actor=? AND state!='done' ORDER BY updated_at DESC,id LIMIT 1`, importID, actor).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s, err := w.Session(ctx, id, actor)
	return &s, err
}

func (w *Workbench) Start(ctx context.Context, importID, actor, filter string) (Session, error) {
	if actor == "" || filter != "first" && filter != "attention" {
		return Session{}, errors.New("choose first review or cases needing a decision")
	}
	if s, err := w.Resume(ctx, importID, actor); err != nil {
		return Session{}, err
	} else if s != nil {
		return *s, nil
	}
	cs, err := w.ImportCases(ctx, importID)
	if err != nil {
		return Session{}, err
	}
	g, err := w.Grouping(ctx, importID)
	if err != nil {
		return Session{}, err
	}
	// Round-robin prompt sets without consulting model labels or exposing a
	// selection rationale. The first pilot uses development cases only.
	bySet := map[string][]Case{}
	var sets []string
	reviewed := 0
	for _, c := range cs {
		if c.FirstReviewed {
			reviewed++
		}
	}
	for _, c := range cs {
		if filter == "first" && (c.FirstReviewed || c.Review.State == "unresolved") || filter == "attention" && c.Review.State != "unresolved" && c.Review.State != "draft" {
			continue
		}
		if filter == "first" && reviewed < 10 && g.Cases[c.ID].Partition != "development" {
			continue
		}
		if _, ok := bySet[c.Record.Set]; !ok {
			sets = append(sets, c.Record.Set)
		}
		bySet[c.Record.Set] = append(bySet[c.Record.Set], c)
	}
	sort.Strings(sets)
	ids := []string{}
	for round := 0; len(ids) < 10; round++ {
		added := false
		for _, set := range sets {
			if round < len(bySet[set]) && len(ids) < 10 {
				ids = append(ids, bySet[set][round].ID)
				added = true
			}
		}
		if !added {
			break
		}
	}
	if len(ids) == 0 {
		return Session{}, ErrNoReviewCases
	}
	s := Session{ID: "es-" + rand.Text(), ImportID: importID, Actor: actor, CaseIDs: ids, State: "active"}
	b, _ := json.Marshal(ids)
	now := store.Now()
	_, err = w.DB.ExecContext(ctx, `INSERT INTO evaluation_sessions VALUES (?,?,?,?,?,?,?,?)`, s.ID, importID, actor, string(b), 0, s.State, now, now)
	return s, err
}

// Advance is a compare-and-swap on the stable queue. Judgment persistence is
// acknowledged first; a failed advance cannot discard that saved decision.
func (w *Workbench) Advance(ctx context.Context, id, actor, caseID string, cursor int, end bool) error {
	s, err := w.Session(ctx, id, actor)
	if err != nil {
		return err
	}
	if s.Cursor != cursor || cursor < 0 || cursor >= len(s.CaseIDs) || s.CaseIDs[cursor] != caseID {
		return ErrConflict
	}
	next, state := cursor+1, "active"
	if end {
		next, state = cursor, "paused"
	} else if next == len(s.CaseIDs) {
		state = "done"
	}
	r, err := w.DB.ExecContext(ctx, `UPDATE evaluation_sessions SET cursor=?,state=?,updated_at=? WHERE id=? AND actor=? AND cursor=?`, next, state, store.Now(), id, actor, cursor)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return ErrConflict
	}
	return nil
}
