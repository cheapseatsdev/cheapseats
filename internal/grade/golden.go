package grade

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// The golden set is labeled answers the grader is measured against (docs/grading.md): one record
// per stored answer, with what grading it should say. `cheapseats golden export` writes them from
// a database snapshot with the current grader's verdict pre-filled, a human flips the ones that
// are wrong, and TestGolden grades them all under today's System and reports the agreement.

// GoldenRecord is one labeled answer, a line of a testdata/golden/*.jsonl file.
type GoldenRecord struct {
	// A short hash of the answer text: the same answer gets the same id on every export, so a
	// human's label survives re-exports.
	ID       string `json:"id"`
	Set      string `json:"set"`
	PromptID string `json:"prompt_id,omitempty"`
	Prompt   string `json:"prompt"`
	Intent   string `json:"intent,omitempty"`
	// The brand and competitors exactly as the grader receives them (Input).
	Brand       GoldenBrand       `json:"brand"`
	Competitors []sets.Competitor `json:"competitors"`
	// The assistant's answer text. The grader reads nothing else of the answer: citations and
	// searches never reach it, so they aren't here.
	Answer   string         `json:"answer"`
	Expected GoldenExpected `json:"expected"`
	// The quote the grader gave for Expected when the record was exported; for the human reading
	// the record, not scored.
	Evidence string `json:"evidence,omitempty"`
	// "claude" for a label the grader wrote (the export), "human" once a person has checked it.
	LabeledBy string `json:"labeled_by"`
	Note      string `json:"note"`
	// Where the answer came from, for going back to it.
	Source *GoldenSource `json:"source,omitempty"`
}

type GoldenBrand struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	About   string   `json:"about,omitempty"`
}

// GoldenExpected is the part of a grade the golden set scores: the verdict, whether the brand is
// the answer's top pick (results.top_pick), whether the name meant another product, and the tone.
// The rest of a grade (position, the names list, objections, praise, the quote) varies in wording
// from one grading to the next and isn't scored.
type GoldenExpected struct {
	Verdict      Verdict `json:"verdict"`
	TopPick      bool    `json:"top_pick"`
	OtherProduct bool    `json:"other_product"`
	Tone         Tone    `json:"tone,omitempty"`
}

type GoldenSource struct {
	Run      string `json:"run,omitempty"`
	Result   int64  `json:"result,omitempty"`
	Answerer string `json:"answerer,omitempty"`
	Track    string `json:"track,omitempty"`
	// The grader (model) and the method the stored grade was made under.
	Grader string `json:"grader,omitempty"`
	Method string `json:"method,omitempty"`
}

// GoldenLabelers are the values LabeledBy takes.
var GoldenLabelers = []string{"claude", "human"}

// GoldenID is the record id for an answer: "g-" and the first 12 hex digits of the SHA-256 of the
// answer text with surrounding space trimmed.
func GoldenID(answer string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(answer)))
	return "g-" + hex.EncodeToString(h[:])[:12]
}

// GoldenExpectedOf is what a grade says in the scored fields.
func GoldenExpectedOf(g Grade, brand string) GoldenExpected {
	return GoldenExpected{Verdict: g.Verdict, TopPick: g.IsTopPick(brand), OtherProduct: g.OtherProduct, Tone: g.Tone}
}

// GoldenBrandOf is the brand as the grader receives it.
func GoldenBrandOf(b config.Brand) GoldenBrand {
	return GoldenBrand{Name: b.Name, Aliases: b.Aliases, About: b.About}
}

// Config is a grading config for the record's brand, for Answer and Input.
func (r GoldenRecord) Config(base *config.Config) *config.Config {
	cfg := *base
	cfg.Brand = config.Brand{Name: r.Brand.Name, Aliases: r.Brand.Aliases, About: r.Brand.About}
	return &cfg
}

// SetFor is the set as the grader needs it: only the competitors.
func (r GoldenRecord) SetFor() sets.Set {
	return sets.Set{ID: r.Set, Competitors: r.Competitors}
}

// Validate is why the record isn't a well-formed golden record, or nil.
func (r GoldenRecord) Validate() error {
	switch {
	case r.ID != GoldenID(r.Answer):
		return fmt.Errorf("id %q isn't the hash of its answer (%s)", r.ID, GoldenID(r.Answer))
	case r.Set == "" || r.Prompt == "" || strings.TrimSpace(r.Answer) == "" || r.Brand.Name == "":
		return fmt.Errorf("%s: set, prompt, answer and brand.name are required", r.ID)
	case r.Expected.Verdict != Recommended && r.Expected.Verdict != Mentioned && r.Expected.Verdict != Absent:
		return fmt.Errorf("%s: verdict %q isn't one of recommended, mentioned, absent", r.ID, r.Expected.Verdict)
	case r.Expected.TopPick && r.Expected.Verdict != Recommended:
		return fmt.Errorf("%s: a top pick is recommended, not %s", r.ID, r.Expected.Verdict)
	case r.Expected.OtherProduct && r.Expected.Verdict != Absent:
		return fmt.Errorf("%s: a name used for another product is absent, not %s", r.ID, r.Expected.Verdict)
	case r.Expected.Tone != "" && (r.Expected.Verdict == Absent || !containsTone(r.Expected.Tone)):
		return fmt.Errorf("%s: tone %q: only a named brand has one of %v", r.ID, r.Expected.Tone, Tones)
	case r.LabeledBy != "claude" && r.LabeledBy != "human":
		return fmt.Errorf("%s: labeled_by %q isn't claude or human", r.ID, r.LabeledBy)
	}
	return nil
}

func containsTone(t Tone) bool {
	for _, x := range Tones {
		if x == t {
			return true
		}
	}
	return false
}

// ReadGolden reads every *.jsonl file in dir, in file order; a record that doesn't validate is an
// error naming its file and line.
func ReadGolden(dir string) ([]GoldenRecord, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []GoldenRecord
	seen := map[string]string{}
	for _, f := range files {
		rs, err := ReadGoldenFile(f)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if prev, dup := seen[r.ID]; dup {
				return nil, fmt.Errorf("%s: %s is also in %s", f, r.ID, prev)
			}
			seen[r.ID] = f
		}
		out = append(out, rs...)
	}
	return out, nil
}

// ReadGoldenFile reads one .jsonl file.
func ReadGoldenFile(path string) ([]GoldenRecord, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var out []GoldenRecord
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 16<<20) // answers run to tens of KB
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r GoldenRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// WriteGolden writes records to path as JSON lines, sorted by id so a re-export diffs cleanly.
func WriteGolden(path string, records []GoldenRecord) error {
	rs := append([]GoldenRecord(nil), records...)
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	var b strings.Builder
	for _, r := range rs {
		js, err := json.Marshal(r)
		if err != nil {
			return err
		}
		b.Write(js)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
