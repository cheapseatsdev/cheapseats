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
	"strings"
	"testing"
)

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
