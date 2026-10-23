package grade_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// The golden set: labeled answers in testdata/golden/*.jsonl that the grader is measured against
// (docs/grading.md). TestGolden grades them all with the real grader, so it runs only when asked.

var (
	goldenThreshold = flag.Float64("golden.threshold", 0.925, "TestGolden fails when verdict, top_pick or other_product agree with the labels on a smaller share of records")
	goldenOnly      = flag.String("golden.set", "", "TestGolden grades only this set's records")
)

const goldenDir = "testdata/golden"

// goldenConfig is the config TestGolden grades with: CHEAPSEATS_CONFIG, else the sample config.
// Only its grader and pricing are used; each record carries its own brand and competitors.
func goldenConfig(t *testing.T) *config.Config {
	t.Helper()
	path := os.Getenv("CHEAPSEATS_CONFIG")
	if path == "" {
		path = filepath.Join("..", "..", "examples", "cheapseats-config", "cheapseats.toml")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestGolden grades every golden record under the current grade.System with the configured grader,
// over the network, and fails when a scored field agrees with the labels on fewer than
// -golden.threshold of them. It is skipped unless CHEAPSEATS_GOLDEN=1; with it and no key it
// fails, so a CI run without the secret can't pass by doing nothing. Run with -v for the report.
func TestGolden(t *testing.T) {
	if os.Getenv("CHEAPSEATS_GOLDEN") != "1" {
		t.Skip("grades the golden set with the real grader; set CHEAPSEATS_GOLDEN=1 to run it")
	}
	cfg := goldenConfig(t)
	records, err := grade.ReadGolden(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	if *goldenOnly != "" {
		var keep []grade.GoldenRecord
		for _, r := range records {
			if r.Set == *goldenOnly {
				keep = append(keep, r)
			}
		}
		records = keep
	}
	if len(records) == 0 {
		t.Fatalf("no golden records in %s; export some with cheapseats golden export", goldenDir)
	}
	api := cfg.Grader.Via() != config.ClaudeCode
	if api && os.Getenv("ANTHROPIC_API_KEY") == "" && config.AnthropicBaseURL() == "" {
		t.Fatalf("CHEAPSEATS_GOLDEN=1 but neither ANTHROPIC_API_KEY nor %s is set: the grader %s can't be called", config.AnthropicBaseURLVar, cfg.Grader.Ref())
	}

	// What this run is attributable to: the grader, its prompt, and the method today's config
	// gives a run (run.Method: how answers are asked and graded, including the brand).
	sum := sha256.Sum256([]byte(grade.System))
	t.Logf("grader %s, grader prompt %s, method %s", cfg.Grader.Ref(), hex.EncodeToString(sum[:])[:12], run.Method(run.NewSnapshot(cfg, sets.Set{})))
	est := 0.0
	for _, r := range records {
		in := grade.Input(r.Config(cfg), r.SetFor(), r.Prompt, r.Answer)
		est += cfg.Cost(cfg.Grader.Model, config.Usage{Input: int64(len(grade.System)+len(in))/4 + 400, Output: 1500})
	}
	switch {
	case !api:
		t.Logf("%d records on the claude CLI: $0, on the subscription", len(records))
	case est == 0:
		t.Logf("%d records; %s has no [pricing] in the config, so no cost estimate", len(records), cfg.Grader.Model)
	default:
		t.Logf("%d records, about $%.3f", len(records), est)
	}

	got := make([]grade.Grade, len(records))
	errs := make([]error, len(records))
	cost := 0.0
	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan int)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				r := records[i]
				for try := 0; try < 3; try++ { // one flaky call shouldn't cost the run its record
					var c float64
					got[i], c, errs[i] = grade.Answer(context.Background(), r.Config(cfg), r.SetFor(), r.Prompt, r.Answer)
					mu.Lock()
					cost += c
					mu.Unlock()
					if errs[i] == nil {
						break
					}
				}
			}
		}()
	}
	for i := range records {
		work <- i
	}
	close(work)
	wg.Wait()
	var ok []int
	for i, err := range errs {
		if err != nil {
			t.Errorf("%s: grading failed: %v", records[i].ID, err)
			continue
		}
		ok = append(ok, i)
	}
	scored := make([]grade.GoldenRecord, len(ok))
	grades := make([]grade.Grade, len(ok))
	for j, i := range ok {
		scored[j], grades[j] = records[i], got[i]
	}
	rep := goldenScore(scored, grades)
	t.Logf("cost $%.3f\n%s", cost, rep)
	for _, f := range goldenGated {
		if rate := rep.rate(f); rate < *goldenThreshold {
			t.Errorf("%s agrees with the labels on %.1f%% of %d records, below the threshold of %.1f%%", f, 100*rate, rep.N, 100**goldenThreshold)
		}
	}
}

// goldenGated are the fields the threshold applies to; tone is reported, not gated.
var goldenGated = []string{"verdict", "top_pick", "other_product"}

