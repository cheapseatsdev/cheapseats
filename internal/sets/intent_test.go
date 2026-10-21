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
