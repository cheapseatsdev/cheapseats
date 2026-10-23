package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// GoldenExport is what cheapseats golden export does: it samples a database snapshot's graded
// answers, spread over sets and over the current grader's verdicts, and writes them to Dir as
// golden records (grade.GoldenRecord) with that verdict pre-filled for a human to check. Records
// already in Dir are kept as they are; an answer already there isn't written again.
//
// The brand and competitors in each record are today's, from the config and its sets, as a
// re-grade would use them, not the ones the run was graded with. A set the config no longer has
// can't be exported. An answer or question that names an alias the brand has since retired (a
// name any run in the snapshot knew it by that today's config doesn't) is left out: today's grader
// wouldn't know the name, so the record couldn't be labeled, and assistants go on using an old
// name long after the runs stop listing it.
type GoldenExport struct {
	N   int    // how many records to add in all
	Set string // only this set; "" for every set
	Dir string // where the .jsonl files are, one per set
}

// GoldenSummary is what an export did.
type GoldenSummary struct {
	// New records per set and stratum (the verdict, with "top pick" and "other product" apart).
	Added map[string]map[string]int
	Kept  int // records already in Dir
	// Graded answers in the snapshot by stratum, after the skips: what there was to sample from.
	Candidates map[string]int
	// Answers left out: ones naming a retired alias, and ones of sets the config doesn't have.
	Retired     int
	UnknownSets []string
}

// GoldenStrata are the strata an export balances, in the order it draws from them.
var GoldenStrata = []string{"top pick", "recommended", "mentioned", "other product", "absent"}

// goldenStratum is the stratum of an expected grade.
func goldenStratum(e grade.GoldenExpected) string {
	switch {
	case e.TopPick:
		return "top pick"
	case e.Verdict == grade.Recommended:
		return "recommended"
	case e.Verdict == grade.Mentioned:
		return "mentioned"
	case e.OtherProduct:
		return "other product"
	}
	return "absent"
}

