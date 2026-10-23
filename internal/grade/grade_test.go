package grade

import (
	"context"
	"encoding/json"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// With provider = "claude-code" the grader runs on the claude CLI: no API key, no API call, no
// cost, and the same rules after it (here, absent for an answer that never names the brand).
func TestGradingOnTheClaudeCLI(t *testing.T) {
	dir := t.TempDir()
	out := `{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"verdict":"recommended","position":1,"top_pick":null,"named":["Gauge"],"reasoning":"r","why_not_top":null,"objections":[],"other_product":false,"tone":"positive","praise":["credible"],"evidence":"Use Cheap Seats."}}`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\ncat > /dev/null\nprintf '%s\\n' '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	old := llm.AnthropicURL
	llm.AnthropicURL = "http://127.0.0.1:1/never-called"
	defer func() { llm.AnthropicURL = old }()
	cfg := &config.Config{Brand: config.Brand{Name: "Cheap Seats"}, Grader: config.Model{Model: "claude-haiku-5-5", Provider: config.ClaudeCode}}
	g, cost, err := Answer(context.Background(), cfg, sets.Set{}, "Is Gauge any good?", "Yes, Gauge is a credible choice.")
	if err != nil || cost != 0 || g.Verdict != Absent || g.Position != nil {
		t.Fatalf("%+v cost=%v %v", g, cost, err)
	}
	if g.Tone != "" || len(g.Praise) > 0 {
		t.Fatalf("an absent brand has no tone or praise: %+v", g)
	}
	g, _, err = Answer(context.Background(), cfg, sets.Set{}, "Cheap Seats or Gauge?", "Use Cheap Seats.")
	if err != nil || g.Verdict != Recommended || g.Tone != Positive || len(g.Praise) != 1 || g.Evidence != "Use Cheap Seats." {
		t.Fatalf("a named brand keeps the CLI's verdict, tone and praise: %+v %v", g, err)
	}
}

func TestGraderSeesTheAnswerAndBrand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for _, want := range []string{"Brand: Cheap Seats", "About the brand: workstations", "Use Cheap Seats."} {
			if !strings.Contains(string(body), want) {
				t.Errorf("grader request lacks %q", want)
			}
		}
		top := "Cheap Seats"
		text, _ := json.Marshal(Grade{Verdict: Mentioned, TopPick: &top, Named: []string{"Cheap Seats"}})
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}})
	}))
	defer srv.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	cfg := &config.Config{Brand: config.Brand{Name: "Cheap Seats", About: "workstations"}}
	g, _, e := Answer(context.Background(), cfg, sets.Set{}, "public question", "Use Cheap Seats.")
	if e != nil || g.Verdict != Recommended {
		t.Fatalf("a top pick must be recommended: %+v %v", g, e)
	}
}

// On a question about a competitor the model graded that competitor as the brand: "recommended" at
// position 1 for an answer that never names Cheap Seats (run r-20261021-200546-84c081, result 236).
func TestAnAnswerThatNeverNamesTheBrandIsAbsent(t *testing.T) {
	brand := config.Brand{Name: "Cheap Seats", Aliases: []string{"cheapseats.dev", "cheapseatsdev/cheapseats"}}
	pos, top := 1, "Cheap Seats"
	g := Grade{Verdict: Recommended, Position: &pos, TopPick: &top, Objections: []string{"young company"}}
	if namesBrand("Yes, Gauge is a credible choice. Alternatives: Peec AI, Profound.", brand) {
		t.Fatal("matched a brand that isn't there")
	}
	g.notNamed(brand.Name)
	g.normalize(brand.Name)
	if g.Verdict != Absent || g.Position != nil || g.TopPick != nil || len(g.Objections) > 0 {
		t.Fatalf("%+v", g)
	}
	for _, answer := range []string{"Try CHEAP SEATS for this.", "See cheapseats.dev/docs.", "github.com/cheapseatsdev/cheapseats is open source."} {
		if !namesBrand(answer, brand) {
			t.Errorf("missed the brand in %q", answer)
		}
	}

	// The same through Answer, with a grader that makes the mistake.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text, _ := json.Marshal(Grade{Verdict: Recommended, Position: &pos, Named: []string{"Gauge", "Peec AI"}})
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}})
	}))
	defer srv.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	got, _, err := Answer(context.Background(), &config.Config{Brand: brand}, sets.Set{}, "Is Gauge a good tool?", "Yes, Gauge is a credible choice. Alternatives: Peec AI.")
	if err != nil || got.Verdict != Absent || got.Position != nil {
		t.Fatalf("an answer that never names the brand must be absent: %+v %v", got, err)
	}
}

