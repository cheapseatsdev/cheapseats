package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

// Every schema the app sends is registered, so the live check covers each. A schema declared
// without llm.RegisterSchema drops out of this list.
func TestLiveEverySchemaIsRegistered(t *testing.T) {
	var got []string
	for _, s := range llm.Schemas() {
		got = append(got, s.Name+"/"+s.Role)
	}
	want := []string{"analysis.fixes/analyst", "analysis.review/analyst", "app.facts/grader", "grade/grader", "opinion.themes/grader", "suggest.edits/analyst", "suggest.prompts/analyst"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registered schemas %v, want %v", got, want)
	}
}

// liveFakeAPI answers the Messages API: refuse names a schema property whose presence earns a
// 400, as the real API refuses an unsupported keyword; usage is what each accepted reply bills.
func liveFakeAPI(t *testing.T, refuse string, inputTokens int) (*[]map[string]any, *config.Config) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		if refuse != "" && strings.Contains(string(b), refuse) {
			w.WriteHeader(400)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"For 'array' type, property 'maxItems' is not supported"}}`)
			return
		}
		io.WriteString(w, `{"stop_reason":"max_tokens","content":[{"type":"text","text":"{"}],"usage":{"input_tokens":`+itoa(inputTokens)+`,"output_tokens":16}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(config.AnthropicBaseURLVar, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test")
	cfg := &config.Config{
		Grader:  config.Model{Model: "grader-model"},
		Analyst: config.Model{Model: "analyst-model"},
		Pricing: map[string]config.Pricing{"grader-model": {Input: 1, Output: 5}, "analyst-model": {Input: 5, Output: 25}},
	}
	return &bodies, cfg
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func liveTestSchemas() []llm.NamedSchema {
	obj := func(prop string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{prop},
			"properties": map[string]any{prop: map[string]any{"type": "string"}}}
	}
	return []llm.NamedSchema{
		{Name: "a", Role: "grader", Schema: obj("verdict")},
		{Name: "b", Role: "analyst", Schema: obj("fixes")},
		{Name: "c", Role: "analyst", Schema: obj("bad_one")},
	}
}

// Each schema goes once, to its role's model, as the structured output with a small max_tokens;
// a 400 for one of them fails the check and names it.
func TestLiveCheckSendsEachSchemaAndFailsOnARefusal(t *testing.T) {
	bodies, cfg := liveFakeAPI(t, "bad_one", 1000)
	var out strings.Builder
	err := liveSchemaCheck(context.Background(), cfg, liveTestSchemas(), 1, &out)
	if err == nil || !strings.Contains(err.Error(), "c: the API refused the schema") {
		t.Fatalf("want c refused, got %v\n%s", err, out.String())
	}
	if lines := strings.Split(err.Error(), "\n"); len(lines) != 1 {
		t.Fatalf("accepted schemas reported as problems: %v", err)
	}
	if len(*bodies) != 3 {
		t.Fatalf("%d requests, want one per schema", len(*bodies))
	}
	wantModel := []string{"grader-model", "analyst-model", "analyst-model"}
	for i, b := range *bodies {
		if b["model"] != wantModel[i] {
			t.Errorf("schema %d went to %v, want %s", i, b["model"], wantModel[i])
		}
		if mt, _ := b["max_tokens"].(float64); mt <= 0 || mt > liveMaxTokens {
			t.Errorf("schema %d: max_tokens %v", i, b["max_tokens"])
		}
		sent := llm.Map(llm.Map(llm.Map(b["output_config"])["format"])["schema"])
		want, _ := json.Marshal(liveTestSchemas()[i].Schema)
		got, _ := json.Marshal(sent)
		if string(got) != string(want) {
			t.Errorf("schema %d sent as %s, want %s", i, got, want)
		}
	}
}

func TestLiveCheckPassesWhenAllAreAccepted(t *testing.T) {
	_, cfg := liveFakeAPI(t, "", 1000)
	var out strings.Builder
	if err := liveSchemaCheck(context.Background(), cfg, liveTestSchemas(), 1, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "3 of 3 schemas sent") {
		t.Fatalf("summary missing:\n%s", out.String())
	}
}

// Spend is checked before each request: once the budget is used up, nothing more is sent, and
// the check fails rather than passing with schemas unchecked.
func TestLiveCheckStopsAtItsBudget(t *testing.T) {
	// 100k input tokens at $1/M is $0.10 per grader request.
	bodies, cfg := liveFakeAPI(t, "", 100000)
	err := liveSchemaCheck(context.Background(), cfg, liveTestSchemas(), 0.05, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("want a budget stop, got %v", err)
	}
	if len(*bodies) != 1 {
		t.Fatalf("%d requests sent past the budget, want 1", len(*bodies))
	}
}

// A role on the claude CLI sends nothing through the API, so it is skipped, with a note; a config
// whose every role is on the CLI fails rather than passing having checked nothing.
func TestLiveCheckSkipsCLIRoles(t *testing.T) {
	bodies, cfg := liveFakeAPI(t, "", 1000)
	cfg.Analyst.Provider = config.ClaudeCode
	var out strings.Builder
	if err := liveSchemaCheck(context.Background(), cfg, liveTestSchemas(), 1, &out); err != nil {
		t.Fatal(err)
	}
	if len(*bodies) != 1 || !strings.Contains(out.String(), "b: skipped") {
		t.Fatalf("%d requests, want only the grader's:\n%s", len(*bodies), out.String())
	}
	cfg.Grader.Provider = config.ClaudeCode
	if err := liveSchemaCheck(context.Background(), cfg, liveTestSchemas(), 1, io.Discard); err == nil {
		t.Fatal("passed with nothing checked")
	}
}
