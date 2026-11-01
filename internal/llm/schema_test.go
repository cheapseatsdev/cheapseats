package llm

import (
	"context"
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
