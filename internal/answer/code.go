package answer

import (
	_ "embed"

	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

//go:embed cli.go
var cliSource []byte

// StructuredCode is a digest of the code that asks the claude CLI for structured output and reads
// its reply (its arguments included), for grade.EvaluatorDigest. Other edits here leave it alone.
func StructuredCode() string {
	d, err := llm.CodeDigest(map[string][]byte{"cli.go": cliSource}, "ClaudeStructured", "parseClaudeStructured")
	if err != nil {
		panic("answer: " + err.Error())
	}
	return d
}
