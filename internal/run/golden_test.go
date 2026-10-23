package run

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// goldenDB is a database snapshot with one finished run of set s and a graded answer in every
// stratum, plus the shapes an export has to cope with: a grade from before other_product and
// tone, a row with no grade JSON but a verdict column, an answer naming an alias only another
// run's snapshot still lists, and a run of a set today's config doesn't have.
func goldenDB(t *testing.T) (string, *config.Config, []sets.Set) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := &config.Config{Brand: config.Brand{Name: "Cheap Seats", Aliases: []string{"cheapseats.dev"}, About: "a tracker"}, Grader: config.Model{Model: "g"}}
	today := sets.Set{ID: "s", Prompts: []sets.Prompt{{ID: "p1", Text: "Which tool?"}, {ID: "p2", Text: "How do I do it?", Intent: sets.Info}}, Competitors: []sets.Competitor{{Name: "Gauge", Aliases: []string{"withgauge"}}}}
	then := Snapshot{Set: today, Brand: config.Brand{Name: "Cheap Seats", Aliases: []string{"cheapseats.dev"}}, Grader: "g", GraderPrompt: "old"}
	then.Set.Competitors = []sets.Competitor{{Name: "Peec AI"}} // the run's competitors, not today's
	snap, _ := json.Marshal(then)
	then.Brand.Aliases = append(then.Brand.Aliases, "oldname", "oldname.example") // an earlier run knew the old names
	older, _ := json.Marshal(then)
	ctx := context.Background()
	for _, r := range []struct{ id, set, snap string }{{"r1", "s", string(snap)}, {"r2", "gone", string(older)}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO runs (id, set_id, set_version, name, trigger, status, snapshot, created_at, method) VALUES (?,?,?,?,?,?,?,?,?)`,
			r.id, r.set, "v", "n", "manual", "done", r.snap, "2026-10-01T00:00:00Z", "m1"); err != nil {
			t.Fatal(err)
		}
	}
	type row struct {
		run, prompt, pid, answer string
		grade                    any // nil for no grade JSON
		verdict                  string
		top                      int
	}
	brand := "Cheap Seats"
	rows := []row{
		{"r1", "Which tool?", "p1", "Cheap Seats is the one to pick.", grade.Grade{Verdict: grade.Recommended, TopPick: &brand, Tone: grade.Positive, Evidence: "Cheap Seats is the one to pick."}, "recommended", 1},
		{"r1", "Which tool?", "p1", "Gauge first, then Cheap Seats as a strong second.", grade.Grade{Verdict: grade.Recommended, Tone: grade.Positive}, "recommended", 0},
		{"r1", "Which tool?", "p1", "Options include Gauge, Peec AI and Cheap Seats.", map[string]any{"verdict": "mentioned", "position": 3}, "mentioned", 0}, // an old shape
		{"r1", "Which tool?", "p1", "Cheap Seats sells WhatsApp agents; pick Gauge.", grade.Grade{Verdict: grade.Absent, OtherProduct: true}, "absent", 0},
		{"r1", "How do I do it?", "p2", "Measure it with Gauge.", nil, "absent", 0}, // no grade JSON, a verdict column
		{"r1", "How do I do it?", "p2", "Try oldname, it is the same thing.", grade.Grade{Verdict: grade.Mentioned}, "mentioned", 0},
		{"r1", "Which tool?", "p1", "Cheap Seats is the one to pick.", grade.Grade{Verdict: grade.Recommended}, "recommended", 0}, // the same answer again
		{"r2", "Which tool?", "p1", "Cheap Seats, in the set that's gone.", grade.Grade{Verdict: grade.Recommended}, "recommended", 0},
	}
	for i, r := range rows {
		ans, _ := json.Marshal(map[string]string{"text": r.answer})
		var gj any
		if r.grade != nil {
			b, _ := json.Marshal(r.grade)
			gj = string(b)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO results (run_id, prompt_idx, prompt, prompt_id, answerer, model, track, sample, status, answer, grade, verdict, top_pick) VALUES (?,?,?,?,?,?,?,?,'done',?,?,?,?)`,
			r.run, 0, r.prompt, r.pid, "a", "m", "search", i, string(ans), gj, r.verdict, r.top); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "cheapseats.db"), cfg, []sets.Set{today}
}

