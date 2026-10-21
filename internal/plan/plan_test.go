package plan

import (
	"strings"
	"testing"

	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/sets"
)

func TestPlanSaysWhatAChangeDoes(t *testing.T) {
	cfg := func(model, grader string) *config.Config {
		c := &config.Config{Brand: config.Brand{Name: "Popcorn"}, Grader: config.Model{Model: grader},
			Answerers: []config.Answerer{{ID: "claude", Provider: "anthropic", Model: model, Samples: config.Samples{Search: 2, Recall: 1}}}}
		c.Tiers.PLG.MaxSamples = 1
		return c
	}
	p := func(id, text string) sets.Prompt { return sets.Prompt{ID: id, Text: text} }
	before := Config{Cfg: cfg("opus-1", "g"), Sets: []sets.Set{
		{ID: "a", Name: "A", Tier: sets.Core, Prompts: []sets.Prompt{p("keep", "How do I measure AI visibility?"), p("tweak", "Which tools track AI answers?"), p("drop", "Is it worth it?"), p("old-id", "What does it cost?")}},
		{ID: "b", Name: "B", Tier: sets.Core, Prompts: []sets.Prompt{p("mover", "Can I self-host it?")}},
	}}
	after := Config{Cfg: cfg("opus-2", "g"), Sets: []sets.Set{
		{ID: "a", Name: "A renamed", Tier: sets.Core, Prompts: []sets.Prompt{p("keep", "How do I measure AI visibility?"), p("tweak", "Which tools track AI answers today?"), p("new-id", "What does it cost?"), p("mover", "Can I self-host it?"), p("swap", "Completely unrelated question about pricing tiers")}},
	}}
	got := Markdown(before, after)
	for _, want := range []string{
		"Added: `new-id`, `swap`",
		"Removed: `drop`, `old-id`",
		"Reworded: `tweak`",
		"`mover` moved here from `b`",
		"`new-id` asks exactly what removed prompt `old-id` asked",
		"`b` removed",
		"`claude`: model opus-1 → opus-2",
		"| a | core | 5 | 15 |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "starts over") {
		t.Errorf("nothing here changes the method:\n%s", got)
	}
	after.Cfg.Brand.Sites = []config.Site{{URL: "docs.popcorn.example", Kind: "docs"}}
	if got := Markdown(before, after); strings.Contains(got, "starts over") || !strings.Contains(got, "The brand's sites changed") {
		t.Errorf("a site change notes where fixes go and keeps trends:\n%s", got)
	}
	after.Cfg.Grader.Model = "g2"
	if got := Markdown(before, after); !strings.Contains(got, "Every set's trend starts over**, because the grader model changed") {
		t.Errorf("a grader change should warn:\n%s", got)
	}
	if got := Markdown(before, before); !strings.Contains(got, "No change to what Popcorn asks") {
		t.Errorf("no-op plan:\n%s", got)
	}
}
