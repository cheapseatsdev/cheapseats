package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

const DatasetContract = "human-core-labels-v1"

type Approval struct {
	ImportID string `json:"import_id"`
	Name     string `json:"name"`
	// Every included member pins the revision displayed in the approval preview.
	Members map[string]int `json:"members"`
	// Keys are a case ID, or "line:N" for a blocked import row.
	Exclusions map[string]string `json:"exclusions"`
}

type Member struct {
	CaseID      string             `json:"case_id"`
	Revision    int                `json:"revision"`
	Record      grade.GoldenRecord `json:"record"`
	Judgment    Judgment           `json:"judgment"`
	Group       Group              `json:"group"`
	Actor       string             `json:"actor"`
	Provenance  string             `json:"provenance"`
	Resolutions []ResolutionRecord `json:"resolutions,omitempty"`
}

type Dataset struct {
	ID             string            `json:"id"`
	Contract       string            `json:"contract"`
	Name           string            `json:"name"`
	ImportID       string            `json:"import_id"`
	Rubric         string            `json:"rubric"`
	Rules          string            `json:"rules"`
	GroupingMethod string            `json:"grouping_method"`
	Members        []Member          `json:"members"`
	Exclusions     map[string]string `json:"exclusions"`
	TotalCases     int               `json:"total_cases"`
	TotalRows      int               `json:"total_rows"`
	Blocked        int               `json:"blocked"`
	Duplicate      int               `json:"duplicates"`
	Actor          string            `json:"actor"`
	CreatedAt      string            `json:"created_at"`
}

