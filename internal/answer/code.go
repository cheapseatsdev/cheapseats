package answer

import (
	"embed"

	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

//go:embed *.go
var source embed.FS

// StructuredCode is a digest of the code that asks the claude CLI for structured output and reads
// its reply (its arguments included), with everything in the package that code uses, for
// grade.EvaluatorDigest. Other edits here leave it alone.
func StructuredCode() string {
	d, err := llm.CodeDigest(llm.GoFiles(source), "ClaudeStructured", "parseClaudeStructured")
	if err != nil {
		panic("answer: " + err.Error())
	}
	return d
}
