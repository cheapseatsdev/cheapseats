package llm

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

// A Schema this Cheap Seats sends as a structured output, and which model sends it: Role is
// "grader" or "analyst", the config table whose model it goes to.
type NamedSchema struct {
	Name, Role string
	Schema     map[string]any
}

var (
	schemasMu sync.Mutex
	schemas   = map[string]NamedSchema{}
)

// RegisterSchema records a structured output schema under a name, for the live check
// (cheapseats check -live), and returns it. Declare every schema passed to Structured or the
// claude CLI through it, so none is left out of the check:
//
//	var schema = llm.RegisterSchema("grade", "grader", map[string]any{...})
func RegisterSchema(name, role string, schema map[string]any) map[string]any {
	if role != "grader" && role != "analyst" {
		panic(fmt.Sprintf("llm.RegisterSchema %s: role %q is not grader or analyst", name, role))
	}
	schemasMu.Lock()
	defer schemasMu.Unlock()
	if _, dup := schemas[name]; dup {
		panic("llm.RegisterSchema: " + name + " registered twice")
	}
	schemas[name] = NamedSchema{Name: name, Role: role, Schema: schema}
	return schema
}

// Schemas are the registered schemas, by name.
func Schemas() []NamedSchema {
	schemasMu.Lock()
	defer schemasMu.Unlock()
	out := make([]NamedSchema, 0, len(schemas))
	for _, s := range schemas {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ProbeSchema sends schema to model once, as Structured would, with a one-line prompt and
// maxTokens, to learn whether the API takes it. A schema the API can't take is refused with an
// HTTP 400 before anything is generated; one it takes costs the schema's input tokens and at
// most maxTokens of output. A reply that stops at maxTokens is a pass: only acceptance is
// tested. It skips CheckSchema, so it tests the API rather than this package's idea of it.
func ProbeSchema(ctx context.Context, model string, schema map[string]any, maxTokens int) (config.Usage, error) {
	resp, err := Anthropic(ctx, structuredBody(model, "You are testing a JSON schema. Keep the reply as short as the schema allows.", "Reply with the smallest JSON that fits the schema.", maxTokens, schema))
	if err != nil {
		return config.Usage{}, err
	}
	return Usage(resp, false), nil
}
