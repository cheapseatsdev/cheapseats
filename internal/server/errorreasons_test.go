package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// errorReasonsFakeAnthropic answers every prompt without searching, and grades every answer.
func errorReasonsFakeAnthropic(t *testing.T) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := "Use Docker Sandboxes or Cheap Seats."
		if strings.Contains(string(body), "output_config") {
			text = `{"verdict":"mentioned","named":["Docker Sandboxes","Cheap Seats"],"reasoning":"r"}`
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	t.Cleanup(api.Close)
	old := llm.AnthropicURL
	llm.AnthropicURL = api.URL
	t.Cleanup(func() { llm.AnthropicURL = old })
	t.Setenv("ANTHROPIC_API_KEY", "fake-review-provider-key-123456")
	t.Setenv("OPENAI_API_KEY", "")
}

// A search-track answerer that stops searching fails every search answer; the run page says so
// beside the error count, rather than a bare number like a timeout's.
func TestRunPageSaysWhyAnswersFailed(t *testing.T) {
	errorReasonsFakeAnthropic(t)
	cfg := testConfig(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := newApp(t, cfg, db)
	set, _ := a.Set("ai-search-visibility")
	r, err := a.Runs.Create(context.Background(), set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	a.Runs.Execute(context.Background(), r.ID) // its search answers fail; the run still finishes
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/runs/"+r.ID, nil))
	n := len(set.Prompts)
	want := fmt.Sprintf(`<td>search</td><td class="num">–</td><td class="num">–</td><td class="num">–</td><td class="num">%d <span class="muted small">(%d answered without searching)</span></td>`, n, n)
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("run page: want %q", want)
	}
}
