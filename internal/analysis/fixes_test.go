package analysis

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

func TestPriorityWeighsReachAgainstEffort(t *testing.T) {
	fix := func(prompts int, effort string) Fix { return Fix{Prompts: make([]int, prompts), Effort: effort} }
	for _, c := range []struct {
		f    Fix
		want string
	}{
		{fix(4, "M"), "urgent"}, // 40% of 10
		{fix(4, "L"), "high"},   // a big job is never urgent
		{fix(2, "M"), "high"},
		{fix(1, "M"), "medium"},
		{fix(1, "S"), "high"}, // a one-day fix is bumped a step
		{fix(0, "M"), "low"},
	} {
		if got := PriorityOf(c.f, 10); got != c.want {
			t.Errorf("%d prompts, effort %s: %s, want %s", len(c.f.Prompts), c.f.Effort, got, c.want)
		}
	}
	if fix(2, "S").Speed() <= fix(4, "M").Speed() {
		t.Error("two prompts in a day should beat four in a week")
	}
}

func TestBuildKeepsUnverifiedCandidatesOutOfActions(t *testing.T) {
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sent = string(body)
		out := `{"insights":"## Why Cheap Seats lost\nCoder is GA.","fixes":[` +
			`{"rank":2,"type":"outreach","title":"Get into the roundup","target":"https://example.com/roundup","claim":"c","done_when":"d","check":"Inspect https://example.com/roundup for current inclusion and the publisher eligibility rules.","decision":"Choose whether this specific publisher is an eligible distribution route for our starter product.","evidence":"e","prompts":[2,2,9],"effort":"S","displaces":""},` +
			`{"rank":1,"type":"new-page","title":"Publish the devcontainer page","target":"cheapseats.example/docs/devcontainers","claim":"c","done_when":"d","check":"Inspect cheapseats.example/docs/devcontainers to determine whether the existing setup instructions already answer this question.","decision":"Decide whether our existing setup instructions need a material change before recommending this entry route.","evidence":"e","prompts":[1,2],"effort":"M","displaces":"https://coder.com/docs"},` +
			`{"rank":3,"type":"product","title":"Nothing real","target":"x","claim":"c","done_when":"d","evidence":"e","prompts":[7],"effort":"L","displaces":null}]}`
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": out}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 10}})
	}))
	defer srv.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")

	cfg := &config.Config{
		Brand:   config.Brand{Name: "Cheap Seats", Domains: []string{"cheapseats.example"}, Facts: []string{"PUBLIC-FACT"}},
		Analyst: config.Model{Model: "fake"},
		Owners:  config.Owners{Default: "growth", ByGap: map[string]string{"documentation": "docs"}},
	}
	set := sets.Set{Name: "S", Prompts: []sets.Prompt{{ID: "first", Text: "first question"}, {ID: "second", Text: "second question"}}}
	why := "preferred Coder"
	results := []run.Result{{PromptIdx: 0, Answerer: "claude", Track: answer.Search, Status: "done",
		Grade:  &grade.Grade{Verdict: grade.Absent, WhyNotTop: &why},
		Answer: &answer.Answer{Citations: []answer.Link{{URL: "https://coder.com/docs"}}}}}
	a, _, err := Build(context.Background(), cfg, set, results, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PUBLIC-FACT", "Prompt 1: first question", "preferred Coder", "no first-party URL recorded; not proof of a missing or defective page", "https://coder.com/docs", "named 0/1, recommended 0/1", "a prompt's count is noisy"} {
		if !strings.Contains(sent, want) {
			t.Errorf("analyst input lacks %q", want)
		}
	}
	if !strings.Contains(a.Insights, "Coder is GA") {
		t.Error(a.Insights)
	}
	if len(a.Fixes) != 0 || len(a.Investigations) != 2 {
		t.Fatalf("unverified candidates must be checks, not actions: %+v", a)
	}
	if a.Investigations[0].Target != "cheapseats.example/docs/devcontainers" || len(a.Investigations[1].Prompts) != 1 {
		t.Errorf("candidate sorting and prompt cleanup: %+v", a.Investigations)
	}
}

func TestTriageSendsAFixToItsSitesOwner(t *testing.T) {
	cfg := &config.Config{Brand: config.Brand{Sites: []config.Site{
		{URL: "acme.com", Kind: "marketing"},
		{URL: "docs.acme.com", Kind: "docs", Owner: "docs team"},
	}}}
	cfg.Owners.Default = "growth"
	cfg.Owners.ByGap = map[string]string{"third-party": "pr"}
	set := sets.Set{Prompts: []sets.Prompt{{ID: "q", Text: "q"}}}
	for _, c := range []struct {
		fix         Fix
		site, owner string
	}{
		{Fix{Type: NewPage, Target: "docs.acme.com/guides/sso", Prompts: []int{1}, Effort: "S"}, "docs.acme.com", "docs team"},
		{Fix{Type: Positioning, Target: "https://acme.com/pricing", Prompts: []int{1}, Effort: "S"}, "acme.com", "growth"}, // no site owner: the default
		{Fix{Type: Outreach, Target: "https://roundup.example/best-tools", Prompts: []int{1}, Effort: "S"}, "", "pr"},
	} {
		f := Triage(cfg, set, c.fix)
		if f.Site != c.site || f.Owner != c.owner {
			t.Errorf("%s: site %q owner %q, want %q %q", c.fix.Target, f.Site, f.Owner, c.site, c.owner)
		}
	}
}

func TestMisstatementsReachTheAnalyst(t *testing.T) {
	if misstatementsText(context.Background(), "X") != "" {
		t.Error("nothing to say with no fact check")
	}
	ctx := WithMisstatements(context.Background(), []string{`Fact 2 ("It is self-hosted.") is contradicted by 2 answers, on prompts 1, 4.`})
	got := misstatementsText(ctx, "X")
	for _, want := range []string{"Answers that named X contradicted these public facts", `- Fact 2 ("It is self-hosted.") is contradicted by 2 answers, on prompts 1, 4.`, "make done_when a team-controlled check", "separately as observations, not completion requirements"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
