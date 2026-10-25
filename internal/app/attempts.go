package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/store"
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
	State     AttemptState `json:"state"`
	Note      string       `json:"note,omitempty"`
	By        string       `json:"by,omitempty"`
	CreatedAt string       `json:"created_at"`
	UpdatedAt string       `json:"updated_at"`
	Outcome   Outcome      `json:"outcome"`
}

// FixStatus is where a fix stands given the attempts recorded on it (newest first): its newest
// attempt's state, or open when it has none or the newest was withdrawn. FixAttempts.Status also
// counts work on the same page that the fix list didn't know about.
func FixStatus(attempts []Attempt) string {
	if len(attempts) == 0 || attempts[0].State == Withdrawn {
		return Open
	}
	return string(attempts[0].State)
}

// Outcome is what the runs of the set since an attempt show for the prompts its fix meant to
// flip, against the fix's own run and the runs just before it. Answers are pooled per prompt on
// each side and the change is tested prompt by prompt (Diff), so a prompt drifting by chance
// isn't read as the fix working. It's measured, not judged: a page can take weeks to be crawled,
// and recall moves only with new models.
type Outcome struct {
	// The newest run it's measured on; empty while the set hasn't run since.
	RunID string `json:"run_id,omitempty"`
	// Each target prompt's answers, pooled over the runs before and the runs after.
	Prompts []PromptOutcome `json:"prompts,omitempty"`
	// Whether an answer's searches, fetches or citations included the fix's target, before and
	// after. Supporting evidence: a page turning up doesn't by itself mean answers changed.
	TargetBefore bool `json:"target_seen_before"`
	TargetAfter  bool `json:"target_seen_after"`
	// The target's host, and whether any answer cited a page on it before and after: whether the
	// host entered or left the set of hosts cited.
	TargetHost      string `json:"target_host,omitempty"`
	HostCitedBefore bool   `json:"host_cited_before"`
	HostCitedAfter  bool   `json:"host_cited_after"`
	// Runs pooled on each side, and days from the attempt to the newest run after it.
	BeforeRuns int     `json:"before_runs,omitempty"`
	AfterRuns  int     `json:"after_runs,omitempty"`
	Days       float64 `json:"days,omitempty"`
	// The set has run since, but only with answers asked differently (another answer prompt or
	// search policy), which no re-grade can change: nothing compares and nothing ever will.
	Incomparable bool `json:"incomparable,omitempty"`
	// The set has only run since under another grading method. These runs (the fix's, the ones
	// pooled before it, and runs since on methods older than the newest) would compare once
	// re-graded under today's grader; a re-grade of the set reaching RegradeSinceDays back covers
	// them all.
	RegradeRuns      []string `json:"regrade_runs,omitempty"`
	RegradeSinceDays int      `json:"regrade_since_days,omitempty"`
}

type PromptOutcome struct {
	PromptID string `json:"prompt_id"`
	Before   Tally  `json:"before"`
	After    Tally  `json:"after"`
}

// Tally counts a prompt's graded answers, in one run or pooled over several.
type Tally struct {
	N           int `json:"n"`
	Named       int `json:"named"`
	Recommended int `json:"recommended"`
	// Answers to it asked as a choice (not an info prompt): what recommended is out of.
	Choice int `json:"choice"`
}

// When an outcome is read. Runs after the attempt count once it's at least OutcomeMinDays old
// (a page has to be crawled before answers can change; Gauge waits 14 days) and its target
// prompts average OutcomeMinAnswers graded answers since. Before is the fix's run and the
// same-method runs up to OutcomeBeforeDays before it, at most OutcomeBeforeRuns in all; after is
// every same-method run since the attempt within OutcomeAfterDays of the newest.
const (
	OutcomeMinDays    = 7
	OutcomeMinAnswers = 4
	OutcomeBeforeDays = 28
	OutcomeBeforeRuns = 5
	OutcomeAfterDays  = 28
)

// Outcome statuses.
const (
	OutcomePending      = "pending"
	OutcomeTooEarly     = "too early"
	OutcomeMoved        = "moved"
	OutcomeWorse        = "worse"
	OutcomeMixed        = "mixed"
	OutcomeNoChange     = "no detectable change"
	OutcomeIncomparable = "not comparable"
	OutcomeNeedsRegrade = "needs regrade"
)