// Another product can share the brand's name: answers about unrelated products with a name like
// the brand's were graded "mentioned" for it (run r-20261022-043102-ebd82f, results 671, 695,
// 699). The grader says so in other_product, and that makes the grade absent;
// an answer that can't find the brand, without taking it for something else, still names it.
func TestTheNameUsedForAnotherProductIsAbsent(t *testing.T) {
	brand := config.Brand{Name: "Cheap Seats", Aliases: []string{"cheapseats.dev"}, About: "an open-source, self-hosted AI visibility tracker"}
	pos := 2
	var reply Grade
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for _, want := range []string{"About the brand: an open-source", "other_product", "share the brand's name"} {
			if !strings.Contains(string(body), want) {
				t.Errorf("grader request lacks %q", want)
			}
		}
		text, _ := json.Marshal(reply)
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}})
	}))
	defer srv.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	ask := func(answer string) Grade {
		t.Helper()
		g, _, err := Answer(context.Background(), &config.Config{Brand: brand}, sets.Set{}, "Cheap Seats vs Profound?", answer)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}

	reply = Grade{Verdict: Mentioned, Position: &pos, Named: []string{"Profound", "Cheap Seats AI"}, Objections: []string{"WhatsApp agents"}, OtherProduct: true}
	if g := ask("I couldn't verify the Cheap Seats you mean; trycheapseats.ai sells WhatsApp sales agents. Pick Profound."); g.Verdict != Absent || g.Position != nil || len(g.Objections) > 0 || !g.OtherProduct {
		t.Errorf("another product's name must be absent: %+v", g)
	}

	reply = Grade{Verdict: Mentioned, Position: &pos, Named: []string{"Profound", "Cheap Seats"}, Objections: []string{"could not verify"}}
	if g := ask("I couldn't find a product called Cheap Seats, so pick Profound."); g.Verdict != Mentioned || g.Position == nil {
		t.Errorf("not finding the brand still names it: %+v", g)
	}
}

func TestAbsentBrandDoesNotGetInventedObjectionAndTiesStayUnpicked(t *testing.T) {
	why := "Preferred another tool"
	g := Grade{Verdict: Absent, WhyNotTop: &why, Objections: []string{"unsupported"}}
	g.normalize("Cheap Seats")
	if g.WhyNotTop != nil || len(g.Objections) > 0 {
		t.Fatal(g)
	}
	g = Grade{Verdict: Recommended}
	g.normalize("Cheap Seats")
	if g.TopPick != nil {
		t.Fatal("tie forced into a top pick")
	}
}

// Tone and praise describe how the answer talks about the brand, so only a named brand has them,
// and a tone outside the four (the schema's "none", or nothing from an older grade) is no tone.
func TestToneAndPraiseOnlyForANamedBrand(t *testing.T) {
	g := Grade{Verdict: Absent, Tone: Positive, Praise: []string{"open source"}}
	g.normalize("Cheap Seats")
	if g.Tone != "" || g.Praise != nil {
		t.Fatalf("%+v", g)
	}
	for tone, want := range map[Tone]Tone{"none": "", "": "", "glowing": "", Mixed: Mixed, Negative: Negative} {
		g = Grade{Verdict: Mentioned, Tone: tone, Praise: []string{"cheap"}}
		g.normalize("Cheap Seats")
		if g.Tone != want || len(g.Praise) != 1 {
			t.Errorf("tone %q: %+v", tone, g)
		}
	}
	for _, f := range []string{"tone", "praise"} {
		if !slices.Contains(schema["required"].([]string), f) || !strings.Contains(System, "\n"+f+":") {
			t.Errorf("%s isn't required by the schema and described in System", f)
		}
	}
}

