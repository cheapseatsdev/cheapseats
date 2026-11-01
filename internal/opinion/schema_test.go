package opinion

import (
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

// Every schema this package sends must be one Anthropic structured outputs accept. The claude CLI
// takes keywords the API answers with an HTTP 400, so tests through that path never see them.
func TestOpinionSchemasFitStructuredOutputs(t *testing.T) {
	for name, s := range map[string]map[string]any{
		"themeSchema": themeSchema,
	} {
		if err := llm.CheckSchema(s); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
