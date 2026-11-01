package grade

import (
	"strings"
	"testing"
)

// evaluatorPinnedDigest is EvaluatorDigest now. When it changes, every stored evaluation check,
// check batch and experiment reads as stale and can't be resumed, so a change must be meant.
const evaluatorPinnedDigest = "d71c5f79fe10445ac2bdbd61a3766de3d529190ff9227248fa1ffeba52c6655a"

// The digest follows the grader's prompt, its schema as sent, its user message, the code in this
// package and the shared code that sends a structured request and reads the reply. A change to
// any of them is a new evaluator; a comment or a reflow is not. This test fails until a change is
// acknowledged by updating evaluatorPinnedDigest.
func TestEvaluatorDigestIsPinned(t *testing.T) {
	if got := EvaluatorDigest(); got != evaluatorPinnedDigest {
		t.Fatalf("EvaluatorDigest is %s, pinned %s.\n"+
			"What the grader sends or how it reads a reply changed: System, its schema, the user message, code in internal/grade,\n"+
			"or the structured request and reply code in llm (llm.StructuredCode) or the claude CLI path (answer.StructuredCode).\n"+
			"Every stored evaluation check, batch and experiment will read as stale. If that's meant, set evaluatorPinnedDigest to %s\n"+
			"and say so in the release notes.",
			got, evaluatorPinnedDigest, got)
	}
}

func TestEvaluatorDigestTracksEachInput(t *testing.T) {
	js := []byte(`{"type":"object"}`)
	inputs := evaluatorInputs(js)
	all := strings.Join(inputs, "\x00")
	for _, want := range []string{System, string(js), "<answer>", "<competitor alias>", "<about>", "grade ", "llm ", "answer "} {
		if !strings.Contains(all, want) {
			t.Errorf("the digest doesn't hash %.40q", want)
		}
	}
}
