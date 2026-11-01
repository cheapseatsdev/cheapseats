package sets

import (
	"embed"
	"io/fs"
)

//go:embed *.go
var source embed.FS

// Source is this package's Go files, for grade.EvaluatorDigest: frozen evaluation evidence
// (grade.GoldenRecord) decodes its competitors through Competitor's JSON tags, so they decide what
// the grader is sent. sets can't import llm (llm imports it), so the digest is the caller's to take.
func Source() fs.FS { return source }
