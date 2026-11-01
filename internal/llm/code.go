package llm

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"hash"
	"sort"
)

//go:embed llm.go
var llmSource []byte

//go:embed schema.go
var schemaSource []byte

// StructuredCode is a digest of the code in this package that builds a structured output request
// and reads its reply, for a caller's evaluator digest (grade.EvaluatorDigest). Other edits to the
// package leave it alone. The schema as sent is the caller's to hash, with SchemaJSON.
func StructuredCode() string {
	d, err := CodeDigest(map[string][]byte{"llm.go": llmSource, "schema.go": schemaSource},
		"Anthropic", "Structured", "structuredBody", "sentSchema", "sentSchema.MarshalJSON", "ReplyText")
	if err != nil {
		panic("llm: " + err.Error())
	}
	return d
}

// CodeDigest is the SHA-256 of Go code's tokens: comments, blank lines and layout don't count,
// so it changes with what the code does and not with how it's written up. With names, only those
// top-level declarations count (a func, a type, a var or const, or a method as Type.Method), and
// each must be there; without, every file does. files maps a file name to its source.
func CodeDigest(files map[string][]byte, names ...string) (string, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	fset := token.NewFileSet()
	type span struct {
		src        []byte
		start, end token.Pos
	}
	spans := map[string]span{}
	for _, p := range paths {
		src := files[p]
		if len(names) == 0 {
			fmt.Fprintf(h, "%s\x00", p)
			hashTokens(h, src)
			continue
		}
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			return "", err
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				spans[funcName(d)] = span{src, d.Type.Pos(), d.End()}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						spans[s.Name.Name] = span{src, s.Pos(), s.End()}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							spans[n.Name] = span{src, s.Pos(), s.End()}
						}
					}
				}
			}
		}
	}
	for _, n := range names {
		s, ok := spans[n]
		if !ok {
			return "", fmt.Errorf("no declaration %s", n)
		}
		fmt.Fprintf(h, "%s\x00", n)
		hashTokens(h, s.src[fset.Position(s.start).Offset:fset.Position(s.end).Offset])
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// funcName is a func's name, or Type.Method for a method.
func funcName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) != 1 {
		return d.Name.Name
	}
	t := d.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + d.Name.Name
	}
	return d.Name.Name
}

func hashTokens(h hash.Hash, src []byte) {
	var s scanner.Scanner
	s.Init(token.NewFileSet().AddFile("", -1, len(src)), src, nil, 0) // comments skipped
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return
		}
		if tok == token.SEMICOLON {
			lit = ";" // written, or put in at a line's end
		}
		fmt.Fprintf(h, "%s %s\x00", tok, lit)
	}
}
