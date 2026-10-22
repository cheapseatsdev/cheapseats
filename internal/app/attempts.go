package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/metcalfc/popcorn/internal/analysis"
	"github.com/metcalfc/popcorn/internal/grade"
	"github.com/metcalfc/popcorn/internal/run"
	"github.com/metcalfc/popcorn/internal/store"
)

// Attempt is work submitted for a fix: the commit or pull request, proposed, then accepted or
// withdrawn. It settles the one fix it was recorded on. Fix lists are rebuilt every run, so the fix
// is kept as it was; a later fix with the same target stays open, as a retry of the same idea, and
// shows the attempts before it with their outcomes.
type Attempt struct {
	ID        int64        `json:"id"`
	RunID     string       `json:"run_id"`
	SetID     string       `json:"set_id"`
	Rank      int          `json:"rank"`
	Fix       analysis.Fix `json:"fix"`
	URL       string       `json:"url"`
	State     string       `json:"state"`
	Note      string       `json:"note,omitempty"`
	By        string       `json:"by,omitempty"`
	CreatedAt string       `json:"created_at"`
	UpdatedAt string       `json:"updated_at"`
	Outcome   Outcome      `json:"outcome"`
}

// Attempt states, and Open, the status of a fix with no attempt that's still standing.
const (
	Proposed  = "proposed"
	Accepted  = "accepted"
	Withdrawn = "withdrawn"
	Open      = "open"
)

func validState(s string) bool { return s == Proposed || s == Accepted || s == Withdrawn }

// FixStatus is where a fix stands given the attempts recorded on it (newest first): its newest
// attempt's state, or open when it has none or the newest was withdrawn. FixAttempts.Status also
// counts work on the same page that the fix list didn't know about.
func FixStatus(attempts []Attempt) string {
	if len(attempts) == 0 || attempts[0].State == Withdrawn {
		return Open
	}
	return attempts[0].State
}

// Outcome is what the newest run of the set since an attempt shows for the prompts its fix meant
// to flip, against the run the fix came from. It's measured, not judged: a page can take weeks to
// be crawled, and recall moves only with new models.
type Outcome struct {
	// The run it's measured on; empty while the set hasn't run since.
	RunID   string          `json:"run_id,omitempty"`
	Prompts []PromptOutcome `json:"prompts,omitempty"`
	// Whether an answer's searches, fetches or citations included the fix's target, before and
	// after.
	TargetBefore bool `json:"target_seen_before"`
	TargetAfter  bool `json:"target_seen_after"`
}

type PromptOutcome struct {
	PromptID string `json:"prompt_id"`
	Before   Tally  `json:"before"`
	After    Tally  `json:"after"`
}

// Tally counts a prompt's graded answers in one run.
type Tally struct {
	N           int `json:"n"`
	Named       int `json:"named"`
	Recommended int `json:"recommended"`
}

// Status is the outcome in a word: pending until the set runs again, then moved when any of the
// prompts is named or recommended more often, or the target newly turns up, else unmoved.
func (o Outcome) Status() string {
	if o.RunID == "" {
		return "pending"
	}
	if o.TargetAfter && !o.TargetBefore {
		return "moved"
	}
	for _, p := range o.Prompts {
		if p.After.N > 0 && p.Before.N > 0 && (p.After.Named*p.Before.N > p.Before.Named*p.After.N || p.After.Recommended*p.Before.N > p.Before.Recommended*p.After.N) {
			return "moved"
		}
	}
	return "unmoved"
}

// Summary is the outcome in one line, for the page and for the analyst.
func (o Outcome) Summary() string {
	if o.RunID == "" {
		return "pending: the set hasn't run since"
	}
	var parts []string
	for _, p := range o.Prompts {
		parts = append(parts, fmt.Sprintf("%s named %d/%d→%d/%d, recommended %d/%d→%d/%d", p.PromptID,
			p.Before.Named, p.Before.N, p.After.Named, p.After.N, p.Before.Recommended, p.Before.N, p.After.Recommended, p.After.N))
	}
	seen := map[bool]string{true: "seen", false: "not seen"}
	parts = append(parts, fmt.Sprintf("target %s→%s", seen[o.TargetBefore], seen[o.TargetAfter]))
	return o.Status() + ": " + strings.Join(parts, "; ")
}

