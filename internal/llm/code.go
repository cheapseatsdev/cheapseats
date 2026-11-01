package llm

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"hash"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.go
var source embed.FS

// StructuredCode is a digest of the code in this package that builds a structured output request
// and reads its reply, and everything in the package that code uses, for a caller's evaluator
// digest (grade.EvaluatorDigest). Other edits to the package leave it alone. The schema as sent is
// the caller's to hash, with SchemaJSON.
func StructuredCode() string {
	d, err := CodeDigest(GoFiles(source),
		"Anthropic", "Structured", "structuredBody", "sentSchema", "sentSchema.MarshalJSON", "ReplyText")
	if err != nil {
		panic("llm: " + err.Error())
	}
	return d
}

// GoFiles are the non-test Go files at the top of fsys, by name, for CodeDigest. It panics if
// fsys can't be read: it is a package's own embedded source.
func GoFiles(fsys fs.FS) map[string][]byte {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		panic("llm.GoFiles: " + err.Error())
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			if files[n], err = fs.ReadFile(fsys, n); err != nil {
				panic("llm.GoFiles: " + err.Error())
			}
		}
	}
	return files
}

// CodeDigest is the SHA-256 of Go code's tokens: comments and layout (the semicolons put in at line
// ends among it) don't count, so it changes with what the code does and not with how it's written
// up. files are one package's files, by name. Without names, all of them count. With names, the roots, each of which must be
// a top-level declaration (a func, a type, a var or const, or a method as Type.Method), count with
// every declaration in the package they use, directly or not: whatever a root's code names, and a
// type's methods, since encoding/json and the like call those unnamed.
func CodeDigest(files map[string][]byte, names ...string) (string, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	if len(names) == 0 {
		for _, p := range paths {
			fmt.Fprintf(h, "%s\x00", p)
			hashTokens(h, files[p])
		}
		return fmt.Sprintf("%x", h.Sum(nil)), nil
	}
	fset := token.NewFileSet()
	decls := map[string]codeDecl{}
	methods := map[string][]string{} // a type's methods, as Type.Method
	for _, p := range paths {
		src := files[p]
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			return "", err
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				name, recv := funcName(d)
				decls[name] = codeDecl{src, fset, d.Type.Pos(), d.End(), d}
				if recv != "" {
					methods[recv] = append(methods[recv], name)
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						decls[s.Name.Name] = codeDecl{src, fset, s.Pos(), s.End(), s}
					case *ast.ValueSpec:
						// A const in a group can take its type and value from the one above it
						// (iota), so the group counts as one.
						c := codeDecl{src, fset, s.Pos(), s.End(), s}
						if d.Tok == token.CONST && d.Lparen.IsValid() {
							c = codeDecl{src, fset, d.Pos(), d.End(), d}
						}
						for _, n := range s.Names {
							if n.Name != "_" {
								decls[n.Name] = c
							}
						}
					}
				}
			}
		}
	}
	seen := map[string]bool{}
	queue := []string{}
	add := func(n string) {
		if _, ok := decls[n]; ok && !seen[n] {
			seen[n] = true
			queue = append(queue, n)
		}
	}
	for _, n := range names {
		if _, ok := decls[n]; !ok {
			return "", fmt.Errorf("no declaration %s", n)
		}
		add(n)
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		ast.Inspect(decls[n].node, func(x ast.Node) bool {
			if id, ok := x.(*ast.Ident); ok {
				add(id.Name)
			}
			return true
		})
		for _, m := range methods[n] {
			add(m)
		}
	}
	used := make([]string, 0, len(seen))
	for n := range seen {
		used = append(used, n)
	}
	sort.Strings(used)
	for _, n := range used {
		d := decls[n]
		fmt.Fprintf(h, "%s\x00", n)
		hashTokens(h, d.src[d.fset.Position(d.start).Offset:d.fset.Position(d.end).Offset])
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// codeDecl is a top-level declaration: its source span and its syntax.
type codeDecl struct {
	src        []byte
	fset       *token.FileSet
	start, end token.Pos
	node       ast.Node
}

// funcName is a func's name, or Type.Method and Type for a method.
func funcName(d *ast.FuncDecl) (name, recv string) {
	if d.Recv == nil || len(d.Recv.List) != 1 {
		return d.Name.Name, ""
	}
	t := d.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	switch g := t.(type) { // a generic type's method: T[A], or T[A, B] with more parameters
	case *ast.IndexExpr:
		t = g.X
	case *ast.IndexListExpr:
		t = g.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + d.Name.Name, id.Name
	}
	return d.Name.Name, ""
}

// hashTokens writes src's tokens to h. The semicolons the scanner puts in at line ends (reported
// with the literal "\n") are layout and left out, so a one-line func and the same func split over
// lines are the same. A semicolon written in the source (literal ";") counts: it can change what
// code means, as in switch f() {} and switch f(); {}.
func hashTokens(h hash.Hash, src []byte) {
	var s scanner.Scanner
	s.Init(token.NewFileSet().AddFile("", -1, len(src)), src, nil, 0) // comments skipped
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return
		}
		if tok != token.SEMICOLON || lit == ";" {
			fmt.Fprintf(h, "%s %s\x00", tok, lit)
		}
	}
}