// Changes is the stratified change over the target prompts in named (out of every answer) and
// recommended (out of answers to choice prompts).
func (o Outcome) Changes() (named, recommended Diff) {
	var nb, na, rb, ra []Hits
	for _, p := range o.Prompts {
		nb, na = append(nb, Hits{p.Before.Named, p.Before.N}), append(na, Hits{p.After.Named, p.After.N})
		rb, ra = append(rb, Hits{p.Before.Recommended, p.Before.Choice}), append(ra, Hits{p.After.Recommended, p.After.Choice})
	}
	return PairedDiff(nb, na), PairedDiff(rb, ra)
}

// Status is the outcome in a word: pending until the set runs again; needs regrade while it has
// only run under another grading method (RegradeRuns says which runs to re-grade); too early
// until the attempt is OutcomeMinDays old with OutcomeMinAnswers answers a prompt since; then
// moved when named or recommended rose beyond its 95% margin, worse when one fell beyond it,
// mixed when one did each, and otherwise no detectable change.
func (o Outcome) Status() string {
	if len(o.RegradeRuns) > 0 {
		return OutcomeNeedsRegrade
	}
	if o.Incomparable {
		return OutcomeIncomparable
	}
	if o.RunID == "" {
		return OutcomePending
	}
	measured, after := 0, 0
	for _, p := range o.Prompts {
		if p.After.N > 0 && p.Before.N > 0 {
			measured++
			after += p.After.N
		}
	}
	if o.Days < OutcomeMinDays || measured == 0 || after < OutcomeMinAnswers*measured {
		return OutcomeTooEarly
	}
	named, rec := o.Changes()
	up := named.Sign() > 0 || rec.Sign() > 0
	down := named.Sign() < 0 || rec.Sign() < 0
	switch {
	case up && down:
		return OutcomeMixed
	case up:
		return OutcomeMoved
	case down:
		return OutcomeWorse
	}
	return OutcomeNoChange
}

