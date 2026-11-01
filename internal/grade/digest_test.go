package grade

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// evaluatorPinnedDigest is EvaluatorDigest now. When it changes, every stored evaluation check,
// check batch and experiment reads as stale and can't be resumed, so a change must be meant.
const evaluatorPinnedDigest = "692adf565564e9eb6e9ea9c7f2ff92337578684974abf9e51f3979a77e186cdc"

// The digest follows the grader's prompt, its schema as sent, its user message, the code in this
// package, the shared code that sends a structured request and reads the reply, and the sets code
// stored evaluation cases decode through. A change to any of them is a new evaluator; a comment or
// a reflow is not. This test fails until a change is acknowledged by updating evaluatorPinnedDigest.
func TestEvaluatorDigestIsPinned(t *testing.T) {
	if got := EvaluatorDigest(); got != evaluatorPinnedDigest {
		t.Fatalf("EvaluatorDigest is %s, pinned %s.\n"+
			"What the grader sends or how it reads a reply changed: System, its schema, the user message, code in internal/grade,\n"+
			"or the structured request and reply code in llm (llm.StructuredCode) or the claude CLI path (answer.StructuredCode),\n"+
			"or sets.Competitor, which stored evaluation cases decode their competitors through.\n"+
			"Every stored evaluation check, batch and experiment will read as stale. If that's meant, set evaluatorPinnedDigest to %s\n"+
			"and say so in the release notes.",
			got, evaluatorPinnedDigest, got)
	}
}

func TestEvaluatorDigestTracksEachInput(t *testing.T) {
	js := []byte(`{"type":"object"}`)
	inputs := evaluatorInputs(js)
	all := strings.Join(inputs, "\x00")
	for _, want := range []string{System, string(js), "<answer>", "<competitor alias>", "<about>", "grade ", "llm ", "answer ", "sets "} {
		if !strings.Contains(all, want) {
			t.Errorf("the digest doesn't hash %.40q", want)
		}
	}
}

// A stored evaluation case (GoldenRecord) decodes its competitors through sets.Competitor's JSON
// tags: renaming "aliases" would drop them from what Input sends, so it must change the digest.
func TestEvaluatorDigestTracksCompetitorJSONTags(t *testing.T) {
	files := llm.GoFiles(sets.Source())
	base, err := llm.CodeDigest(files, setsCodeRoots...)
	if err != nil || base != setsCode() {
		t.Fatalf("%v, or setsCode has other roots", err)
	}
	src := string(files["sets.go"])
	edited := strings.Replace(src, `json:"aliases,omitempty"`, `json:"aka,omitempty"`, 1)
	if edited == src {
		t.Fatal("Competitor's aliases tag is not where this test expects it")
	}
	files["sets.go"] = []byte(edited)
	if d, _ := llm.CodeDigest(files, setsCodeRoots...); d == base {
		t.Errorf("renaming Competitor's aliases JSON tag left the digest alone")
	}
}

// Everything a GoldenRecord decodes through is in this package or a root of setsCode, so the
// digest covers the JSON tags that load stored evaluation evidence. A field of a type from
// another package must be added to the digest before this test is changed.
func TestGoldenRecordDecodesThroughCoveredTypes(t *testing.T) {
	gradePkg := reflect.TypeOf(GoldenRecord{}).PkgPath()
	setsPkg := reflect.TypeOf(sets.Set{}).PkgPath()
	covered := map[string]bool{}
	for _, n := range setsCodeRoots {
		covered[setsPkg+"."+n] = true
	}
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		// A named type is checked before anything inside it: a named slice or map from another
		// package (type Aliases []string, with its own UnmarshalJSON) decodes through its code.
		if p := typ.PkgPath(); p != "" && p != gradePkg && !covered[p+"."+typ.Name()] {
			t.Errorf("GoldenRecord decodes through %s, which the evaluator digest doesn't cover", typ)
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(typ.Elem())
		case reflect.Map:
			walk(typ.Key())
			walk(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				walk(typ.Field(i).Type)
			}
		}
	}
	walk(reflect.TypeOf(GoldenRecord{}))
}
