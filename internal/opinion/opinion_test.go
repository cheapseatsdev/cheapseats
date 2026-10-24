package opinion

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// The quote in the string's own case, when it's there word for word; lowercasing a rune can
// change its size (İ), which must not slice past the string's end or into the middle of a rune.
func TestVerbatim(t *testing.T) {
	for _, c := range []struct {
		quote string
		texts []string
		want  string
	}{
		{"cheap  seats IS cheap", []string{"Pricing is unclear", "Cheap Seats is cheap"}, "Cheap Seats is cheap"},
		{"unclear İ", []string{"Pricing is unclear i"}, "unclear İ"},       // İ lowercases to i: a byte past the end
		{"unclear i", []string{"Pricing is unclear İ"}, "unclear i"},       // the string shrank: the quote as given
		{"İstanbul", []string{"İstanbul İstanbul"}, "İstanbul"},            // sizes balance by chance: still the quote
		{"not here", []string{"Pricing is unclear", "Too new"}, "Too new"}, // the shortest string
		{"", []string{"Pricing is unclear"}, "Pricing is unclear"},
	} {
		if got := verbatim(c.quote, c.texts); got != c.want {
			t.Errorf("verbatim(%q, %q) = %q, want %q", c.quote, c.texts, got, c.want)
		}
	}
}

func TestSentiment(t *testing.T) {
	g := func(v grade.Verdict, tone grade.Tone, praise, objections []string) *grade.Grade {
		return &grade.Grade{Verdict: v, Tone: tone, Praise: praise, Objections: objections}
	}
	s := Summarize([]*grade.Grade{
		g(grade.Recommended, grade.Positive, []string{"Open source", "open source."}, nil), // one answer, counted once
		g(grade.Mentioned, grade.Positive, []string{"open source", "cheap"}, []string{"early stage"}),
		g(grade.Mentioned, grade.Negative, nil, []string{"Early stage", "no SSO"}),
		g(grade.Mentioned, grade.Mixed, nil, nil),
		g(grade.Mentioned, "", nil, nil), // graded before tone: named, but no tone
		g(grade.Absent, "", nil, nil),
		nil,
	})
	if s.Named != 5 || s.Toned != 4 || s.NetText() != "+25" {
		t.Fatalf("named %d toned %d net %s", s.Named, s.Toned, s.NetText())
	}
	if s.Praise[0] != (Count{Text: "Open source", N: 2}) || s.Objections[0] != (Count{Text: "early stage", N: 2}) || len(s.Objections) != 2 {
		t.Fatalf("%+v %+v", s.Praise, s.Objections)
	}
	if len(s.Tones) != 4 || s.Tones[0].Tone != grade.Positive || s.Tones[0].Share != 0.5 {
		t.Fatalf("%+v", s.Tones)
	}
	if s := Summarize([]*grade.Grade{g(grade.Absent, "", nil, nil)}); s.Net != nil || s.NetText() != "n/a" {
		t.Fatalf("nothing to go on is n/a, not zero: %+v", s)
	}
	// Wordings mapped onto one (app.PhraseClusters) count as one, under the mapped wording.
	clustered := []*grade.Grade{
		g(grade.Mentioned, "", []string{"Easy setup"}, []string{"Coverage unverified"}),
		g(grade.Mentioned, "", []string{"easy setup", "setup is easy and quick"}, []string{"coverage of two assistants unverified"}), // one answer, counted once
		g(grade.Mentioned, "", []string{"Setup is easy and quick", "cheap"}, []string{"no free tier"}),
	}
	s = SummarizeWith(clustered, Options{
		Praise:     map[string]string{"setup is easy and quick": "Easy setup"},
		Objections: map[string]string{"coverage of two assistants unverified": "Coverage unverified"},
	})
	if len(s.Praise) != 2 || s.Praise[0] != (Count{Text: "Easy setup", N: 3}) || len(s.Objections) != 2 || s.Objections[0] != (Count{Text: "Coverage unverified", N: 2}) {
		t.Fatalf("%+v %+v", s.Praise, s.Objections)
	}
	// A wording mapped to its own first wording counts under that wording.
	if s := SummarizeWith(clustered[2:], Options{Praise: map[string]string{"cheap": "Cheap"}}); len(s.Praise) != 2 || s.Praise[1] != (Count{Text: "Cheap", N: 1}) {
		t.Fatalf("%+v", s.Praise)
	}
	if s := Summarize(clustered); len(s.Praise) != 3 || len(s.Objections) != 3 {
		t.Fatalf("without the maps, as before: %+v %+v", s.Praise, s.Objections)
	}
	b, _ := json.Marshal(Summarize(nil))
	if !strings.Contains(string(b), `"net":null`) || !strings.Contains(string(b), `"praise":[]`) {
		t.Fatal(string(b))
	}
}

