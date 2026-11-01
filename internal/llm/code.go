package llm

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"hash"
	"io"
	"io/fs"
	"reflect"
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

// CodeDigest is the SHA-256 of Go code's syntax trees (hashNode): comments, semicolons and layout
// aren't in a tree, so it changes with what the code does and not with how it's written up. files
// are one package's files, by name. Without names, all of them count. With names, the roots, each
// of which must be a top-level declaration (a func, a type, a var or const, or a method as
// Type.Method), count with every declaration in the package they use, directly or not: whatever a
// root's code names, and a type's methods, since encoding/json and the like call those unnamed.
func CodeDigest(files map[string][]byte, names ...string) (string, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	fset := token.NewFileSet()
	decls := map[string]ast.Node{}
	methods := map[string][]string{} // a type's methods, as Type.Method
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, files[p], parser.SkipObjectResolution)
		if err != nil {
			return "", err
		}
		if len(names) == 0 {
			fmt.Fprintf(h, "%s\x00", p)
			hashNode(h, reflect.ValueOf(f.Name))
			hashNode(h, reflect.ValueOf(f.Decls))
			continue
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				name, recv := funcName(d)
				decls[name] = d
				if recv != "" {
					methods[recv] = append(methods[recv], name)
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						decls[s.Name.Name] = s
					case *ast.ValueSpec:
						// A const in a group can take its type and value from the one above it
						// (iota), so the group counts as one.
						var c ast.Node = s
						if d.Tok == token.CONST && d.Lparen.IsValid() {
							c = d
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
	if len(names) == 0 {
		return fmt.Sprintf("%x", h.Sum(nil)), nil
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
		ast.Inspect(decls[n], func(x ast.Node) bool {
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
		fmt.Fprintf(h, "%s\x00", n)
		hashNode(h, reflect.ValueOf(decls[n]))
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
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

// hashNode writes a canonical form of a syntax tree to h: each node's type, then its fields in
// declaration order, with a marker for a nil child (so s[1:] and s[:1], or a for loop's init and
// its post statement, differ), identifiers' names, literals' kinds
// and values (a struct field's tag among them) and operators. Positions are left out but for
// whether one is there, since a few things are written only as one: the "=" of type A = B, the
// "..." of f(xs...). Comments and resolved objects are left out. Layout and semicolons, written or
// put in at line ends, never reach the tree, so they don't count.
func hashNode(h hash.Hash, v reflect.Value) {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			io.WriteString(h, "nil\x00")
			return
		}
		hashNode(h, v.Elem())
	case reflect.Slice:
		fmt.Fprintf(h, "[%d\x00", v.Len())
		for i := 0; i < v.Len(); i++ {
			hashNode(h, v.Index(i))
		}
		io.WriteString(h, "]\x00")
	case reflect.Struct:
		fmt.Fprintf(h, "%s{\x00", v.Type().Name())
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			switch f.Type {
			case posType:
				fmt.Fprintf(h, "%t\x00", v.Field(i).Interface().(token.Pos).IsValid())
			case commentType, objectType, scopeType:
			default:
				hashNode(h, v.Field(i))
			}
		}
		io.WriteString(h, "}\x00")
	case reflect.String:
		fmt.Fprintf(h, "%q\x00", v.String())
	case reflect.Int:
		if t, ok := v.Interface().(token.Token); ok {
			fmt.Fprintf(h, "%s\x00", t)
		} else {
			fmt.Fprintf(h, "%d\x00", v.Int())
		}
	default: // bool, ast.ChanDir
		fmt.Fprintf(h, "%v\x00", v.Interface())
	}
}

var (
	posType     = reflect.TypeOf(token.NoPos)
	commentType = reflect.TypeOf((*ast.CommentGroup)(nil))
	objectType  = reflect.TypeOf((*ast.Object)(nil))
	scopeType   = reflect.TypeOf((*ast.Scope)(nil))
)
