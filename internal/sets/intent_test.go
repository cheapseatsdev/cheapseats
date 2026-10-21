package sets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntent(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) (Set, error) {
		f := filepath.Join(dir, "s.toml")
		os.WriteFile(f, []byte("id = \"s\"\n"+body), 0o644)
		return Parse(f)
	}
	s, err := write("[[prompts]]\nid = \"how\"\ntext = \"How do I x?\"\nintent = \"info\"\n\n[[prompts]]\nid = \"which\"\ntext = \"Which x?\"\nintent = \"selection\"\n")
	if err != nil || !s.Prompts[0].Info() || s.Prompts[1].Info() || s.Prompts[1].Intent != "" {
		t.Fatalf("%+v %v", s.Prompts, err)
	}
	plain, _ := write("[[prompts]]\nid = \"how\"\ntext = \"How do I x?\"\n\n[[prompts]]\nid = \"which\"\ntext = \"Which x?\"\n")
	s.Prompts[0].Intent = ""
	if Fingerprint(s) != Fingerprint(plain) {
		t.Error("spelling out the default intent changed the set's fingerprint")
	}
	if _, err := write("[[prompts]]\nid = \"how\"\ntext = \"How?\"\nintent = \"howto\"\n"); err == nil || !strings.Contains(err.Error(), "intent") {
		t.Errorf("an unknown intent should fail: %v", err)
	}
}

func TestShareAddsCompetitorsASetDoesntList(t *testing.T) {
	own := Set{ID: "a", Prompts: []Prompt{{ID: "q", Text: "q"}}, Competitors: []Competitor{{Name: "Peec AI", Aliases: []string{"Peec"}}}}
	own.Version = Fingerprint(own)
	shared := []Competitor{{Name: "peec ai"}, {Name: "Profound"}}
	got := Share([]Set{own}, shared)[0]
	if len(got.Competitors) != 2 || got.Competitors[0].Aliases[0] != "Peec" || got.Competitors[1].Name != "Profound" {
		t.Fatalf("%+v", got.Competitors)
	}
	if got.Version == own.Version || len(own.Competitors) != 1 {
		t.Errorf("the merged set is a new version, and the original is untouched")
	}
	moved := Share([]Set{{ID: "a", Prompts: own.Prompts}}, []Competitor{{Name: "Peec AI", Aliases: []string{"Peec"}}})[0]
	if moved.Version != own.Version {
		t.Errorf("moving a set's competitors to the shared list should change nothing")
	}
}