// ExportGolden samples the snapshot at dbPath (opened read-only, never migrated) into golden
// records. all are today's sets with their shared competitors folded in (sets.Share).
func ExportGolden(ctx context.Context, dbPath string, cfg *config.Config, all []sets.Set, opt GoldenExport) (GoldenSummary, error) {
	sum := GoldenSummary{Added: map[string]map[string]int{}, Candidates: map[string]int{}}
	existing, err := grade.ReadGolden(opt.Dir)
	if err != nil {
		return sum, err
	}
	sum.Kept = len(existing)
	have := map[string]bool{}
	byFile := map[string][]grade.GoldenRecord{}
	for _, r := range existing {
		have[r.ID] = true
		byFile[r.Set] = append(byFile[r.Set], r)
	}
	bySet := map[string]sets.Set{}
	for _, s := range all {
		bySet[s.ID] = s
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return sum, err
	}
	defer db.Close()
	var schema int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return sum, fmt.Errorf("reading %s: %w", dbPath, err)
	}
	// results.prompt_id came with schema 2 and runs.method with 7; older snapshots have neither.
	promptID, method := "''", "''"
	if schema >= 2 {
		promptID = "s.prompt_id"
	}
	if schema >= 7 {
		method = "r.method"
	}
	retired, err := goldenRetired(ctx, db, cfg.Brand)
	if err != nil {
		return sum, err
	}
	q := `SELECT r.id, r.set_id, r.snapshot, ` + method + `, s.id, s.prompt, ` + promptID + `, s.answerer, s.track, s.answer, s.grade, COALESCE(s.verdict, ''), COALESCE(s.top_pick, 0)
		FROM results s JOIN runs r ON r.id = s.run_id
		WHERE r.status = 'done' AND s.status = 'done' AND s.answer IS NOT NULL AND (s.grade IS NOT NULL OR s.verdict IS NOT NULL)`
	args := []any{}
	if opt.Set != "" {
		q += ` AND r.set_id = ?`
		args = append(args, opt.Set)
	}
	rows, err := db.QueryContext(ctx, q+` ORDER BY r.created_at DESC, s.id`, args...)
	if err != nil {
		return sum, err
	}
	defer rows.Close()

	runs := map[string]*Snapshot{}
	unknown := map[string]bool{}
	candidates := map[string]map[string][]grade.GoldenRecord{} // set -> stratum -> records
	seen := map[string]bool{}
	for rows.Next() {
		var runID, setID, snapJS, meth, prompt, pid, answerer, track, ansJS, verdictCol string
		var gradeJS sql.NullString
		var resultID int64
		var topCol int
		if err := rows.Scan(&runID, &setID, &snapJS, &meth, &resultID, &prompt, &pid, &answerer, &track, &ansJS, &gradeJS, &verdictCol, &topCol); err != nil {
			return sum, err
		}
		set, ok := bySet[setID]
		if !ok {
			unknown[setID] = true
			continue
		}
		snap := runs[runID]
		if snap == nil {
			snap = &Snapshot{}
			json.Unmarshal([]byte(snapJS), snap) // lenient: an older snapshot still gives what it has
			runs[runID] = snap
		}
		var a struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(ansJS), &a) != nil || strings.TrimSpace(a.Text) == "" {
			continue
		}
		if namesAny(prompt, retired) || namesAny(a.Text, retired) {
			sum.Retired++
			continue
		}
		id := grade.GoldenID(a.Text)
		if have[id] || seen[id] {
			continue
		}
		exp, ok := goldenExpected(gradeJS, verdictCol, topCol, cfg.Brand.Name)
		if !ok {
			continue
		}
		seen[id] = true
		p := goldenPrompt(set, snap.Set, pid, prompt)
		rec := grade.GoldenRecord{
			ID: id, Set: setID, PromptID: p.ID, Prompt: prompt, Intent: p.Intent,
			Brand: grade.GoldenBrandOf(cfg.Brand), Competitors: set.Competitors,
			Answer: a.Text, Expected: exp, LabeledBy: "claude", Note: "",
			Source: &grade.GoldenSource{Run: runID, Result: resultID, Answerer: answerer, Track: track, Grader: snap.Grader, Method: meth},
		}
		if gradeJS.Valid {
			var g grade.Grade
			if json.Unmarshal([]byte(gradeJS.String), &g) == nil {
				rec.Evidence = grade.Quote(a.Text, g.Evidence)
			}
		}
		st := goldenStratum(exp)
		if candidates[setID] == nil {
			candidates[setID] = map[string][]grade.GoldenRecord{}
		}
		candidates[setID][st] = append(candidates[setID][st], rec)
		sum.Candidates[st]++
	}
	if err := rows.Err(); err != nil {
		return sum, err
	}
	for s := range unknown {
		sum.UnknownSets = append(sum.UnknownSets, s)
	}
	sort.Strings(sum.UnknownSets)

	// Draw one record at a time from each set in turn, and within a set from each stratum in
	// turn, so every verdict of every set is there before any is there twice. The order within
	// a stratum is shuffled with a fixed seed, so the same snapshot exports the same records.
	rng := rand.New(rand.NewPCG(7, 11))
	setIDs := make([]string, 0, len(candidates))
	for id, strata := range candidates {
		setIDs = append(setIDs, id)
		for _, rs := range strata {
			rng.Shuffle(len(rs), func(i, j int) { rs[i], rs[j] = rs[j], rs[i] })
		}
	}
	sort.Strings(setIDs)
	added := 0
	for added < opt.N {
		drew := false
		for _, id := range setIDs {
			for _, st := range GoldenStrata {
				rs := candidates[id][st]
				if len(rs) == 0 || added >= opt.N {
					continue
				}
				candidates[id][st] = rs[1:]
				byFile[id] = append(byFile[id], rs[0])
				if sum.Added[id] == nil {
					sum.Added[id] = map[string]int{}
				}
				sum.Added[id][st]++
				added++
				drew = true
			}
		}
		if !drew {
			break
		}
	}
	for id := range sum.Added {
		if err := grade.WriteGolden(filepath.Join(opt.Dir, id+".jsonl"), byFile[id]); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// goldenExpected reads a stored grade leniently: grades from before other_product and tone lack
// them, and a grade that doesn't parse still has the results row's verdict and top_pick columns.
func goldenExpected(gradeJS sql.NullString, verdictCol string, topCol int, brand string) (grade.GoldenExpected, bool) {
	var g grade.Grade
	if gradeJS.Valid {
		json.Unmarshal([]byte(gradeJS.String), &g)
	}
	v := grade.Verdict(strings.ToLower(strings.TrimSpace(string(g.Verdict))))
	if v == "" {
		v = grade.Verdict(strings.ToLower(strings.TrimSpace(verdictCol)))
	}
	if v != grade.Recommended && v != grade.Mentioned && v != grade.Absent {
		return grade.GoldenExpected{}, false
	}
	g.Verdict = v
	e := grade.GoldenExpectedOf(g, brand)
	if topCol != 0 && v == grade.Recommended {
		e.TopPick = true
	}
	// The same consistency grading keeps (grade.normalize), for a grade stored before a rule.
	if e.OtherProduct {
		e.Verdict, e.TopPick = grade.Absent, false
	}
	if e.TopPick {
		e.Verdict = grade.Recommended
	}
	if e.Verdict == grade.Absent || !slices.Contains(grade.Tones, e.Tone) {
		e.Tone = ""
	}
	return e, true
}

// goldenPrompt is the prompt as today's set has it, by id then by wording, else as the run's
// snapshot had it; what matters is its intent.
func goldenPrompt(today, then sets.Set, id, text string) sets.Prompt {
	for _, s := range []sets.Set{today, then} {
		for _, p := range s.Prompts {
			if (id != "" && p.ID == id) || p.Text == text {
				return p
			}
		}
	}
	return sets.Prompt{ID: id, Text: text}
}

// goldenRetired is every name the brand went by in any of the snapshot's runs that today's brand
// doesn't, lowercased.
func goldenRetired(ctx context.Context, db *sql.DB, now config.Brand) ([]string, error) {
	keep := map[string]bool{}
	for _, n := range append([]string{now.Name}, now.Aliases...) {
		keep[strings.ToLower(strings.TrimSpace(n))] = true
	}
	rows, err := db.QueryContext(ctx, `SELECT snapshot FROM runs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var js string
		if err := rows.Scan(&js); err != nil {
			return nil, err
		}
		var snap Snapshot
		json.Unmarshal([]byte(js), &snap)
		for _, n := range append([]string{snap.Brand.Name}, snap.Brand.Aliases...) {
			n = strings.ToLower(strings.TrimSpace(n))
			if n != "" && !keep[n] && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out, rows.Err()
}

func namesAny(text string, names []string) bool {
	text = strings.ToLower(text)
	for _, n := range names {
		if strings.Contains(text, n) {
			return true
		}
	}
	return false
}