// Summary is the outcome in one line, for the page and for the analyst: the status, the change in
// named and recommended with their margins, how much data it rests on, then each prompt.
func (o Outcome) Summary() string {
	switch o.Status() {
	case OutcomePending:
		return "pending: the set hasn't run since"
	case OutcomeIncomparable:
		return "not comparable: the set has only run since with answers asked differently, which no re-grade can change"
	case OutcomeNeedsRegrade:
		return fmt.Sprintf("needs regrade: the set has only run since under another grading method; re-grade %s (%s) to measure it",
			plural(len(o.RegradeRuns), "run"), strings.Join(o.RegradeRuns, ", "))
	}
	named, rec := o.Changes()
	head := o.Status()
	if head == OutcomeNoChange {
		head += fmt.Sprintf(" (±%.0f pts)", 100*named.Margin)
	}
	before, after := 0, 0
	for _, p := range o.Prompts {
		before, after = before+p.Before.N, after+p.After.N
	}
	parts := []string{fmt.Sprintf("named %s, recommended %s over %s; %d answers in %s before, %d in %s over %s after",
		named, rec, plural(named.Prompts, "prompt"), before, plural(o.BeforeRuns, "run"), after, plural(o.AfterRuns, "run"), plural(int(math.Round(o.Days)), "day"))}
	for _, p := range o.Prompts {
		parts = append(parts, fmt.Sprintf("%s named %d/%d→%d/%d, recommended %d/%d→%d/%d", p.PromptID,
			p.Before.Named, p.Before.N, p.After.Named, p.After.N, p.Before.Recommended, p.Before.Choice, p.After.Recommended, p.After.Choice))
	}
	seen := map[bool]string{true: "seen", false: "not seen"}
	target := fmt.Sprintf("target %s→%s", seen[o.TargetBefore], seen[o.TargetAfter])
	if o.TargetAfter && !o.TargetBefore {
		target += " (newly surfaced)"
	}
	parts = append(parts, target)
	if o.TargetHost != "" {
		cited := map[bool]string{true: "cited", false: "not cited"}
		host := fmt.Sprintf("host %s %s→%s", o.TargetHost, cited[o.HostCitedBefore], cited[o.HostCitedAfter])
		switch {
		case o.HostCitedAfter && !o.HostCitedBefore:
			host += " (entered the cited set)"
		case o.HostCitedBefore && !o.HostCitedAfter:
			host += " (left the cited set)"
		}
		parts = append(parts, host)
	}
	return head + ": " + strings.Join(parts, "; ")
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// MarshalJSON adds what the outcome works out to: status, summary and the two changes.
func (o Outcome) MarshalJSON() ([]byte, error) {
	type plain Outcome
	named, rec := o.Changes()
	return json.Marshal(struct {
		plain
		Status      string `json:"status"`
		Summary     string `json:"summary"`
		Named       Diff   `json:"named"`
		Recommended Diff   `json:"recommended"`
	}{plain(o), o.Status(), o.Summary(), named, rec})
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

// The longest link and note an attempt stores, in bytes. A commit or pull request URL is far
// shorter, and a note is a line or two; anything longer is a mistake or abuse, refused rather than
// cut, so what's stored is what was sent.
const (
	MaxAttemptURL  = 2048
	MaxAttemptNote = 4096
)

// MarkFix records work submitted for fix rank of run runID: the commit or pull request at link, in
// state (proposed when empty).
func (a *App) MarkFix(ctx context.Context, runID string, rank int, link, state, note, by string) (Attempt, error) {
	link, note = strings.TrimSpace(link), strings.TrimSpace(note)
	if len(link) > MaxAttemptURL {
		return Attempt{}, fmt.Errorf("the link is %d bytes; at most %d", len(link), MaxAttemptURL)
	}
	if len(note) > MaxAttemptNote {
		return Attempt{}, fmt.Errorf("the note is %d bytes; at most %d", len(note), MaxAttemptNote)
	}
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Attempt{}, fmt.Errorf("a link to the commit or pull request is required, as an http(s) URL")
	}
	st := AttemptState(state)
	if st == "" {
		st = Proposed
	}
	if !validState(st) {
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
		at := Attempt{RunID: runID, SetID: r.SetID, Rank: rank, Fix: f, URL: u.String(), State: st, Note: note, By: by, CreatedAt: now, UpdatedAt: now}
		AttemptTransitions.Check("fix_attempts", newAttempt, at.State)
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
		out = append(out, analysis.Tried{Title: at.Fix.Title, Target: at.Fix.Target, URL: at.URL, State: string(at.State), Outcome: at.Outcome.Summary()})
		if len(out) == 10 {
			break
		}
	}
	return out
}

// SetAttemptState moves an attempt on: proposed to accepted when the pull request merges, or to
// withdrawn when it's closed, which reopens its fix.
func (a *App) SetAttemptState(ctx context.Context, id int64, state string) error {
	st := AttemptState(state)
	if !validState(st) {
		return fmt.Errorf("state is proposed, accepted or withdrawn, not %q", state)
	}
	var from AttemptState
	if err := a.DB.QueryRowContext(ctx, `SELECT state FROM fix_attempts WHERE id=?`, id).Scan(&from); err == nil {
		AttemptTransitions.Check("fix_attempts", from, st)
	}
	res, err := a.DB.ExecContext(ctx, `UPDATE fix_attempts SET state=?, updated_at=? WHERE id=?`, st, store.Now(), id)
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
	return a.attempts(ctx, setID, "")
}

// attempts is Attempts made at or after since (any time, if empty), with their outcomes.
func (a *App) attempts(ctx context.Context, setID, since string) ([]Attempt, error) {
	q, args := `SELECT id,run_id,set_id,fix_rank,fix,url,state,note,marked_by,created_at,updated_at FROM fix_attempts WHERE created_at>=?`, []any{since}
	if setID != "" {
		q += ` AND set_id=?`
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
			return string(at.State)
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

// outcome measures an attempt: the fix's run and the same-method runs just before it, against the
// same-method runs of the set since the attempt, pooled per target prompt (Outcome). Counts come
// from the results' verdict column and only answers that might name the target are decoded, so
// listing attempts stays cheap however many runs it covers. When the set has only run since under
// another method, the attempt needs a re-grade (RegradeRuns) if the runs were asked alike, and is
// incomparable for good if not.
func (a *App) outcome(ctx context.Context, at Attempt) Outcome {
	var o Outcome
	r, err := a.Runs.Get(ctx, at.RunID)
	if err != nil {
		return o
	}
	fixAt := r.CreatedAt
	// Timestamps are to the second, so a run started in the attempt's second counts as after it,
	// and so does one started in the fix run's own second.
	since := at.CreatedAt
	if fixAt > since {
		since = fixAt
	}
	rows, err := a.DB.QueryContext(ctx, `SELECT id, created_at, method FROM runs WHERE set_id=? AND status='done' AND created_at>=? AND id<>? ORDER BY created_at DESC, rowid DESC`, at.SetID, since, at.RunID)
	if err != nil {
		return o
	}
	var after, older []string // older: runs since on a method that is neither the fix run's nor the newest's
	var newest time.Time
	ran := false
	newestID, newestMethod := "", ""
	for rows.Next() {
		var id, created, method string
		rows.Scan(&id, &created, &method)
		ran = true
		if newestID == "" {
			newestID, newestMethod = id, method
		}
		if method != r.Method {
			if method != newestMethod {
				older = append(older, id)
			}
			continue
		}
		t, _ := time.Parse(time.RFC3339, created)
		if len(after) == 0 {
			newest = t
		} else if newest.Sub(t) > OutcomeAfterDays*24*time.Hour {
			break
		}
		after = append(after, id)
	}
	rows.Close()
	if len(after) == 0 {
		// Runs since, but none graded as the fix's run was. Re-grading joins them if they were
		// asked alike (a re-grade changes grades, never answers); nothing can if they weren't.
		if ran && a.askedAlike(ctx, r, newestID) {
			before, oldest := a.beforeRuns(ctx, at, r)
			o.RegradeRuns = append(before, older...)
			o.RegradeSinceDays = 1
			if t, err := time.Parse(time.RFC3339, oldest); err == nil {
				o.RegradeSinceDays = int(math.Max(1, math.Ceil(time.Since(t).Hours()/24)))
			}
		} else {
			o.Incomparable = ran
		}
		return o
	}
	o.RunID = after[0]
	if t, err := time.Parse(time.RFC3339, at.CreatedAt); err == nil {
		o.Days = math.Max(0, newest.Sub(t).Hours()/24)
	}
	before, _ := a.beforeRuns(ctx, at, r)
	o.BeforeRuns, o.AfterRuns = len(before), len(after)

	ids := at.Fix.PromptIDs
	if len(ids) == 0 { // a fix stored before prompt ids: map its numbers through the run's set
		for _, n := range at.Fix.Prompts {
			if n >= 1 && n <= len(r.Snapshot.Set.Prompts) {
				ids = append(ids, r.Snapshot.Set.Prompts[n-1].ID)
			}
		}
	}
	// Each prompt as the fix's run asked it: answers to it worded differently aren't pooled.
	sha, info := map[string]string{}, map[string]bool{}
	for _, p := range r.Snapshot.Set.Prompts {
		sha[p.ID], info[p.ID] = p.SHA(), p.Info()
	}
	pb, pa := a.tallies(ctx, before, sha, info), a.tallies(ctx, after, sha, info)
	for _, id := range ids {
		o.Prompts = append(o.Prompts, PromptOutcome{PromptID: id, Before: pb[id], After: pa[id]})
	}
	key := TargetKey(at.Fix.Target)
	o.TargetBefore, o.TargetAfter = a.surfacedIn(ctx, before, key), a.surfacedIn(ctx, after, key)
	if host, _, _ := strings.Cut(key, "/"); strings.Contains(host, ".") {
		o.TargetHost = host
		o.HostCitedBefore, o.HostCitedAfter = a.hostCitedIn(ctx, before, host), a.hostCitedIn(ctx, after, host)
	}
	return o
}

// beforeRuns is the fix's run and the same-method runs up to OutcomeBeforeDays before it, at most
// OutcomeBeforeRuns in all, newest first, with the oldest one's date.
func (a *App) beforeRuns(ctx context.Context, at Attempt, r *run.Run) (ids []string, oldest string) {
	ids, oldest = []string{at.RunID}, r.CreatedAt
	floor := ""
	if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
		floor = t.AddDate(0, 0, -OutcomeBeforeDays).Format(time.RFC3339)
	}
	rows, err := a.DB.QueryContext(ctx, `SELECT id, created_at FROM runs WHERE set_id=? AND status='done' AND method=? AND created_at<? AND created_at>=? ORDER BY created_at DESC LIMIT ?`,
		at.SetID, r.Method, r.CreatedAt, floor, OutcomeBeforeRuns-1)
	if err != nil {
		return ids, oldest
	}
	defer rows.Close()
	for rows.Next() {
		var id, created string
		if rows.Scan(&id, &created) == nil {
			ids, oldest = append(ids, id), created
		}
	}
	return ids, oldest
}

// askedAlike reports whether run id asked its answers as r did (the answer prompt and search
// policy): the part of a method a re-grade can't change, so what decides whether one could ever
// join the two.
func (a *App) askedAlike(ctx context.Context, r *run.Run, id string) bool {
	x, err := a.Runs.Get(ctx, id)
	return err == nil && x.Snapshot.AnswerPrompt == r.Snapshot.AnswerPrompt && x.Snapshot.SearchPolicy == r.Snapshot.SearchPolicy
}

// hostCitedIn reports whether any answer in runs cited a page on host or a subdomain of it. Only
// answers whose JSON mentions the host are decoded.
func (a *App) hostCitedIn(ctx context.Context, runs []string, host string) bool {
	in, args := inRuns(runs)
	rows, err := a.DB.QueryContext(ctx, `SELECT answer FROM results WHERE `+in+` AND status='done' AND answer LIKE ?`, append(args, "%"+host+"%")...)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var js string
		var ans answer.Answer
		if rows.Scan(&js) != nil || json.Unmarshal([]byte(js), &ans) != nil {
			continue
		}
		if citesHost(&ans, host) {
			return true
		}
	}
	return false
}

// citesHost reports whether the answer cited a page on host or a subdomain of it.
func citesHost(ans *answer.Answer, host string) bool {
	for _, l := range ans.Citations {
		if h := strings.ToLower(report.Host(report.NormalizeURL(l.URL))); h == host || strings.HasSuffix(h, "."+host) {
			return true
		}
	}
	return false
}

// inRuns is "run_id IN (?,…)" for ids, with its arguments.
func inRuns(ids []string) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return "run_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", args
}

