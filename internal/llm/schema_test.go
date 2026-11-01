package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

func TestCheckSchemaRefusesWhatTheAPIRefuses(t *testing.T) {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": []string{}, "additionalProperties": false}
	}
	for _, tc := range []struct {
		name   string
		schema map[string]any
		want   string
	}{
		// The v0.6.0 analyst schema: HTTP 400 "For 'array' type, property 'maxItems' is not supported".
		{"maxItems", obj(map[string]any{"fixes": map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "string"}}}), `schema.fixes uses "maxItems"`},
		{"minItems above one", obj(map[string]any{"xs": map[string]any{"type": "array", "minItems": 2, "items": map[string]any{"type": "string"}}}), "minItems to 2"},
		{"minimum", obj(map[string]any{"n": map[string]any{"type": "integer", "minimum": 1}}), `"minimum"`},
		{"maxLength", obj(map[string]any{"s": map[string]any{"type": "string", "maxLength": 9}}), `"maxLength"`},
		{"open object", map[string]any{"type": "object", "properties": map[string]any{}}, "without additionalProperties: false"},
		{"nested open object", obj(map[string]any{"xs": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{}}}}), "schema.xs[] is an object without"},
		{"format", obj(map[string]any{"s": map[string]any{"type": "string", "format": "color"}}), "format color"},
	} {
		err := CheckSchema(tc.schema)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
	// Property names are not keywords: a field called "pattern" or "minimum" is fine.
	ok := obj(map[string]any{
		"pattern": map[string]any{"type": "string"}, "minimum": map[string]any{"type": []string{"integer", "null"}},
		"tags": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "enum": []string{"a", "b"}}},
	})
	if err := CheckSchema(ok); err != nil {
		t.Fatalf("supported schema refused: %v", err)
	}
}

// schemaTestOrderedShape is written with typed lists and maps, which an ordered schema must order
// all the same.
func schemaTestOrderedShape() map[string]any {
	str := map[string]any{"type": "string"}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"z", "a"},
		"properties": map[string]any{
			"a": str, "m": str,
			"z": map[string]any{"anyOf": []map[string]any{{"type": "null"}, {"type": "object", "additionalProperties": false,
				"required": []string{"y", "b"}, "properties": map[string]map[string]any{"b": str, "y": str}}}},
		}}
}

var schemaTestOrdered = RegisterOrderedSchema("test.ordered", "grader", schemaTestOrderedShape())

// An ordered schema's properties go in required's order, at every depth, with any property not
// required after them, sorted; other keys stay sorted. The request body carries that order. A
// schema not registered as ordered is sent exactly as encoding/json writes it.
func TestSchemaJSONSendsPropertiesInRequiredOrder(t *testing.T) {
	want := `{"additionalProperties":false,"properties":{"z":{"anyOf":[{"type":"null"},{"additionalProperties":false,"properties":{"y":{"type":"string"},"b":{"type":"string"}},"required":["y","b"],"type":"object"}]},"a":{"type":"string"},"m":{"type":"string"}},"required":["z","a"],"type":"object"}`
	got, err := SchemaJSON(schemaTestOrdered)
	if err != nil || string(got) != want {
		t.Fatalf("SchemaJSON = %s, %v\nwant %s", got, err, want)
	}
	body, err := json.Marshal(structuredBody("m", "s", "u", 10, schemaTestOrdered))
	if err != nil || !strings.Contains(string(body), `"schema":`+want) {
		t.Fatalf("request body %s, %v: schema not in declared order", body, err)
	}

	plain := schemaTestOrderedShape()
	plain["description"] = "not registered as ordered"
	got, err = SchemaJSON(plain)
	if js, _ := json.Marshal(plain); err != nil || string(got) != string(js) {
		t.Fatalf("unordered schema sent as %s, %v; want %s", got, err, js)
	}
}

func TestStructuredChecksTheSchemaBeforeSending(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()
	old := AnthropicURL
	AnthropicURL = srv.URL
	t.Cleanup(func() { AnthropicURL = old })
	t.Setenv("ANTHROPIC_API_KEY", "test")
	t.Setenv(config.AnthropicBaseURLVar, "")
	bad := map[string]any{"type": "array", "maxItems": 3, "items": map[string]any{"type": "string"}}
	var out []string
	if _, err := Structured(context.Background(), "m", "s", "u", 10, bad, &out); err == nil || !strings.Contains(err.Error(), "maxItems") {
		t.Fatalf("bad schema: %v", err)
	}
	if calls != 0 {
		t.Fatalf("sent %d requests with a schema the API refuses", calls)
	}
}

var schemaTestRegistered = RegisterSchema("test.registered", "grader", map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"a"},
	"properties": map[string]any{"a": map[string]any{"type": "string"}},
})

// A schema that skipped RegisterSchema would skip cheapseats check -live, so Structured refuses
// it before sending. A registered schema's content is what counts: a copy passes, and a
// registered schema changed after registering fails, since the check sent the old content.
func TestStructuredRefusesAnUnregisteredSchema(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":"{\"a\":\"x\"}"}]}`)
	}))
	defer srv.Close()
	old := AnthropicURL
	AnthropicURL = srv.URL
	t.Cleanup(func() { AnthropicURL = old })
	t.Setenv("ANTHROPIC_API_KEY", "test")
	t.Setenv(config.AnthropicBaseURLVar, "")
	var out map[string]any

	unregistered := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"b"},
		"properties": map[string]any{"b": map[string]any{"type": "string"}}}
	if _, err := Structured(context.Background(), "m", "s", "u", 10, unregistered, &out); err == nil || !strings.Contains(err.Error(), "RegisterSchema") {
		t.Fatalf("unregistered schema: %v", err)
	}
	if calls != 0 {
		t.Fatalf("sent %d requests with an unregistered schema", calls)
	}

	copied := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"a"},
		"properties": map[string]any{"a": map[string]any{"type": "string"}}}
	if _, err := Structured(context.Background(), "m", "s", "u", 10, copied, &out); err != nil || calls != 1 {
		t.Fatalf("a copy of a registered schema: %v, %d requests", err, calls)
	}

	copied["required"] = []string{}
	if err := Registered(copied); err == nil {
		t.Fatal("a schema changed from the registered one passed")
	}
}
