package answer

import (
	"context"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/llm"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFailedSearchCannotQualifyAsSearchTrack(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"content":[{"type":"server_tool_use","id":"s","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"s","content":{"type":"web_search_tool_result_error","error_code":"unavailable"}},{"type":"text","text":"An answer from memory."}]}`))
	}))
	defer srv.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	if _, e := Ask(context.Background(), config.Answerer{Provider: "anthropic", Model: "fake"}, Search, System(""), "q"); e == nil {
		t.Fatal("failed search accepted")
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestCompletedEmptySearchIsValidAndOpenAIQueriesPreserved(t *testing.T) {
	a := ParseAnthropic(decode(t, `{"content":[{"type":"server_tool_use","id":"s","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"s","content":[]}]}`))
	if !a.Searched() {
		t.Fatal("empty successful search rejected")
	}
	a = ParseOpenAI(decode(t, `{"output":[{"type":"web_search_call","status":"failed","action":{"type":"search","queries":["one","two"]}}]}`))
	if a.Searched() || a.Searches[0].Query != "one | two" {
		t.Fatal(a)
	}
}
