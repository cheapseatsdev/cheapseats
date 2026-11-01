package grade

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

// Every schema this package sends must be one Anthropic structured outputs accept. The claude CLI
// takes keywords the API answers with an HTTP 400, so tests through that path never see them.
func TestGradeSchemasFitStructuredOutputs(t *testing.T) {
	for name, s := range map[string]map[string]any{
		"schema": schema,
	} {
		if err := llm.CheckSchema(s); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The grader writes its fields in the order the schema is sent, and reasoning before the verdict
// agreed with the golden labels more often than sorted (evidence first) or verdict first. A
// schema registered plainly is sent sorted, so this also fails if grade stops being ordered.
func TestGradeSchemaSendsReasoningFirst(t *testing.T) {
	js, err := llm.SchemaJSON(schema)
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(js, &sent); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(sent.Properties))
	var got []string
	if _, err := dec.Token(); err != nil { // {
		t.Fatal(err)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"reasoning", "evidence", "verdict", "position", "top_pick", "named", "why_not_top", "objections", "other_product", "tone", "praise"}
	if !slices.Equal(got, want) {
		t.Errorf("grade schema sends properties in order\n%v\nwant\n%v", got, want)
	}
}