var goldenFields = []string{"verdict", "top_pick", "other_product", "tone"}

type goldenReport struct {
	N         int
	Agree     map[string]int
	Confusion map[[2]grade.Verdict]int // expected, got
	Disagreed []goldenDisagreement
}

type goldenDisagreement struct {
	Record grade.GoldenRecord
	Got    grade.Grade
	Fields []string
}

// goldenScore compares each grade with its record's label, field by field.
func goldenScore(records []grade.GoldenRecord, grades []grade.Grade) goldenReport {
	rep := goldenReport{N: len(records), Agree: map[string]int{}, Confusion: map[[2]grade.Verdict]int{}}
	for i, r := range records {
		g := grades[i]
		e, got := r.Expected, grade.GoldenExpectedOf(g, r.Brand.Name)
		rep.Confusion[[2]grade.Verdict{e.Verdict, got.Verdict}]++
		var off []string
		for f, same := range map[string]bool{"verdict": e.Verdict == got.Verdict, "top_pick": e.TopPick == got.TopPick, "other_product": e.OtherProduct == got.OtherProduct, "tone": e.Tone == got.Tone} {
			if same {
				rep.Agree[f]++
			} else {
				off = append(off, f)
			}
		}
		if len(off) > 0 {
			sort.Strings(off)
			rep.Disagreed = append(rep.Disagreed, goldenDisagreement{r, g, off})
		}
	}
	sort.Slice(rep.Disagreed, func(i, j int) bool { return rep.Disagreed[i].Record.ID < rep.Disagreed[j].Record.ID })
	return rep
}

func (r goldenReport) rate(field string) float64 {
	if r.N == 0 {
		return 0
	}
	return float64(r.Agree[field]) / float64(r.N)
}

func (r goldenReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "agreement over %d records:\n", r.N)
	for _, f := range goldenFields {
		fmt.Fprintf(&b, "  %-14s %3d/%d  %5.1f%%\n", f, r.Agree[f], r.N, 100*r.rate(f))
	}
	b.WriteString("verdict confusion (rows: label, columns: grader):\n")
	fmt.Fprintf(&b, "  %-12s", "")
	for _, v := range grade.Verdicts {
		fmt.Fprintf(&b, " %12s", v)
	}
	b.WriteByte('\n')
	for _, e := range grade.Verdicts {
		fmt.Fprintf(&b, "  %-12s", e)
		for _, g := range grade.Verdicts {
			fmt.Fprintf(&b, " %12d", r.Confusion[[2]grade.Verdict{e, g}])
		}
		b.WriteByte('\n')
	}
	if len(r.Disagreed) > 0 {
		fmt.Fprintf(&b, "%d records disagreed:\n", len(r.Disagreed))
	}
	for _, d := range r.Disagreed {
		e, g := d.Record.Expected, grade.GoldenExpectedOf(d.Got, d.Record.Brand.Name)
		fmt.Fprintf(&b, "- %s (%s, labeled by %s) on %s: %s\n", d.Record.ID, d.Record.Set, d.Record.LabeledBy, strings.Join(d.Fields, ", "), d.Record.Prompt)
		fmt.Fprintf(&b, "    label:  verdict=%s top_pick=%t other_product=%t tone=%q\n", e.Verdict, e.TopPick, e.OtherProduct, e.Tone)
		fmt.Fprintf(&b, "    grader: verdict=%s top_pick=%t other_product=%t tone=%q\n", g.Verdict, g.TopPick, g.OtherProduct, g.Tone)
		if d.Record.Evidence != "" {
			fmt.Fprintf(&b, "    label's evidence:  %q\n", d.Record.Evidence)
		}
		if d.Got.Evidence != "" {
			fmt.Fprintf(&b, "    grader's evidence: %q\n", d.Got.Evidence)
		}
		if d.Got.Reasoning != "" {
			fmt.Fprintf(&b, "    grader's reasoning: %s\n", d.Got.Reasoning)
		}
		if d.Record.Note != "" {
			fmt.Fprintf(&b, "    note: %s\n", d.Record.Note)
		}
	}
	return b.String()
}

