package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

// A Schema this Cheap Seats sends as a structured output, and which model sends it: Role is
// "grader" or "analyst", the config table whose model it goes to.
// Ordered is set by RegisterOrderedSchema.
type NamedSchema struct {
	Name, Role string
	Schema     map[string]any
	Ordered    bool
}

var (
	schemasMu sync.Mutex
	schemas   = map[string]NamedSchema{}
	digests   = map[[sha256.Size]byte]bool{} // of each registered schema's content
	ordered   = map[[sha256.Size]byte]bool{} // of each one sent in declared order (SchemaJSON)
)

// RegisterOrderedSchema is RegisterSchema for a schema whose objects send their properties in
// the order of their required lists rather than sorted (see SchemaJSON).
func RegisterOrderedSchema(name, role string, schema map[string]any) map[string]any {
	RegisterSchema(name, role, schema)
	d, _ := digest(schema)
	schemasMu.Lock()
	defer schemasMu.Unlock()
	ordered[d] = true
	s := schemas[name]
	s.Ordered = true
	schemas[name] = s
	return schema
}

// RegisterSchema records a structured output schema under a name, for the live check
// (cheapseats check -live), and returns it. Structured and answer.ClaudeStructured refuse a
// schema that wasn't registered (see Registered), so none can skip the check:
//
//	var schema = llm.RegisterSchema("grade", "grader", map[string]any{...})
func RegisterSchema(name, role string, schema map[string]any) map[string]any {
	if role != "grader" && role != "analyst" {
		panic(fmt.Sprintf("llm.RegisterSchema %s: role %q is not grader or analyst", name, role))
	}
	d, err := digest(schema)
	if err != nil {
		panic(fmt.Sprintf("llm.RegisterSchema %s: %v", name, err))
	}
	schemasMu.Lock()
	defer schemasMu.Unlock()
	if _, dup := schemas[name]; dup {
		panic("llm.RegisterSchema: " + name + " registered twice")
	}
	schemas[name] = NamedSchema{Name: name, Role: role, Schema: schema}
	digests[d] = true
	return schema
}

// Registered returns an error unless schema, as it would be sent now, is one RegisterSchema
// recorded. It compares the JSON, not the map, so a schema built per call passes if its content
// is registered, and a registered one changed after registering fails: the live check would have
// sent something else.
func Registered(schema map[string]any) error {
	d, err := digest(schema)
	if err != nil {
		return err
	}
	schemasMu.Lock()
	defer schemasMu.Unlock()
	if !digests[d] {
		return fmt.Errorf("structured output schema is not registered with llm.RegisterSchema, so cheapseats check -live does not send it to the API")
	}
	return nil
}

// digest is the SHA-256 of schema's JSON, which encoding/json writes with sorted map keys.
func digest(schema map[string]any) ([sha256.Size]byte, error) {
	b, err := json.Marshal(schema)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(b), nil
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
