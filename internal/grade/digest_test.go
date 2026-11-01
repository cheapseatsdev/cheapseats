package grade

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"strings"
	"testing"
)

// evaluatorPinnedDigest is EvaluatorDigest now. When it changes, every stored evaluation check, check
// batch and experiment reads as stale and can't be resumed, so a change must be meant.
const evaluatorPinnedDigest = "f0a7c3d20ae17df1ff3b0db895e71117928db1ca3eb6f0c76ad47284f5388a06"

// evaluatorPinnedBehaviour is the code of evaluatorBehaviourFuncs, comments left out, for evaluatorVersion.
const evaluatorPinnedBehaviour = "9eb474f2022ab274753967944cd43d831f8f9740a35ffd6042749dcb0d6e5307"

// evaluatorBehaviourFuncs are the functions whose logic EvaluatorDigest can only follow through
// evaluatorVersion: they build the request or turn the reply into a grade.
var evaluatorBehaviourFuncs = map[string][]string{
	"grade.go":    {"AnswerWithSystem", "Input", "namesBrand", "notNamed", "normalize"},
	"evidence.go": {"Quote", "fold"},
}

// The digest follows the grader's prompt, schema and constants. A change to any of them is a new
// evaluator: this test fails until it's acknowledged by updating evaluatorPinnedDigest.
func TestEvaluatorDigestIsPinned(t *testing.T) {
	if got := EvaluatorDigest(); got != evaluatorPinnedDigest {
		t.Fatalf("EvaluatorDigest is %s, pinned %s.\n"+
			"What the grader sends or how it reads a reply changed (System, its schema, the user message, a constant or evaluatorVersion).\n"+
			"Every stored evaluation check, batch and experiment will read as stale. If that's meant, set evaluatorPinnedDigest to %s and say so in the release notes.",
			got, evaluatorPinnedDigest, got)
	}
}

// A change to the grader's code that can change a grade has to bump evaluatorVersion, which the
// digest hashes; a comment-only edit doesn't, and doesn't trip this test.
func TestEvaluatorBehaviourCodeIsPinned(t *testing.T) {
	got := evaluatorBehaviourHash(t)
	if got != evaluatorPinnedBehaviour {
		t.Fatalf("the grader's request or reply handling code changed (%s), at evaluatorVersion %d.\n"+
			"If it can change a request or a grade, bump evaluatorVersion and update evaluatorPinnedDigest (TestEvaluatorDigestIsPinned prints it).\n"+
			"Either way, set evaluatorPinnedBehaviour to %s.",
			strings.Join(evaluatorFuncNames(), ", "), evaluatorVersion, got)
	}
}

func TestEvaluatorDigestTracksEachInput(t *testing.T) {
	js := []byte(`{"type":"object"}`)
	base := evaluatorInputs(js)
	seen := map[string]int{}
	for i, part := range base {
		if j, dup := seen[part]; dup {
			t.Errorf("inputs %d and %d are both %q", j, i, part)
		}
		seen[part] = i
	}
	if got := evaluatorInputs(js); fmt.Sprint(got) != fmt.Sprint(base) {
		t.Errorf("evaluatorInputs is not deterministic")
	}
	for _, want := range []string{System, string(js), "<answer>", "<competitor alias>", "<about>"} {
		if !strings.Contains(strings.Join(base, "\x00"), want) {
			t.Errorf("the digest doesn't hash %.40q", want)
		}
	}
}

func evaluatorFuncNames() []string {
	var out []string
	for _, file := range []string{"grade.go", "evidence.go"} {
		out = append(out, evaluatorBehaviourFuncs[file]...)
	}
	return out
}

// evaluatorBehaviourHash is the SHA-256 of evaluatorBehaviourFuncs' tokens: comments, blank lines and layout
// don't count.
func evaluatorBehaviourHash(t *testing.T) string {
	t.Helper()
	h := sha256.New()
	for _, file := range []string{"grade.go", "evidence.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		decls := map[string]*ast.FuncDecl{}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				decls[fn.Name.Name] = fn
			}
		}
		for _, name := range evaluatorBehaviourFuncs[file] {
			fn := decls[name]
			if fn == nil {
				t.Fatalf("%s has no func %s; update evaluatorBehaviourFuncs", file, name)
			}
			body := src[fset.Position(fn.Type.Pos()).Offset:fset.Position(fn.End()).Offset]
			var s scanner.Scanner
			s.Init(fset.AddFile(name, -1, len(body)), body, nil, 0) // comments skipped
			fmt.Fprintf(h, "%s\x00", name)
			for {
				_, tok, lit := s.Scan()
				if tok == token.EOF {
					break
				}
				if tok == token.SEMICOLON {
					lit = ";" // written or put in at a line's end
				}
				fmt.Fprintf(h, "%s %s\x00", tok, lit)
			}
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
