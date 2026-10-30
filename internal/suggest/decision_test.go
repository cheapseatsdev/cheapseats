package suggest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// A syntactically valid educational question is not enough to become another recurring prompt.
// The same call can return fewer than requested, or no candidates, without filling funnel stages.
func TestSuggestRequiresDecisionValueAndAllowsNone(t *testing.T) {
	old := structuredCLI
	defer func() { structuredCLI = old }()
	empty := false
	structuredCLI = func(_ context.Context, _, system, prompt string, schema map[string]any, out any) error {
		for _, want := range []string{"Preserve worthwhile competitive losses", "A missing capability is not a copy fix", "return no additions"} {
			if !strings.Contains(system, want) {
				t.Errorf("system lacks %q", want)
			}
		}
		for _, want := range []string{"At most", "starter teams", "No enterprise procurement"} {
			if want == "At most" {
				want = "Return at most 5"
			}
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt lacks %q", want)
			}
		}
		if empty {
			return json.Unmarshal([]byte(`{"prompts":[]}`), out)
		}
		return json.Unmarshal([]byte(`{"prompts":[
			{"stage":"awareness","text":"What is GEO?","intent":"info"},
			{"stage":"comparison","text":"Which tracker should a small startup consider?","intent":"choice","buyer":"a founder starting measurement","learning":"whether manual work or a tracker fits","decision":"choose the first-run offer and prerequisites","repeat_value":"review after four weekly checks and stop when that decision is settled"}
		]}`), out)
	}
	cfg := &config.Config{Brand: config.Brand{Name: "Example", Audience: "a founder", Advisory: config.Advisory{Positioning: "starter teams", Boundaries: []string{"No enterprise procurement"}}}, Analyst: config.Model{Provider: config.ClaudeCode}}
	res, err := Prompts(context.Background(), cfg, sets.Set{ID: "discovery"}, nil, 5)
	if err != nil || len(res.Prompts) != 1 || res.Dropped != 1 || !strings.Contains(strings.Join(res.DroppedWhy, " "), "missing buyer") {
		t.Fatalf("decision gate: %+v %v", res, err)
	}
	if !strings.Contains(res.Snippet, "# Decision: choose the first-run offer") || strings.Contains(res.Snippet, `text = "What is GEO?"`) {
		t.Fatalf("unreviewable snippet: %s", res.Snippet)
	}
	empty = true
	res, err = Prompts(context.Background(), cfg, sets.Set{ID: "discovery"}, nil, 5)
	if err != nil || len(res.Prompts) != 0 || strings.Contains(res.Snippet, "[[prompts]]") {
		t.Fatalf("zero must stay zero: %+v %v", res, err)
	}
}

func TestSuggestWithoutBuyerContextDoesNotCallModel(t *testing.T) {
	old := structuredCLI
	defer func() { structuredCLI = old }()
	structuredCLI = func(context.Context, string, string, string, map[string]any, any) error {
		t.Fatal("a category alone must not spend an authoring call or invent a buyer")
		return nil
	}
	cfg := &config.Config{Brand: config.Brand{Name: "Example", About: "An analytics platform", Category: "analytics platforms"}, Analyst: config.Model{Provider: config.ClaudeCode}}
	res, err := Prompts(context.Background(), cfg, sets.Set{ID: "discovery"}, nil, 10)
	if err != nil || len(res.Prompts) != 0 || res.CostUSD != 0 || !strings.Contains(res.Note, "review the target buyer") {
		t.Fatalf("missing context: %+v %v", res, err)
	}
}

// A new cohort or an intent change cannot silently inherit the old question's ID/history.
// Missing preservation evidence also fails closed instead of accepting an old-format response.
func TestReviewRejectsUnsafeSameIDRewrites(t *testing.T) {
	old := structuredCLI
	defer func() { structuredCLI = old }()
	structuredCLI = func(_ context.Context, _, _, prompt string, _ map[string]any, out any) error {
		if !strings.Contains(prompt, "[intent: info]") {
			t.Errorf("current intent missing: %s", prompt)
		}
		return json.Unmarshal([]byte(`{"rewrites":[
			{"id":"cohort","text":"Which tool should an enterprise bank choose?","why":"new cohort","intent":"choice","meaning_changed":true},
			{"id":"method","text":"Which tool should I use to check recommendations?","why":"score recommendations instead","intent":"choice","meaning_changed":false},
			{"id":"missing","text":"Which tracker should I select?","why":"clearer","intent":"choice"}
		],"retire":[],"add":[{"stage":"pricing","text":"How do enterprise platforms price contracts?","intent":"info"}]}`), out)
	}
	set := sets.Set{ID: "discovery", File: "discovery.toml", Prompts: []sets.Prompt{
		{ID: "cohort", Text: "Which tool should a startup choose?"},
		{ID: "method", Text: "How can I check whether models recommend us?", Intent: sets.Info},
		{ID: "missing", Text: "Which tracker should I choose?"},
	}}
	cfg := &config.Config{Brand: config.Brand{Name: "Example"}, Analyst: config.Model{Provider: config.ClaudeCode}}
	res, err := Review(context.Background(), cfg, set, []sets.Set{set}, "all absent; preserve worthwhile losses")
	if err != nil || len(res.Rewrites) != 0 || len(res.Add) != 0 || res.Dropped != 4 {
		t.Fatalf("unsafe edits: %+v %v", res, err)
	}
	why := strings.Join(res.DroppedWhy, " ")
	for _, want := range []string{"new id", "intent must be preserved", "missing buyer"} {
		if !strings.Contains(why, want) {
			t.Errorf("missing rejection %q: %s", want, why)
		}
	}
}