func (w *Workbench) Approve(ctx context.Context, p Approval, actor string) (Dataset, error) {
	if strings.TrimSpace(actor) == "" || len(actor) > 200 || strings.TrimSpace(p.Name) == "" || len(p.Name) > 160 {
		return Dataset{}, errors.New("name this version and identify its approving reviewer")
	}
	a, err := w.Import(ctx, p.ImportID)
	if err != nil {
		return Dataset{}, err
	}
	cs, err := w.ImportCases(ctx, p.ImportID)
	if err != nil {
		return Dataset{}, err
	}
	g, err := w.Grouping(ctx, p.ImportID)
	if err != nil {
		return Dataset{}, err
	}
	d := Dataset{Contract: DatasetContract, Name: strings.TrimSpace(p.Name), ImportID: p.ImportID, Rubric: Rubric(), Rules: grade.System, GroupingMethod: g.Method, Exclusions: map[string]string{}, Members: []Member{}, TotalCases: a.Cases, TotalRows: len(a.Rows), Blocked: a.Blocked, Duplicate: a.Duplicate}
	accounted := map[string]bool{}
	exclude := func(key string) error {
		reason := strings.TrimSpace(p.Exclusions[key])
		if reason == "" || len(reason) > 1000 {
			return fmt.Errorf("%s: give an explicit exclusion reason", key)
		}
		d.Exclusions[key] = reason
		accounted[key] = true
		return nil
	}
	seenGolden := map[string]bool{}
	for _, c := range cs {
		revision, included := p.Members[c.ID]
		if !included {
			if err := exclude(c.ID); err != nil {
				return Dataset{}, err
			}
			continue
		}
		if strings.TrimSpace(p.Exclusions[c.ID]) != "" {
			return Dataset{}, fmt.Errorf("%s: choose membership or exclusion, not both", c.ID)
		}
		accounted[c.ID] = true
		if revision != c.Review.Revision {
			return Dataset{}, ErrConflict
		}
		if c.Review.State != "reviewed" {
			return Dataset{}, fmt.Errorf("%s: only a committed, resolved judgment can be included", c.ID)
		}
		j, err := validate(c.Review.Judgment, c.Record.Answer, "judgment")
		if err != nil {
			return Dataset{}, err
		}
		var rubric string
		if err := w.DB.QueryRowContext(ctx, `SELECT rubric FROM evaluation_events WHERE case_id=? AND revision=? AND kind='judgment'`, c.ID, revision).Scan(&rubric); err != nil {
			return Dataset{}, err
		}
		if rubric != d.Rubric {
			return Dataset{}, fmt.Errorf("%s: the decision uses another rubric; review it under the current rules", c.ID)
		}
		// The existing golden reader rejects answer-only ID collisions. Refuse to
		// silently lose a context or relabel IDs when exporting its older contract.
		if seenGolden[c.Record.ID] {
			return Dataset{}, errors.New("the legacy JSONL contract cannot represent the same answer in two contexts; exclude one explicitly or use separate dataset versions")
		}
		seenGolden[c.Record.ID] = true
		record := c.Record
		record.Expected = j.Expected()
		record.LabeledBy = "human"
		record.Note = j.Note
		record.Evidence = ""
		if len(j.Evidence) > 0 {
			record.Evidence = j.Evidence[0]
		}
		m := Member{CaseID: c.ID, Revision: revision, Record: record, Judgment: j, Group: g.Cases[c.ID], Actor: c.Review.Actor, Provenance: "single human review"}
		resolutions, err := w.ResolutionHistory(ctx, c.ID)
		if err != nil {
			return Dataset{}, err
		}
		for _, r := range resolutions {
			if r.Revision == revision {
				m.Resolutions = append(m.Resolutions, r)
			}
		}
		d.Members = append(d.Members, m)
	}
	for _, r := range a.Rows {
		if r.Error != "" {
			if err := exclude(fmt.Sprintf("line:%d", r.Line)); err != nil {
				return Dataset{}, err
			}
		}
	}
	for key := range p.Members {
		if !accounted[key] || strings.HasPrefix(key, "line:") {
			return Dataset{}, fmt.Errorf("unknown member %s", key)
		}
	}
	for key := range p.Exclusions {
		if !accounted[key] {
			return Dataset{}, fmt.Errorf("unknown exclusion %s", key)
		}
	}
	if len(d.Members) == 0 {
		return Dataset{}, errors.New("an approved dataset needs at least one human-reviewed member")
	}
	sort.Slice(d.Members, func(i, j int) bool { return d.Members[i].CaseID < d.Members[j].CaseID })
	content, _ := json.Marshal(d)
	d.ID = "ed-" + digest(content)
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return Dataset{}, err
	}
	defer tx.Rollback()
	// Check all included revisions under the write lock, so a concurrent edit
	// cannot be approved behind the preview. Frozen snapshots never follow edits.
	for _, m := range d.Members {
		var rev int
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT revision,state FROM evaluation_reviews WHERE case_id=?`, m.CaseID).Scan(&rev, &state); err != nil {
			return Dataset{}, err
		}
		if rev != m.Revision || state != "reviewed" {
			return Dataset{}, ErrConflict
		}
		c := Case{ID: m.CaseID, Record: m.Record, Review: Review{Revision: m.Revision, Judgment: m.Judgment}}
		views, err := checkViews(ctx, tx, c)
		if err != nil {
			return Dataset{}, err
		}
		if len(pendingIDs(views)) > 0 {
			return Dataset{}, fmt.Errorf("%s: resolve independent-check differences before approval", m.CaseID)
		}
		for _, v := range views {
			if !v.Stale && v.State == "running" {
				return Dataset{}, fmt.Errorf("%s: an independent check is still running", m.CaseID)
			}
		}
	}
	d.Actor, d.CreatedAt = actor, store.Now()
	content, _ = json.Marshal(d)
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO evaluation_datasets VALUES (?,?,?,?,?)`, d.ID, d.ImportID, string(content), actor, d.CreatedAt); err != nil {
		return Dataset{}, err
	}
	if err := tx.Commit(); err != nil {
		return Dataset{}, err
	}
	return w.Dataset(ctx, d.ID)
}

func (w *Workbench) Dataset(ctx context.Context, id string) (Dataset, error) {
	var raw string
	var d Dataset
	if err := w.DB.QueryRowContext(ctx, `SELECT snapshot FROM evaluation_datasets WHERE id=?`, id).Scan(&raw); err != nil {
		return d, err
	}
	err := json.Unmarshal([]byte(raw), &d)
	return d, err
}

func (w *Workbench) Datasets(ctx context.Context, importID string) ([]Dataset, error) {
	rows, err := w.DB.QueryContext(ctx, `SELECT snapshot FROM evaluation_datasets WHERE import_id=? ORDER BY created_at DESC,id`, importID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ds := []Dataset{}
	for rows.Next() {
		var raw string
		var d Dataset
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, err
		}
		ds = append(ds, d)
	}
	return ds, rows.Err()
}

func (d Dataset) JSONL() ([]byte, error) {
	var b strings.Builder
	ms := append([]Member(nil), d.Members...)
	sort.Slice(ms, func(i, j int) bool { return ms[i].Record.ID < ms[j].Record.ID })
	for _, m := range ms {
		if err := m.Record.Validate(); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(m.Record)
		if err != nil {
			return nil, err
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}