func TestGoldenExportSamplesEveryStratumAndKeepsLabels(t *testing.T) {
	dbPath, cfg, all := goldenDB(t)
	out := t.TempDir()
	ctx := context.Background()
	sum, err := ExportGolden(ctx, dbPath, cfg, all, GoldenExport{N: 80, Dir: out})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"top pick": 1, "recommended": 1, "mentioned": 1, "other product": 1, "absent": 1}
	for st, n := range want {
		if sum.Added["s"][st] != n {
			t.Errorf("%s: %d records, want %d (%v)", st, sum.Added["s"][st], n, sum.Added)
		}
	}
	if sum.Retired != 1 || len(sum.UnknownSets) != 1 || sum.UnknownSets[0] != "gone" || sum.Kept != 0 {
		t.Errorf("retired=%d unknown=%v kept=%d", sum.Retired, sum.UnknownSets, sum.Kept)
	}
	records, err := grade.ReadGolden(out)
	if err != nil || len(records) != 5 {
		t.Fatalf("%d records %v", len(records), err)
	}
	byID := map[string]grade.GoldenRecord{}
	for _, r := range records {
		byID[r.ID] = r
		if r.LabeledBy != "claude" || r.Set != "s" || r.Brand.About != "a tracker" || len(r.Competitors) != 1 || r.Competitors[0].Name != "Gauge" {
			t.Errorf("today's brand and competitors, labeled by claude: %+v", r)
		}
		if strings.Contains(strings.ToLower(r.Answer), "oldname") {
			t.Errorf("an answer naming a retired alias was exported: %q", r.Answer)
		}
	}
	top := byID[grade.GoldenID("Cheap Seats is the one to pick.")]
	if !top.Expected.TopPick || top.Expected.Tone != grade.Positive || top.Evidence != "Cheap Seats is the one to pick." || top.PromptID != "p1" || top.Source == nil || top.Source.Run != "r1" || top.Source.Method != "m1" {
		t.Errorf("%+v", top)
	}
	old := byID[grade.GoldenID("Options include Gauge, Peec AI and Cheap Seats.")]
	if old.Expected.Verdict != grade.Mentioned || old.Expected.Tone != "" || old.Expected.TopPick {
		t.Errorf("an old-shape grade: %+v", old.Expected)
	}
	col := byID[grade.GoldenID("Measure it with Gauge.")]
	if col.Expected.Verdict != grade.Absent || col.Intent != sets.Info {
		t.Errorf("verdict from the column, intent from today's set: %+v", col)
	}
	if other := byID[grade.GoldenID("Cheap Seats sells WhatsApp agents; pick Gauge.")]; !other.Expected.OtherProduct || other.Expected.Verdict != grade.Absent {
		t.Errorf("%+v", other.Expected)
	}

	// A human flips a label; the next export keeps it and adds nothing it already has.
	for i := range records {
		if records[i].ID == top.ID {
			records[i].Expected.Verdict, records[i].Expected.TopPick, records[i].LabeledBy, records[i].Note = grade.Mentioned, false, "human", "one of three, not the pick"
		}
	}
	if err := grade.WriteGolden(filepath.Join(out, "s.jsonl"), records); err != nil {
		t.Fatal(err)
	}
	sum, err = ExportGolden(ctx, dbPath, cfg, all, GoldenExport{N: 80, Dir: out})
	if err != nil || len(sum.Added) != 0 || sum.Kept != 5 {
		t.Fatalf("%+v %v", sum, err)
	}
	again, _ := grade.ReadGolden(out)
	n := 0
	for _, r := range again {
		if r.ID == top.ID {
			n++
			if r.LabeledBy != "human" || r.Expected.Verdict != grade.Mentioned || r.Note == "" {
				t.Errorf("the human's label was lost: %+v", r)
			}
		}
	}
	if n != 1 || len(again) != 5 {
		t.Errorf("%d records, %d copies of the flipped one", len(again), n)
	}

	// -n caps the records, and -set keeps to one set.
	sum, err = ExportGolden(ctx, dbPath, cfg, all, GoldenExport{N: 2, Dir: t.TempDir(), Set: "s"})
	if err != nil || sum.Added["s"]["top pick"] != 1 || sum.Added["s"]["recommended"] != 1 || len(sum.UnknownSets) != 0 {
		t.Fatalf("%+v %v", sum, err)
	}
}
