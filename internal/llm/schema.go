package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Structured outputs accept a subset of JSON Schema and answer anything else with an HTTP 400
// (https://platform.claude.com/docs/en/build-with-claude/structured-outputs, "JSON Schema
// limitations"). The claude CLI's --json-schema is looser, so a schema that works on the
// subscription can still fail on the API. Limits such as a list's length are stated in the prompt
// and enforced after parsing instead.
var schemaKeywords = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true, "items": true,
	"enum": true, "const": true, "anyOf": true, "allOf": true, "$ref": true, "$defs": true,
	"definitions": true, "default": true, "description": true, "format": true, "minItems": true,
	"pattern": true,
}

var schemaFormats = map[string]bool{
	"date-time": true, "time": true, "date": true, "duration": true, "email": true,
	"hostname": true, "uri": true, "ipv4": true, "ipv6": true, "uuid": true,
}

// The documented per-request totals: properties left out of required, and properties whose type
// is a list or an anyOf.
const (
	maxOptionalProperties = 24
	maxUnionProperties    = 16
)

// CheckSchema reports what in schema structured outputs would refuse, so a schema the API can't
// take fails before a request, on either provider path.
func CheckSchema(schema map[string]any) error {
	b, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	var root any
	if err := json.Unmarshal(b, &root); err != nil {
		return err
	}
	c := schemaCheck{}
	c.node("schema", root)
	if c.optional > maxOptionalProperties {
		c.add("schema", "has %d optional properties; at most %d are supported", c.optional, maxOptionalProperties)
	}
	if c.unions > maxUnionProperties {
		c.add("schema", "has %d union-typed properties; at most %d are supported", c.unions, maxUnionProperties)
	}
	if len(c.problems) == 0 {
		return nil
	}
	return errors.New("structured output schema: " + strings.Join(c.problems, "; "))
}

type schemaCheck struct {
	problems         []string
	optional, unions int
}

func (c *schemaCheck) add(path, format string, args ...any) {
	c.problems = append(c.problems, path+" "+fmt.Sprintf(format, args...))
}

func (c *schemaCheck) node(path string, v any) {
	m, ok := v.(map[string]any)
	if !ok {
		c.add(path, "is not a schema object")
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !schemaKeywords[k] {
			c.add(path, "uses %q, which structured outputs don't support", k)
		}
	}
	if isObject(m["type"]) {
		if ap, ok := m["additionalProperties"].(bool); !ok || ap {
			c.add(path, "is an object without additionalProperties: false")
		}
	} else if ap, ok := m["additionalProperties"]; ok && ap != false {
		c.add(path, "sets additionalProperties to something other than false")
	}
	if n, ok := m["minItems"]; ok && n != 0.0 && n != 1.0 {
		c.add(path, "sets minItems to %v; only 0 and 1 are supported", n)
	}
	if f, ok := m["format"]; ok && !schemaFormats[Str(f)] {
		c.add(path, "uses format %v, which structured outputs don't support", f)
	}
	for _, e := range List(m["enum"]) {
		switch e.(type) {
		case map[string]any, []any:
			c.add(path, "has a complex value in enum")
		}
	}
	required := map[string]bool{}
	for _, r := range List(m["required"]) {
		required[Str(r)] = true
	}
	props := Map(m["properties"])
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := Map(props[name])
		if !required[name] {
			c.optional++
		}
		if _, list := p["type"].([]any); list || p["anyOf"] != nil {
			c.unions++
		}
		c.node(path+"."+name, props[name])
	}
	if items, ok := m["items"]; ok {
		c.node(path+"[]", items)
	}
	for _, k := range []string{"anyOf", "allOf"} {
		for i, s := range List(m[k]) {
			c.node(fmt.Sprintf("%s.%s[%d]", path, k, i), s)
		}
	}
	for _, k := range []string{"$defs", "definitions"} {
		defs := Map(m[k])
		for name, s := range defs {
			c.node(path+"."+k+"."+name, s)
		}
	}
}

func isObject(t any) bool {
	if s, ok := t.(string); ok {
		return s == "object"
	}
	return slices.ContainsFunc(List(t), func(x any) bool { return x == "object" })
}
