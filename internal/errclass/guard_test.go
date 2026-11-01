package errclass

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clientDirs are the packages that call something outside Cheap Seats.
var clientDirs = []string{"../llm", "../exa", "../typesafe", "../answer", "../audit"}

// What came back from outside, by name: an error reply's body (bounded.Prefix), a CLI's stderr,
// claude's result text. The guard follows what each is assigned to.
func guardTainted(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && x.Name == "bounded" && n.Sel.Name == "Prefix" {
				found = true
			}
			if n.Sel.Name == "Result" || n.Sel.Name == "Body" && !isRespBody(n) {
				found = true
			}
		case *ast.Ident:
			if n.Name == "stderr" {
				found = true
			}
		}
		return !found
	})
	return found
}

// isRespBody is resp.Body or r.Body, the reader itself, which only bounded reads.
func isRespBody(s *ast.SelectorExpr) bool {
	x, ok := s.X.(*ast.Ident)
	return ok && (x.Name == "resp" || x.Name == "r" || x.Name == "req")
}

// A client builds an error from what came back only through this package: the constructors
// are the one place a body, stderr or result text is read into an error, and Error() is safe by
// construction. This fails when a client package passes any of them to fmt.Errorf, errors.New,
// fmt.Sprintf or a slog call, or declares its own error type, which could render one.
func TestClientsBuildExternalErrorsOnlyThroughErrclass(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	for _, dir := range clientDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			n := e.Name()
			if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
				continue
			}
			path := filepath.Join(dir, n)
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			checked++
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil && fd.Name.Name == "Error" {
					t.Errorf("%s: %s declares an Error method; build external errors with errclass", fset.Position(fd.Pos()), path)
				}
			}
			// Names holding what came back, per function.
			ast.Inspect(f, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					return true
				}
				tainted := map[string]bool{}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if as, ok := n.(*ast.AssignStmt); ok {
						for i, r := range as.Rhs {
							if guardTainted(r) && i < len(as.Lhs) {
								if id, ok := as.Lhs[i].(*ast.Ident); ok {
									tainted[id.Name] = true
								}
							}
						}
					}
					return true
				})
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkg, _ := sel.X.(*ast.Ident)
					if pkg == nil {
						return true
					}
					sink := pkg.Name == "fmt" && (sel.Sel.Name == "Errorf" || sel.Sel.Name == "Sprintf") ||
						pkg.Name == "errors" && sel.Sel.Name == "New" || pkg.Name == "slog"
					if !sink {
						return true
					}
					for _, a := range call.Args {
						bad := guardTainted(a)
						ast.Inspect(a, func(n ast.Node) bool {
							if id, ok := n.(*ast.Ident); ok && tainted[id.Name] {
								bad = true
							}
							return !bad
						})
						if bad {
							t.Errorf("%s: %s.%s takes what an external call returned; use errclass", fset.Position(call.Pos()), pkg.Name, sel.Sel.Name)
						}
					}
					return true
				})
				return false
			})
		}
	}
	if checked < 10 {
		t.Fatalf("checked only %d files: the client directories moved", checked)
	}
}
