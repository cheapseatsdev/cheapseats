package sets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtureSetsLoad(t *testing.T) {
	files, _ := filepath.Glob("../../examples/popcorn-config/prompt-sets/*.toml")
	got, err := LoadAll("../../examples/popcorn-config/prompt-sets")
	if err != nil || len(files) == 0 || len(got) != len(files) {
		t.Fatalf("loaded %d of %d sets: %v", len(got), len(files), err)
	}
	if got[0].Tier != Core {
		t.Error("core sets should sort first")
	}
	if err := Check("../../examples/popcorn-config/prompt-sets", []string{"Popcorn"}); err != nil {
		t.Fatal(err)
	}
}

// A deploy with a broken set must fail to start, not run without it.
func TestLoadAllFailsOnABrokenSet(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.toml"), []byte("id = \"a\"\n[[prompts]]\nid = \"q\"\ntext = \"q\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.toml"), []byte("id = \"b\"\nprompts = [\"old style\"]\n"), 0o644)
	got, err := LoadAll(dir)
	if err == nil || !strings.Contains(err.Error(), "b.toml") || len(got) != 1 {
		t.Fatalf("got %d sets, err %v", len(got), err)
	}
}

func TestCheckCatchesTheBrandAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prompt := func(id, text string) string { return "\n[[prompts]]\nid = \"" + id + "\"\ntext = \"" + text + "\"\n" }
	write("a.toml", `id = "a"`+prompt("any-good", "Is popcorn any good?"))
	write("b.toml", `id = "b"`+prompt("what-to-use", "What should I use?"))
	write("d.toml", `id = "a"`+prompt("dup-set", "A set reusing a set id"))
	write("c.toml", `id = "c"`+prompt("what-to-use", "Something else entirely?"))
	write("e.toml", `id = "e"`+prompt("same-again", "What  should I use?"))
	write("h.toml", `id = "head-to-heads"`+prompt("vs-docker", "Popcorn or Docker?"))
	write("old.toml", `id = "old"
prompts = ["A list of strings, from before ids"]`)
	write("bad.toml", `id = "Bad Id"`)
	err := Check(dir, []string{"Popcorn"})
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{
		"a.toml: prompt any-good names Popcorn",
		`d.toml: id "a" is already used`,
		`c.toml: prompt id "what-to-use" is already used in b`,
		`e.toml: prompt "same-again" asks the same thing as "what-to-use"`,
		"old.toml", "popcorn migrate-config",
		"bad.toml: id must be",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "h.toml") {
		t.Errorf("head-to-heads may name the brand:\n%v", err)
	}
}

func TestMigrateGivesEveryPromptAnID(t *testing.T) {
	dir := t.TempDir()
	src := `# A comment that stays.
id = "s"
name = "S"
prompts = [
  "How do I measure AI visibility?",  # trailing comment
  "What's the [best] way to track \"mentions\"?",
  """A multi-line
question""",
]

[[competitors]]
name = "Peec AI"
`
	os.WriteFile(filepath.Join(dir, "s.toml"), []byte(src), 0o644)
	os.WriteFile(filepath.Join(dir, "t.toml"), []byte("id = \"t\"\nprompts = [\"How do I measure AI visibility?\"]\n"), 0o644)
	changed, err := Migrate(dir)
	if err != nil || len(changed) != 2 {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	s, err := Parse(filepath.Join(dir, "s.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Prompts) != 3 || s.Prompts[0].ID != "measure-ai-visibility" || s.Prompts[1].Text != `What's the [best] way to track "mentions"?` || s.Prompts[2].Text != "A multi-line\nquestion" {
		t.Fatalf("prompts: %+v", s.Prompts)
	}
	if len(s.Competitors) != 1 {
		t.Fatalf("competitors lost: %+v", s.Competitors)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "s.toml"))
	if !strings.Contains(string(b), "# A comment that stays.") {
		t.Errorf("comment lost:\n%s", b)
	}
	// The same words in another set get their own id: ids are unique across sets.
	tt, _ := Parse(filepath.Join(dir, "t.toml"))
	if tt.Prompts[0].ID != "measure-ai-visibility-2" {
		t.Errorf("t: %+v", tt.Prompts)
	}
	if again, _ := Migrate(dir); len(again) != 0 {
		t.Errorf("a second migrate changed %v", again)
	}
}

func TestOldSnapshotsStillRead(t *testing.T) {
	var s Set
	if err := json.Unmarshal([]byte(`{"id":"s","prompts":["plain text"]}`), &s); err != nil || s.Prompts[0].Text != "plain text" || s.Prompts[0].ID != "" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestFingerprintIncludesCompetitorMapping(t *testing.T) {
	s := Set{ID: "x", Prompts: []Prompt{{ID: "q", Text: "q"}}, Competitors: []Competitor{{Name: "one"}}}
	a := Fingerprint(s)
	s.Competitors[0].Name = "two"
	if a == Fingerprint(s) {
		t.Fatal("competitor mapping not versioned")
	}
}

func TestNamesMatchesWholeWords(t *testing.T) {
	for _, c := range []struct {
		text, name string
		want       bool
	}{
		{"Is rl any good?", "rl", true},
		{"Paste the url here", "rl", false},
		{"Popcorn vs Profound", "popcorn", true},
		{"popcorn.example docs", "popcorn.example", true},
		{"microwave popcorns", "popcorn", false},
		{"anything", "", false},
	} {
		if got := Names(c.text, c.name); got != c.want {
			t.Errorf("Names(%q, %q) = %v", c.text, c.name, got)
		}
	}
}

// Competitors' domains are checked, and never change a set's version: they only decide whose
// pages are whose, so adding them mustn't read as a change to the set.
func TestCompetitorDomains(t *testing.T) {
	dir := t.TempDir()
	base := "id = \"a\"\n[[prompts]]\nid = \"q\"\ntext = \"q\"\n[[competitors]]\nname = \"Y\"\n"
	parse := func(extra string) (Set, error) {
		p := filepath.Join(dir, "a.toml")
		os.WriteFile(p, []byte(base+extra), 0o644)
		return Parse(p)
	}
	without, err := parse("")
	if err != nil {
		t.Fatal(err)
	}
	with, err := parse("domains = [\"y.com\"]\n")
	if err != nil || with.Competitors[0].Domains[0] != "y.com" {
		t.Fatalf("%+v %v", with, err)
	}
	if with.Version != without.Version {
		t.Error("domains changed the set's version")
	}
	if _, err := parse("domains = [\"\"]\n"); err == nil {
		t.Error("an empty domain should be an error")
	}
}