// TargetKey is a fix target reduced to what two fixes for the same page share: lowercase, no
// scheme, www., query, fragment or trailing slash, and only its first word ("github.com/x/y
// (README.md) and the homepage" is github.com/x/y).
func TargetKey(target string) string {
	t := strings.ToLower(strings.TrimSpace(target))
	if i := strings.IndexAny(t, " \t\n("); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimPrefix(strings.TrimPrefix(t, "https://"), "http://")
	t = strings.TrimPrefix(t, "www.")
	if i := strings.IndexAny(t, "?#"); i >= 0 {
		t = t[:i]
	}
	return strings.TrimRight(t, "/")
}

// MarkFix records work submitted for fix rank of run runID: the commit or pull request at link, in
// state (proposed when empty).
func (a *App) MarkFix(ctx context.Context, runID string, rank int, link, state, note, by string) (Attempt, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Attempt{}, fmt.Errorf("a link to the commit or pull request is required, as an http(s) URL")
	}
	if state == "" {
		state = Proposed
	}
	if !validState(state) {
		return Attempt{}, fmt.Errorf("state is proposed, accepted or withdrawn, not %q", state)
	}
	r, err := a.Runs.Get(ctx, runID)
	if err != nil {
		return Attempt{}, fmt.Errorf("no run %q", runID)
	}
	fixes, _, _ := a.Fixes(ctx, runID)
	for _, f := range fixes {
		if f.Rank != rank {
			continue
		}
		now := store.Now()
		at := Attempt{RunID: runID, SetID: r.SetID, Rank: rank, Fix: f, URL: u.String(), State: state, Note: strings.TrimSpace(note), By: by, CreatedAt: now, UpdatedAt: now}
		res, err := a.DB.ExecContext(ctx, `INSERT INTO fix_attempts(run_id,set_id,fix_rank,fix,target_key,url,state,note,marked_by,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			at.RunID, at.SetID, at.Rank, mustJSON(f), TargetKey(f.Target), at.URL, at.State, at.Note, at.By, at.CreatedAt, at.UpdatedAt)
		if err != nil {
			return Attempt{}, err
		}
		at.ID, _ = res.LastInsertId()
		at.Outcome = a.outcome(ctx, at)
		return at, nil
	}
	return Attempt{}, fmt.Errorf("run %s has no fix %d", runID, rank)
}

// triedFor is the set's ten newest standing attempts, for the analyst. Withdrawn ones are left out:
// nothing shipped, so there's nothing to follow up on.
func (a *App) triedFor(ctx context.Context, setID string) []analysis.Tried {
	all, _ := a.Attempts(ctx, setID)
	var out []analysis.Tried
	for _, at := range all {
		if at.State == Withdrawn {
			continue
		}
		out = append(out, analysis.Tried{Title: at.Fix.Title, Target: at.Fix.Target, URL: at.URL, State: at.State, Outcome: at.Outcome.Summary()})
		if len(out) == 10 {
			break
		}
	}
	return out
}

// SetAttemptState moves an attempt on: proposed to accepted when the pull request merges, or to
// withdrawn when it's closed, which reopens its fix.
func (a *App) SetAttemptState(ctx context.Context, id int64, state string) error {
	if !validState(state) {
		return fmt.Errorf("state is proposed, accepted or withdrawn, not %q", state)
	}
	res, err := a.DB.ExecContext(ctx, `UPDATE fix_attempts SET state=?, updated_at=? WHERE id=?`, state, store.Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no attempt %d", id)
	}
	return nil
}

// Attempts lists attempts, newest first, with their outcomes: one set's, or every set's when
// setID is empty.
func (a *App) Attempts(ctx context.Context, setID string) ([]Attempt, error) {
	q, args := `SELECT id,run_id,set_id,fix_rank,fix,url,state,note,marked_by,created_at,updated_at FROM fix_attempts`, []any{}
	if setID != "" {
		q += ` WHERE set_id=?`
		args = append(args, setID)
	}
	rows, err := a.DB.QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		var at Attempt
		var fix string
		if err := rows.Scan(&at.ID, &at.RunID, &at.SetID, &at.Rank, &fix, &at.URL, &at.State, &at.Note, &at.By, &at.CreatedAt, &at.UpdatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(fix), &at.Fix)
		out = append(out, at)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Outcome = a.outcome(ctx, out[i])
	}
	return out, nil
}

// FixAttempts is the attempts that bear on fixes: those recorded on a fix (by run and rank), and
// those on any fix for the same target (by TargetKey), newest first.
type FixAttempts struct {
	byFix    map[string][]Attempt
	byTarget map[string][]Attempt
}

func fixKey(runID string, rank int) string { return fmt.Sprintf("%s#%d", runID, rank) }

func (a *App) FixAttempts(ctx context.Context) FixAttempts {
	all, _ := a.Attempts(ctx, "")
	fa := FixAttempts{byFix: map[string][]Attempt{}, byTarget: map[string][]Attempt{}}
	for _, at := range all {
		k := fixKey(at.RunID, at.Rank)
		fa.byFix[k] = append(fa.byFix[k], at)
		t := TargetKey(at.Fix.Target)
		fa.byTarget[t] = append(fa.byTarget[t], at)
	}
	return fa
}

// On is the attempts recorded on fix rank of run runID, which settle its status.
func (fa FixAttempts) On(runID string, rank int) []Attempt { return fa.byFix[fixKey(runID, rank)] }

// Status is where fix f of run runID stands. The attempts on it decide first. Failing those, an
// attempt on the same target made after the fix list was built (fixesAt) covers it too: the list
// didn't know about that work. A fix built after an attempt is a retry the analyst chose knowing
// what was tried, so it's open.
func (fa FixAttempts) Status(runID string, f analysis.Fix, fixesAt string) string {
	if s := FixStatus(fa.On(runID, f.Rank)); s != Open {
		return s
	}
	for _, at := range fa.Earlier(runID, f) {
		if at.State != Withdrawn && at.CreatedAt >= fixesAt {
			return at.State
		}
	}
	return Open
}

// Earlier is the attempts on other fixes for the same target: what was tried before for the idea.
func (fa FixAttempts) Earlier(runID string, f analysis.Fix) []Attempt {
	var out []Attempt
	for _, at := range fa.byTarget[TargetKey(f.Target)] {
		if at.RunID != runID || at.Rank != f.Rank {
			out = append(out, at)
		}
	}
	return out
}

// outcome measures an attempt on the newest finished run of its set made after it.
func (a *App) outcome(ctx context.Context, at Attempt) Outcome {
	var o Outcome
	var later sql.NullString
	// Timestamps are to the second, so a run started in the attempt's second counts as after it.
	a.DB.QueryRowContext(ctx, `SELECT id FROM runs WHERE set_id=? AND status='done' AND created_at>=? AND id<>? ORDER BY created_at DESC LIMIT 1`, at.SetID, at.CreatedAt, at.RunID).Scan(&later)
	if !later.Valid {
		return o
	}
	before, err := a.Runs.Results(ctx, at.RunID)
	if err != nil {
		return o
	}
	after, err := a.Runs.Results(ctx, later.String)
	if err != nil {
		return o
	}
	o.RunID = later.String
	ids := at.Fix.PromptIDs
	if len(ids) == 0 { // a fix stored before prompt ids: map its numbers through the run's set
		if r, err := a.Runs.Get(ctx, at.RunID); err == nil {
			for _, n := range at.Fix.Prompts {
				if n >= 1 && n <= len(r.Snapshot.Set.Prompts) {
					ids = append(ids, r.Snapshot.Set.Prompts[n-1].ID)
				}
			}
		}
	}
	for _, id := range ids {
		o.Prompts = append(o.Prompts, PromptOutcome{PromptID: id, Before: tally(before, id), After: tally(after, id)})
	}
	key := TargetKey(at.Fix.Target)
	o.TargetBefore, o.TargetAfter = surfaced(before, key), surfaced(after, key)
	return o
}

func tally(results []run.Result, promptID string) Tally {
	var t Tally
	for _, r := range results {
		if r.PromptID != promptID || r.Status != "done" || r.Grade == nil {
			continue
		}
		t.N++
		if r.Grade.Verdict != grade.Absent {
			t.Named++
		}
		if r.Grade.Verdict == grade.Recommended {
			t.Recommended++
		}
	}
	return t
}

// surfaced reports whether any answer's searches, fetches or citations reached the target.
func surfaced(results []run.Result, key string) bool {
	if key == "" {
		return false
	}
	hit := func(u string) bool { return strings.HasPrefix(TargetKey(u), key) }
	for _, r := range results {
		if r.Answer == nil {
			continue
		}
		for _, q := range r.Answer.Searches {
			for _, l := range q.Results {
				if hit(l.URL) {
					return true
				}
			}
		}
		for _, u := range r.Answer.Fetches {
			if hit(u) {
				return true
			}
		}
		for _, l := range r.Answer.Citations {
			if hit(l.URL) {
				return true
			}
		}
	}
	return false
}
