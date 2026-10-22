package queries

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/grade"
	"github.com/metcalfc/popcorn/internal/run"
	"github.com/metcalfc/popcorn/internal/sets"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		`  Best  "AI visibility"   tools 2026 `: "best ai visibility tools 2026",
		"“Peec AI” vs ‘Profound’":               "peec ai vs profound",
		"'quoted' it's fine":                    "quoted it's fine",
		"Tabs\tand\nnewlines":                   "tabs and newlines",
		`""`:                                    "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokens(t *testing.T) {
	got := Tokens("What tools track how often ChatGPT mentions a brand? Otterly.AI, tools, it's")
	want := []string{"tools", "track", "often", "chatgpt", "mentions", "brand", "otterly.ai"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tokens = %q, want %q", got, want)
	}
}

func TestOverlap(t *testing.T) {
	// Prompt words: tools, track, brand, mentions, chatgpt. Query words: best, tool, track,
	// chatgpt, mentions, 2026: four of six are the prompt's, four of five prompt words kept.
	from, kept, added, dropped := Overlap("What tools track brand mentions in ChatGPT?", "best tool to track ChatGPT mentions 2026")
	if math.Abs(from-4.0/6) > 1e-9 || math.Abs(kept-4.0/5) > 1e-9 {
		t.Errorf("from %v kept %v", from, kept)
	}
	if !reflect.DeepEqual(added, []string{"best", "2026"}) || !reflect.DeepEqual(dropped, []string{"brand"}) {
		t.Errorf("added %q dropped %q", added, dropped)
	}
	if f, k, _, _ := Overlap("", "anything"); f != 0 || k != 0 {
		t.Errorf("empty prompt: %v %v", f, k)
	}
}

var (
	brand = config.Brand{Name: "Popcorn", Aliases: []string{"popcorn-cli"}, Domains: []string{"popcorn.example"}}
	set   = sets.Set{ID: "ai-search-visibility", File: "ai-search-visibility.toml",
		Prompts:     []sets.Prompt{{ID: "tools-track", Text: "What tools track AI mentions of a brand?"}},
		Competitors: []sets.Competitor{{Name: "Peec AI", Aliases: []string{"Peec"}}, {Name: "Profound"}, {Name: "RL"}}}
)

func TestInjected(t *testing.T) {
	names := Names(brand, set)
	got := func(prompt, query string) []string {
		var out []string
		for _, n := range Injected(names, prompt, query) {
			out = append(out, n.Name)
		}
		return out
	}
	if g := got("What tools track AI mentions?", "peec ai vs profound pricing"); !reflect.DeepEqual(g, []string{"Peec AI", "Profound"}) {
		t.Errorf("both added: %q", g)
	}
	// Named in the prompt already, by an alias: not added.
	if g := got("Is Peec any good?", "Peec AI review"); g != nil {
		t.Errorf("already in prompt: %q", g)
	}
	// The brand, by its alias; a short name only as a whole word ("rl" isn't in "url").
	if g := got("How do I check AI answers?", "popcorn-cli url checker"); !reflect.DeepEqual(g, []string{"Popcorn"}) {
		t.Errorf("brand alias: %q", g)
	}
}

func TestOnHost(t *testing.T) {
	names := Names(brand, set)
	peec, rl := names[1], names[3]
	for url, want := range map[string]bool{"https://peec.ai/pricing": true, "https://www.peec.ai": true, "https://docs.peec.ai/x": true, "https://peecock.com": false} {
		if peec.OnHost(url) != want {
			t.Errorf("Peec AI on %s: want %v", url, want)
		}
	}
	if rl.OnHost("https://rl.com") {
		t.Error("a name under four letters matched a host")
	}
}

func TestBuild(t *testing.T) {
	res := func(named bool, searches ...answer.Query) run.Result {
		v := grade.Absent
		if named {
			v = grade.Mentioned
		}
		return run.Result{PromptIdx: 0, PromptID: "tools-track", Prompt: set.Prompts[0].Text, Track: answer.Search, Status: "done",
			Answer: &answer.Answer{Searches: searches}, Grade: &grade.Grade{Verdict: v}}
	}
	competitor := answer.Query{Query: "Peec AI vs Profound", Completed: true, Results: []answer.Link{{URL: "https://peec.ai/blog", Title: "Peec AI vs Profound"}}}
	ours := answer.Query{Query: "best AI mention tracking tools", Completed: true, Results: []answer.Link{{URL: "https://docs.popcorn.example/x"}}}
	rep := Build(Input{Brand: brand, Set: set, Results: []run.Result{
		res(true, ours, competitor),
		res(false, answer.Query{Query: `"peec ai"  vs profound`, Completed: true, Results: competitor.Results}),
		res(false),
		{Track: answer.Recall, Status: "done", Answer: &answer.Answer{}},
	}})
	if rep.Answers != 3 || rep.Searched != 2 || rep.Searches != 3 || rep.Distinct != 2 {
		t.Fatalf("%+v", rep)
	}
	p := rep.Prompts[0]
	if p.PerAnswer != 1 || p.Distinct != 2 || p.Queries[0] != (Count{"Peec AI vs Profound", 2}) {
		t.Errorf("prompt: %+v", p)
	}
	top := rep.Queries[0]
	if top.Key != "peec ai vs profound" || top.Answers != 2 || top.FirstParty != 0 || top.Named != 1 || top.Graded != 2 ||
		!reflect.DeepEqual(top.Competitors, []Count{{"Peec AI", 2}, {"Profound", 2}}) || !reflect.DeepEqual(top.Injected, []string{"Peec AI", "Profound"}) {
		t.Errorf("top query: %+v", top)
	}
	if rep.Queries[1].FirstParty != 1 {
		t.Errorf("first-party: %+v", rep.Queries[1])
	}
	if len(rep.Injected) != 2 || rep.Injected[0].Searches != 2 || rep.Injected[0].Answers != 2 || rep.Injected[0].IsBrand {
		t.Errorf("injected: %+v", rep.Injected)
	}
	// The names a search adds count as injected, not as words added.
	var added []string
	for _, w := range rep.Added {
		added = append(added, w.Word)
	}
	if !reflect.DeepEqual(added, []string{"vs", "best", "tracking"}) {
		t.Errorf("added: %+v", rep.Added)
	}
	if len(rep.Suggested) != 1 || rep.Suggested[0].File != "ai-search-visibility.toml" {
		t.Fatalf("suggested: %+v", rep.Suggested)
	}
	// The snippet parses as a set's [[prompts]] entry.
	var parsed struct {
		Prompts []sets.Prompt `toml:"prompts"`
	}
	if _, err := toml.Decode(rep.Suggested[0].TOML, &parsed); err != nil || len(parsed.Prompts) != 1 || parsed.Prompts[0].ID != "peec-ai-vs-profound" || parsed.Prompts[0].Text != "Peec AI vs Profound" {
		t.Errorf("snippet %q: %+v %v", rep.Suggested[0].TOML, parsed, err)
	}
	if b := rep.Brief(); !strings.Contains(b, "Peec AI (2 searches, 1 prompts)") || !strings.Contains(b, "Words searches add") {
		t.Errorf("brief: %s", b)
	}
}

func TestPromptID(t *testing.T) {
	taken := map[string]bool{"best-ai-visibility-tools-2026": true}
	if id := PromptID("Best AI visibility tools 2026 for startups", taken); id != "best-ai-visibility-tools-2026-2" {
		t.Error(id)
	}
	if id := PromptID("“???”", nil); id != "search-query" {
		t.Error(id)
	}
}
