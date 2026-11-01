package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
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

// The registry holds the app's schemas with the roles that send them. That every schema sent is
// registered is enforced where it is sent: llm.Structured and answer.ClaudeStructured refuse an
// unregistered one (their packages test that). This list catches a schema registered under the
// wrong role, which would send the live check to the wrong model.
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

// flag.Float64 accepts NaN and Inf; neither may stand in for a budget, since against NaN the
// spend never reaches it. Nothing is sent.
func TestLiveCheckRefusesABudgetThatIsNotAnAmount(t *testing.T) {
	bodies, cfg := liveFakeAPI(t, "", 1000)
	for _, b := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, -1} {
		if err := liveSchemaCheck(context.Background(), cfg, liveTestSchemas(), b, io.Discard); err == nil || !strings.Contains(err.Error(), "-live-budget") {
			t.Errorf("budget %v: %v", b, err)
		}
	}
	if len(*bodies) != 0 {
		t.Fatalf("%d requests sent without a budget", len(*bodies))
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

// Structured outputs write fields in the order the schema sends them. Sent alphabetically, the
// analyst's fixes item opened with check, often blank, and some replies were every field blank
// (run r-20261101-050336-9f0f23: 9 of 35 replays; 0 of 20 with insights and title first). So
// the fixes schema is registered ordered and sends each object's properties in its required
// order, which is pinned here. Only that order was measured: every other schema is sent exactly
// as encoding/json writes it, as before.
func TestLiveSchemasSendPropertiesInDeclaredOrder(t *testing.T) {
	sent := map[string]map[string][]string{}
	var orderedNames []string
	for _, s := range llm.Schemas() {
		js, err := llm.SchemaJSON(s.Schema)
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		plain, _ := json.Marshal(s.Schema)
		if !s.Ordered {
			if string(js) != string(plain) {
				t.Errorf("%s is not ordered but is sent as %s, not %s", s.Name, js, plain)
			}
			continue
		}
		orderedNames = append(orderedNames, s.Name)
		props, required := liveSchemaOrders(t, js)
		sent[s.Name] = props
		for path, names := range props {
			if !reflect.DeepEqual(names, required[path]) {
				t.Errorf("%s%s: properties sent as %v, required %v", s.Name, path, names, required[path])
			}
		}
		// The content is the schema as written; only the order changes.
		var a, b any
		json.Unmarshal(plain, &a)
		json.Unmarshal(js, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: sent %s, want the content of %s", s.Name, js, plain)
		}
	}
	if !reflect.DeepEqual(orderedNames, []string{"analysis.fixes"}) {
		t.Errorf("ordered schemas %v, want only analysis.fixes", orderedNames)
	}
	fixes := sent["analysis.fixes"]
	for path, want := range map[string][]string{
		"":                   {"insights", "fixes", "investigations", "market_findings"},
		".fixes[]":           {"title", "type", "target", "claim", "evidence", "prompts", "effort", "rank", "displaces", "done_when", "check", "decision"},
		".investigations[]":  {"title", "why", "check", "decision", "evidence", "prompts", "target"},
		".market_findings[]": {"title", "detail", "evidence", "prompts"},
	} {
		if !reflect.DeepEqual(fixes[path], want) {
			t.Errorf("analysis.fixes%s sent as %v, want %v", path, fixes[path], want)
		}
	}
}

// liveSchemaOrders reads schema JSON in the order it was written: for each object (by path, ""
// for the root, ".x" for property x, "[]" for items), its property names and its required list.
func liveSchemaOrders(t *testing.T, js []byte) (props, required map[string][]string) {
	t.Helper()
	props, required = map[string][]string{}, map[string][]string{}
	dec := json.NewDecoder(strings.NewReader(string(js)))
	var value func(path string)
	value = func(path string) {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		switch tok {
		case json.Delim('['):
			for dec.More() {
				value(path)
			}
			dec.Token()
		case json.Delim('{'):
			for dec.More() {
				k, _ := dec.Token()
				switch key := k.(string); key {
				case "properties":
					dec.Token()
					props[path] = []string{}
					for dec.More() {
						name, _ := dec.Token()
						props[path] = append(props[path], name.(string))
						value(path + "." + name.(string))
					}
					dec.Token()
				case "required":
					var r []string
					if err := dec.Decode(&r); err != nil {
						t.Fatal(err)
					}
					required[path] = r
				case "items":
					value(path + "[]")
				default:
					value(path + "." + key)
				}
			}
			dec.Token()
		}
	}
	value("")
	return props, required
}