// The evidence quote is only ever the answer's own words: a quote that differs only in markdown,
// spacing, case or curly quotes is found and replaced by the span of the answer it covers; one the
// grader reworded or joined from two places is dropped, never shown.
func TestEvidenceIsVerifiedAgainstTheAnswer(t *testing.T) {
	answer := "Options:\n\n1. **Cheap Seats** is the  `free` place to start —\n   it runs on your Claude subscription.\n2. Profound is “enterprise grade”."
	for quote, want := range map[string]string{
		"Cheap Seats is the free place to start — it runs on your Claude subscription.": "**Cheap Seats** is the  `free` place to start —\n   it runs on your Claude subscription.",
		`"cheap seats is the FREE place to start"`:                                      "**Cheap Seats** is the  `free` place to start",
		`Profound is "enterprise grade".`:                                               "Profound is “enterprise grade”.",
		"Cheap Seats is the free place to start...":                                     "**Cheap Seats** is the  `free` place to start",
		"Cheap Seats is the best free place to start.":                                  "", // reworded
		"Cheap Seats is the free place to start. Profound is enterprise grade.":         "", // joined
		"":    "",
		"   ": "",
	} {
		if got := Quote(answer, quote); got != want {
			t.Errorf("Quote(%q) = %q, want %q", quote, got, want)
		}
		if want != "" && !strings.Contains(answer, Quote(answer, quote)) {
			t.Errorf("%q: not the answer's own words", quote)
		}
	}
	if Quote("Use Cheap Seats. "+strings.Repeat("x", 700), "Use Cheap Seats. "+strings.Repeat("x", 700)) != "" {
		t.Error("a quote that is most of a long answer isn't evidence")
	}
	m := Mark("Try it. Use Cheap Seats. Done.", "Use Cheap Seats.")
	if m.Before != "Try it. " || m.Match != "Use Cheap Seats." || m.After != " Done." {
		t.Errorf("%+v", m)
	}
	if m := Mark("Text", "missing"); m.Before != "Text" || m.Match != "" {
		t.Errorf("%+v", m)
	}
	if !slices.Contains(schema["required"].([]string), "evidence") || !strings.Contains(System, "\nevidence:") {
		t.Error("evidence isn't required by the schema and described in System")
	}

	// Through Answer: the API grader's quote is checked, and an absent brand has none.
	var reply Grade
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text, _ := json.Marshal(reply)
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}})
	}))
	defer srv.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	cfg := &config.Config{Brand: config.Brand{Name: "Cheap Seats"}}
	reply = Grade{Verdict: Mentioned, Named: []string{"Cheap Seats"}, Evidence: "cheap seats is the free place to start"}
	if g, _, err := Answer(context.Background(), cfg, sets.Set{}, "q", answer); err != nil || g.Evidence != "**Cheap Seats** is the  `free` place to start" {
		t.Errorf("verified quote: %q %v", g.Evidence, err)
	}
	reply.Evidence = "Cheap Seats is the cheapest tool there is."
	if g, _, _ := Answer(context.Background(), cfg, sets.Set{}, "q", answer); g.Evidence != "" {
		t.Errorf("an invented quote was kept: %q", g.Evidence)
	}
	reply = Grade{Verdict: Absent, Evidence: "Profound is"}
	if g, _, _ := Answer(context.Background(), cfg, sets.Set{}, "q", answer); g.Evidence != "" {
		t.Errorf("absent has no evidence: %q", g.Evidence)
	}
}