// tallies counts the graded answers to each prompt over runs, keeping only answers to the prompt
// worded as sha has it (any wording for a prompt sha doesn't list). Recommended is out of the
// answers to prompts that ask for a choice.
func (a *App) tallies(ctx context.Context, runs []string, sha map[string]string, info map[string]bool) map[string]Tally {
	out := map[string]Tally{}
	in, args := inRuns(runs)
	rows, err := a.DB.QueryContext(ctx, `SELECT prompt_id, prompt_sha, COUNT(*), SUM(verdict<>'absent'), SUM(verdict='recommended') FROM results
		WHERE `+in+` AND status='done' AND grade IS NOT NULL AND verdict IS NOT NULL AND prompt_id<>'' GROUP BY prompt_id, prompt_sha`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, s string
		var n, named, rec int
		if rows.Scan(&id, &s, &n, &named, &rec) != nil {
			continue
		}
		if want, ok := sha[id]; ok && want != s {
			continue
		}
		t := out[id]
		t.N, t.Named, t.Recommended = t.N+n, t.Named+named, t.Recommended+rec
		if info[id] {
			t.Recommended -= rec // an info prompt asks for no choice, so recommended doesn't apply
		} else {
			t.Choice += n
		}
		out[id] = t
	}
	return out
}

// surfacedIn reports whether any answer in runs reached the target (surfaced). Only answers whose
// JSON mentions the key are decoded.
func (a *App) surfacedIn(ctx context.Context, runs []string, key string) bool {
	if key == "" {
		return false
	}
	in, args := inRuns(runs)
	rows, err := a.DB.QueryContext(ctx, `SELECT answer FROM results WHERE `+in+` AND status='done' AND answer LIKE ?`, append(args, "%"+key+"%")...)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var js string
		var ans answer.Answer
		if rows.Scan(&js) != nil || json.Unmarshal([]byte(js), &ans) != nil {
			continue
		}
		if surfaced([]run.Result{{Answer: &ans}}, key) {
			return true
		}
	}
	return false
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
