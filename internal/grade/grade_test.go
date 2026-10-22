package grade

import (
	"context"
	"encoding/json"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/sets"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With provider = "claude-code" the grader runs on the claude CLI: no API key, no API call, no
// cost, and the same rules after it (here, absent for an answer that never names the brand).
func TestGradingOnTheClaudeCLI(t *testing.T) {
	dir := t.TempDir()
	out := `{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"verdict":"recommended","position":1,"top_pick":null,"named":["Gauge"],"reasoning":"r","why_not_top":null,"objections":[],"other_product":false}}`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\ncat > /dev/null\nprintf '%s\\n' '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	old := llm.AnthropicURL
	llm.AnthropicURL = "http://127.0.0.1:1/never-called"
	defer func() { llm.AnthropicURL = old }()
	cfg := &config.Config{Brand: config.Brand{Name: "Popcorn"}, Grader: config.Model{Model: "claude-haiku-5-5", Provider: config.ClaudeCode}}
	g, cost, err := Answer(context.Background(), cfg, sets.Set{}, "Is Gauge any good?", "Yes, Gauge is a credible choice.")
	if err != nil || cost != 0 || g.Verdict != Absent || g.Position != nil {
		t.Fatalf("%+v cost=%v %v", g, cost, err)
	}
	g, _, err = Answer(context.Background(), cfg, sets.Set{}, "Popcorn or Gauge?", "Use Popcorn.")
	if err != nil || g.Verdict != Recommended {
		t.Fatalf("a named brand keeps the CLI's verdict: %+v %v", g, err)
	}
}

func TestGraderSeesTheAnswerAndBrand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for _, want := range []string{"Brand: Popcorn", "About the brand: workstations", "Use Popcorn."} {
			if !strings.Contains(string(body), want) {
				t.Errorf("grader request lacks %q", want)
			}
		}
		top := "Popcorn"
		text, _ := json.Marshal(Grade{Verdict: Mentioned, TopPick: &top, Named: []string{"Popcorn"}})
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}})
	}))
	defer srv.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	cfg := &config.Config{Brand: config.Brand{Name: "Popcorn", About: "workstations"}}
	g, _, e := Answer(context.Background(), cfg, sets.Set{}, "public question", "Use Popcorn.")
	if e != nil || g.Verdict != Recommended {
		t.Fatalf("a top pick must be recommended: %+v %v", g, e)
	}
}

// On a question about a competitor the model graded that competitor as the brand: "recommended" at
// position 1 for an answer that never names Popcorn (run r-20261021-200546-84c081, result 236).
func TestAnAnswerThatNeverNamesTheBrandIsAbsent(t *testing.T) {
	brand := config.Brand{Name: "Popcorn", Aliases: []string{"popcorn.zone", "metcalfc/popcorn"}}
	pos, top := 1, "Popcorn"
	g := Grade{Verdict: Recommended, Position: &pos, TopPick: &top, Objections: []string{"young company"}}
	if namesBrand("Yes, Gauge is a credible choice. Alternatives: Peec AI, Profound.", brand) {
		t.Fatal("matched a brand that isn't there")
	}
	g.notNamed(brand.Name)
	g.normalize(brand.Name)
	if g.Verdict != Absent || g.Position != nil || g.TopPick != nil || len(g.Objections) > 0 {
		t.Fatalf("%+v", g)
	}
	for _, answer := range []string{"Try POPCORN for this.", "See popcorn.zone/docs.", "github.com/metcalfc/popcorn is open source."} {
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

// Another product can share the brand's name: answers about trypopcorn.ai, Reclaim Protocol's
// popcorn-oss and Bad Popcorn were graded "mentioned" for Popcorn (run r-20261022-043102-ebd82f,
// results 671, 695, 699). The grader says so in other_product, and that makes the grade absent;
// an answer that can't find the brand, without taking it for something else, still names it.
func TestTheNameUsedForAnotherProductIsAbsent(t *testing.T) {
	brand := config.Brand{Name: "Popcorn", Aliases: []string{"popcorn.zone"}, About: "an open-source, self-hosted AI visibility tracker"}
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
		g, _, err := Answer(context.Background(), &config.Config{Brand: brand}, sets.Set{}, "Popcorn vs Profound?", answer)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}

	reply = Grade{Verdict: Mentioned, Position: &pos, Named: []string{"Profound", "Popcorn AI"}, Objections: []string{"WhatsApp agents"}, OtherProduct: true}
	if g := ask("I couldn't verify the Popcorn you mean; trypopcorn.ai sells WhatsApp sales agents. Pick Profound."); g.Verdict != Absent || g.Position != nil || len(g.Objections) > 0 || !g.OtherProduct {
		t.Errorf("another product's name must be absent: %+v", g)
	}

	reply = Grade{Verdict: Mentioned, Position: &pos, Named: []string{"Profound", "Popcorn"}, Objections: []string{"could not verify"}}
	if g := ask("I couldn't find a product called Popcorn, so pick Profound."); g.Verdict != Mentioned || g.Position == nil {
		t.Errorf("not finding the brand still names it: %+v", g)
	}
}

func TestAbsentBrandDoesNotGetInventedObjectionAndTiesStayUnpicked(t *testing.T) {
	why := "Preferred another tool"
	g := Grade{Verdict: Absent, WhyNotTop: &why, Objections: []string{"unsupported"}}
	g.normalize("Popcorn")
	if g.WhyNotTop != nil || len(g.Objections) > 0 {
		t.Fatal(g)
	}
	g = Grade{Verdict: Recommended}
	g.normalize("Popcorn")
	if g.TopPick != nil {
		t.Fatal("tie forced into a top pick")
	}
}
