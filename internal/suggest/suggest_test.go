package suggest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/sets"
)

// Suggestions that repeat a prompt in any set, repeat each other, or name the brand outside
// head-to-heads are dropped; the rest come out as [[prompts]] tables a set file parses, by stage,
// with fresh ids.
func TestSuggestions(t *testing.T) {
	var sent string
	old := structuredCLI
	defer func() { structuredCLI = old }()
	structuredCLI = func(_ context.Context, model, system, prompt string, schema map[string]any, out any) error {
		sent = prompt
		return json.Unmarshal([]byte(`{"prompts":[
			{"stage":"pricing","text":"How much do AI visibility trackers cost per month?","intent":"choice"},
			{"stage":"awareness","text":"What is   GEO, and should I care?","intent":"info"},
			{"stage":"comparison","text":"Which AI visibility tool is best?!","intent":"choice"},
			{"stage":"comparison","text":"Is Popcorn better than Profound?","intent":"choice"},
			{"stage":"pricing","text":"how much do AI visibility trackers cost per month","intent":"choice"},
			{"stage":"reputation","text":"Is Profound trustworthy?","intent":"choice"},
			{"stage":"implementation","text":"How long does it take to set up an AI visibility tracker?","intent":"info"}]}`), out)
	}
	cfg := &config.Config{Brand: config.Brand{Name: "Popcorn", Aliases: []string{"popcorn.zone"}, About: "a tracker", Audience: "a marketer"},
		Analyst: config.Model{Model: "opus", Provider: config.ClaudeCode}}
	set := sets.Set{ID: "tools", Name: "Tools", Description: "Picking a tool", Prompts: []sets.Prompt{{ID: "best-tool", Text: "Which AI visibility tool is best?"}},
		Competitors: []sets.Competitor{{Name: "Profound"}}}
	other := sets.Set{ID: "other", Prompts: []sets.Prompt{{ID: "is-profound-trustworthy", Text: "Is Profound legit?"}}}
	res, err := Prompts(context.Background(), cfg, set, []sets.Set{set, other}, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Who asks: a marketer", "- Which AI visibility tool is best?", "Never name Popcorn", "Competitors: Profound;", "Write 6 new questions"} {
		if !strings.Contains(sent, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if len(res.Prompts) != 3 || res.Dropped != 3 || res.CostUSD != 0 {
		t.Fatalf("%+v", res)
	}
	if strings.Index(res.Snippet, "# Awareness") > strings.Index(res.Snippet, "# Pricing") || !strings.Contains(res.Snippet, `text = "What is GEO, and should I care?"`+"\nintent = \"info\"") {
		t.Errorf("snippet:\n%s", res.Snippet)
	}
	// It pastes into a set file as it is.
	f := filepath.Join(t.TempDir(), "tools.toml")
	os.WriteFile(f, []byte("id = \"tools\"\nname = \"Tools\"\n\n"+res.Snippet), 0o644)
	parsed, err := sets.Parse(f)
	if err != nil || len(parsed.Prompts) != 3 || parsed.Prompts[0].ID == "" || parsed.Prompts[0].Intent != "info" {
		t.Fatalf("%+v %v\n%s", parsed, err, res.Snippet)
	}
	if _, err := Prompts(context.Background(), cfg, set, nil, 0); err == nil {
		t.Error("0 prompts should be refused")
	}
}