// The committed golden set parses, every record is well formed, and every answer is about the
// sample brand: the fixtures are public, so an answer about any other brand must not be here.
func TestGoldenFixturesAreWellFormedAndBrandNeutral(t *testing.T) {
	records, err := grade.ReadGolden(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 {
		t.Fatalf("no records in %s", goldenDir)
	}
	cfg := goldenConfig(t)
	bySet, byLabeler, byStratum := map[string]int{}, map[string]int{}, map[string]int{}
	for _, r := range records {
		if r.Brand.Name != cfg.Brand.Name {
			t.Errorf("%s is about %q; only the sample brand's answers are committed", r.ID, r.Brand.Name)
		}
		bySet[r.Set]++
		byLabeler[r.LabeledBy]++
		st := string(r.Expected.Verdict)
		if r.Expected.TopPick {
			st = "top pick"
		} else if r.Expected.OtherProduct {
			st = "other product"
		}
		byStratum[st]++
	}
	t.Logf("%d records: sets %v, labeled by %v, strata %v", len(records), bySet, byLabeler, byStratum)
}

// The scorer itself: a record that agrees counts in every field, one that disagrees is listed with
// the fields it disagreed on, and the confusion table has it in its cell.
func TestGoldenScoring(t *testing.T) {
	brand := "Cheap Seats"
	records := []grade.GoldenRecord{
		{ID: "a", Brand: grade.GoldenBrand{Name: brand}, Expected: grade.GoldenExpected{Verdict: grade.Recommended, TopPick: true, Tone: grade.Positive}},
		{ID: "b", Brand: grade.GoldenBrand{Name: brand}, Expected: grade.GoldenExpected{Verdict: grade.Mentioned, Tone: grade.Neutral}},
		{ID: "c", Brand: grade.GoldenBrand{Name: brand}, Expected: grade.GoldenExpected{Verdict: grade.Absent, OtherProduct: true}},
	}
	other := "Profound"
	grades := []grade.Grade{
		{Verdict: grade.Recommended, TopPick: &brand, Tone: grade.Positive},
		{Verdict: grade.Recommended, TopPick: &other, Tone: grade.Neutral}, // verdict off, top pick still not the brand
		{Verdict: grade.Absent}, // other_product off
	}
	rep := goldenScore(records, grades)
	if rep.N != 3 || rep.Agree["verdict"] != 2 || rep.Agree["top_pick"] != 3 || rep.Agree["other_product"] != 2 || rep.Agree["tone"] != 3 {
		t.Fatalf("%+v", rep.Agree)
	}
	if rep.rate("verdict") < 0.66 || rep.rate("verdict") > 0.67 {
		t.Fatalf("rate %v", rep.rate("verdict"))
	}
	if rep.Confusion[[2]grade.Verdict{grade.Mentioned, grade.Recommended}] != 1 || rep.Confusion[[2]grade.Verdict{grade.Recommended, grade.Recommended}] != 1 {
		t.Fatalf("%v", rep.Confusion)
	}
	if len(rep.Disagreed) != 2 || rep.Disagreed[0].Record.ID != "b" || strings.Join(rep.Disagreed[0].Fields, ",") != "verdict" || strings.Join(rep.Disagreed[1].Fields, ",") != "other_product" {
		t.Fatalf("%+v", rep.Disagreed)
	}
	if s := rep.String(); !strings.Contains(s, "2 records disagreed") || !strings.Contains(s, "verdict          2/3") {
		t.Fatalf("%s", s)
	}
}

// A record is read back as written, and a malformed one is refused with its line.
func TestGoldenRecordsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	answer := "Use Cheap Seats: it's open source."
	r := grade.GoldenRecord{ID: grade.GoldenID(answer), Set: "s", Prompt: "q", Brand: grade.GoldenBrand{Name: "Cheap Seats"}, Competitors: []sets.Competitor{{Name: "Gauge"}},
		Answer: answer, Expected: grade.GoldenExpected{Verdict: grade.Recommended, TopPick: true, Tone: grade.Positive}, LabeledBy: "human", Note: "n"}
	if err := grade.WriteGolden(filepath.Join(dir, "s.jsonl"), []grade.GoldenRecord{r}); err != nil {
		t.Fatal(err)
	}
	got, err := grade.ReadGolden(dir)
	if err != nil || len(got) != 1 || got[0].ID != r.ID || got[0].Expected != r.Expected || got[0].Competitors[0].Name != "Gauge" || got[0].Note != "n" {
		t.Fatalf("%+v %v", got, err)
	}
	for name, bad := range map[string]func(*grade.GoldenRecord){
		"id":            func(r *grade.GoldenRecord) { r.ID = "g-000000000000" },
		"verdict":       func(r *grade.GoldenRecord) { r.Expected.Verdict = "maybe" },
		"top pick":      func(r *grade.GoldenRecord) { r.Expected.Verdict = grade.Mentioned },
		"other product": func(r *grade.GoldenRecord) { r.Expected.OtherProduct = true },
		"tone":          func(r *grade.GoldenRecord) { r.Expected.Tone = "glowing" },
		"labeler":       func(r *grade.GoldenRecord) { r.LabeledBy = "me" },
	} {
		b := r
		bad(&b)
		if err := b.Validate(); err == nil {
			t.Errorf("%s: a bad record validated", name)
		}
	}
	b := r
	b.LabeledBy = "me"
	grade.WriteGolden(filepath.Join(dir, "s.jsonl"), []grade.GoldenRecord{b})
	if _, err := grade.ReadGolden(dir); err == nil || !strings.Contains(err.Error(), "s.jsonl:1") {
		t.Fatalf("a bad line must be named: %v", err)
	}
}
