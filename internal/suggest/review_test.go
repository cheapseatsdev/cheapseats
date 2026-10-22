package suggest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/sets"
)

// A review's edits keep the prompt's id on a rewrite, retire as a comment, and add with ids that
// don't collide; edits to prompts the set doesn't have, no-op rewrites, repeats and the brand
// outside head-to-heads are dropped.
func TestReviewEdits(t *testing.T) {
	var sent string
	old := structuredCLI
	defer func() { structuredCLI = old }()
	structuredCLI = func(_ context.Context, model, system, prompt string, schema map[string]any, out any) error {
		sent = prompt
		return json.Unmarshal([]byte(`{
			"rewrites":[{"id":"best-tool","text":"Which AI visibility tool suits a five-person agency?","why":"flat: always absent"},
				{"id":"cost","text":"How much does it cost?","why":"same words"},{"id":"gone","text":"x","why":"y"}],
			"retire":[{"id":"cost","why":"duplicate of best-tool"}],
			"add":[{"stage":"pricing","text":"Is Popcorn cheaper than Profound?","intent":"choice"},
				{"stage":"comparison","text":"Is Profound legit?","intent":"choice"},
				{"stage":"implementation","text":"How do I set up an AI visibility tracker?","intent":"info"}]}`), out)
	}
	cfg := &config.Config{Brand: config.Brand{Name: "Popcorn"}, Analyst: config.Model{Model: "opus", Provider: config.ClaudeCode}}
	set := sets.Set{ID: "tools", Name: "Tools", File: "tools.toml", Prompts: []sets.Prompt{{ID: "best-tool", Text: "Which AI visibility tool is best?"}, {ID: "cost", Text: "How much does it cost?"}}}
	other := sets.Set{ID: "other", Prompts: []sets.Prompt{{ID: "set-up-ai-visibility-tracker", Text: "Is Profound legit?"}}}
	e, err := Review(context.Background(), cfg, set, []sets.Set{set, other}, "- flat (always_absent) best-tool: named in 0 of 24 answers")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent, "- best-tool: Which AI visibility tool is best?") || !strings.Contains(sent, "named in 0 of 24 answers") || !strings.Contains(sent, "Never name Popcorn") {
		t.Errorf("prompt:\n%s", sent)
	}
	if len(e.Rewrites) != 1 || len(e.Retire) != 1 || len(e.Add) != 1 || e.Dropped != 4 || e.CostUSD != 0 {
		t.Fatalf("%+v", e)
	}
	for _, want := range []string{"--- prompt-sets/tools.toml", " id = \"best-tool\"\n-text = \"Which AI visibility tool is best?\"\n+text = \"Which AI visibility tool suits a five-person agency?\"",
		"+# Retire cost: duplicate of best-tool.", "+id = \"set-up-ai-visibility-tracker-2\"\n+text = \"How do I set up an AI visibility tracker?\"\n+intent = \"info\""} {
		if !strings.Contains(e.Diff, want) {
			t.Errorf("diff lacks %q:\n%s", want, e.Diff)
		}
	}
}