func answers() []Answer {
	why := "The answer preferred Profound for its enterprise reporting and SOC 2 report."
	why2 := "Peec AI was picked as cheaper to start with."
	return []Answer{
		{1, &grade.Grade{Verdict: grade.Mentioned, Objections: []string{"no SOC 2"}, WhyNotTop: &why, Named: []string{"Profound", "Cheap Seats"}}},
		{2, &grade.Grade{Verdict: grade.Mentioned, Objections: []string{"No SOC 2", "early stage"}, WhyNotTop: &why2, Named: []string{"Peec AI", "Cheap Seats"}}},
		{3, &grade.Grade{Verdict: grade.Recommended, Praise: []string{"open source", "runs locally"}}},
		{4, &grade.Grade{Verdict: grade.Absent}},
	}
}

// The model's themes are checked: answers are counted from the strings a theme names, a quote
// that isn't in them is replaced by one that is, a beneficiary has to be a known product, and an
// empty or unknown theme is dropped.
func TestThemesAreValidated(t *testing.T) {
	reply := `{"themes":[
		{"name":"No SOC 2 report","kind":"weakness","items":[1,1,99],"beneficiary":"profound","quote":"no soc 2"},
		{"name":"Self-hosted","kind":"strength","items":[5,6],"beneficiary":"Profound","quote":"praised as the best tool on the market"},
		{"name":"Enterprise reporting","kind":"competitive","items":[2],"beneficiary":"Acme Unknown","quote":"enterprise reporting"},
		{"name":"","kind":"weakness","items":[2],"beneficiary":null,"quote":"x"},
		{"name":"Bogus","kind":"opinion","items":[2],"beneficiary":null,"quote":"x"},
		{"name":"Nothing","kind":"weakness","items":[42],"beneficiary":null,"quote":"x"}]}`
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": reply}}, "usage": map[string]any{"input_tokens": 1000, "output_tokens": 200}})
	}))
	defer srv.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	cfg := &config.Config{Brand: config.Brand{Name: "Cheap Seats"}, Grader: config.Model{Model: "h"}, Pricing: map[string]config.Pricing{"h": {Input: 1, Output: 5}}}
	set := sets.Set{Competitors: []sets.Competitor{{Name: "Profound", Aliases: []string{"tryprofound"}}}}
	got, err := Group(context.Background(), cfg, set, answers())
	if err != nil {
		t.Fatal(err)
	}
	// "no SOC 2" twice is one string from two answers.
	if !strings.Contains(sent, `weakness ×2: no SOC 2`) || !strings.Contains(sent, "Competitors: Profound;") {
		t.Errorf("request: %s", sent)
	}
	if got.Inputs != 6 || got.From != 3 || got.CostUSD != 0.002 || len(got.Themes) != 3 {
		t.Fatalf("%+v", got)
	}
	soc := got.Themes[0]
	if soc.Name != "No SOC 2 report" || soc.Answers != 2 || soc.Beneficiary != "Profound" || soc.Quote != "no SOC 2" {
		t.Errorf("%+v", soc)
	}
	self := got.Themes[1]
	if self.Answers != 1 || self.Beneficiary != "" || self.Quote != "open source" {
		t.Errorf("a strength helps no one, and a quote not in its strings is replaced: %+v", self)
	}
	if ent := got.Themes[2]; ent.Beneficiary != "" || ent.Quote != "enterprise reporting" {
		t.Errorf("a product nobody named isn't a beneficiary: %+v", ent)
	}
}

// Too little to group makes no call; on the claude-code provider it's the CLI, at no cost.
func TestThemesSkipAndCLI(t *testing.T) {
	calls := 0
	old := structuredCLI
	defer func() { structuredCLI = old }()
	structuredCLI = func(_ context.Context, model, system, prompt string, schema map[string]any, out any) error {
		calls++
		if model != "claude-haiku" || system != ThemeSystem {
			t.Errorf("%s %s", model, system)
		}
		return json.Unmarshal([]byte(`{"themes":[{"name":"Open source","kind":"strength","items":[5],"beneficiary":null,"quote":"open source"}]}`), out)
	}
	cfg := &config.Config{Brand: config.Brand{Name: "Cheap Seats"}, Grader: config.Model{Model: "claude-haiku", Provider: config.ClaudeCode}}
	few, err := Group(context.Background(), cfg, sets.Set{}, answers()[2:])
	if err != nil || calls != 0 || few.Skipped == "" || few.Themes == nil {
		t.Fatalf("%+v %v calls=%d", few, err, calls)
	}
	got, err := Group(context.Background(), cfg, sets.Set{}, answers())
	if err != nil || calls != 1 || got.CostUSD != 0 || len(got.Themes) != 1 || got.Themes[0].Answers != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestClipStaysVerbatim(t *testing.T) {
	s := strings.Repeat("word ", 100)
	c := clip(s, 50)
	if len([]rune(c)) > 50 || !strings.HasPrefix(s, c) || strings.HasSuffix(c, " ") {
		t.Fatalf("%q", c)
	}
}
