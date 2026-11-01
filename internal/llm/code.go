package llm

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"hash"
	"io"
	"io/fs"
	"reflect"
	"sort"
	"strconv"
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
// root's code names, and a type's methods, since encoding/json and the like call those unnamed,
// and the imports of the files those declarations are in.
func CodeDigest(files map[string][]byte, names ...string) (string, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	fset := token.NewFileSet()
	decls := map[string]ast.Node{}
	declFile := map[string]string{} // the file a declaration is in
	fileImports := map[string][]string{}
	methods := map[string][]string{} // a type's methods, as Type.Method
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, files[p], parser.SkipObjectResolution)
		if err != nil {
			return "", err
		}
		if len(names) == 0 {
			fmt.Fprintf(h, "%s\x00", p)
			hashFile(h, f)
			continue
		}
		fileImports[p] = imports(f)
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				name, recv := funcName(d)
				decls[name], declFile[name] = d, p
				if recv != "" {
					methods[recv] = append(methods[recv], name)
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						decls[s.Name.Name], declFile[s.Name.Name] = s, p
					case *ast.ValueSpec:
						// A const in a group can take its type and value from the one above it
						// (iota), so the group counts as one.
						var c ast.Node = s
						if d.Tok == token.CONST && d.Lparen.IsValid() {
							c = d
						}
						for _, n := range s.Names {
							if n.Name != "_" {
								decls[n.Name], declFile[n.Name] = c, p
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
	// The imports of the files the used declarations are in say which package each name they use
	// from another package is: encoding/json's json or another one's.
	usedImports := map[string]bool{}
	for _, n := range used {
		for _, i := range fileImports[declFile[n]] {
			usedImports[i] = true
		}
	}
	importList := make([]string, 0, len(usedImports))
	for i := range usedImports {
		importList = append(importList, i)
	}
	sort.Strings(importList)
	fmt.Fprintf(h, "imports %q\x00", importList)
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
// its post statement, differ), identifiers' names, literals' kinds and values (a struct field's tag
// among them) and operators. Comments and resolved objects are left out, and layout and
// semicolons never reach the tree. What gofmt changes without changing the code is left out or
// made canonical too: parentheses (the tree's shape already says how an expression groups),
// empty statements in a list, a number literal's spelling (0X1F and 0x1F, 1E3 and 1e3) and the
// order of imports. Positions are left out but for the two things written only as one: the "="
// of type A = B and the "..." of f(xs...).
func hashNode(h hash.Hash, v reflect.Value) {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			io.WriteString(h, "nil\x00")
			return
		}
		if p, ok := v.Interface().(*ast.ParenExpr); ok {
			hashNode(h, reflect.ValueOf(p.X))
			return
		}
		if l, ok := v.Interface().(*ast.BasicLit); ok && l.Kind != token.STRING && l.Kind != token.CHAR {
			// gofmt lowercases a number's prefix and exponent; its value is what counts.
			fmt.Fprintf(h, "BasicLit{\x00%s\x00%s\x00}\x00", l.Kind, constant.MakeFromLiteral(l.Value, l.Kind, 0).ExactString())
			return
		}
		hashNode(h, v.Elem())
	case reflect.Slice:
		n := 0
		for i := 0; i < v.Len(); i++ {
			if !isEmptyStmt(v.Index(i)) {
				n++
			}
		}
		fmt.Fprintf(h, "[%d\x00", n)
		for i := 0; i < v.Len(); i++ {
			if !isEmptyStmt(v.Index(i)) {
				hashNode(h, v.Index(i))
			}
		}
		io.WriteString(h, "]\x00")
	case reflect.Struct:
		t := v.Type()
		fmt.Fprintf(h, "%s{\x00", t.Name())
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			switch {
			case f.Type == posType:
				if meaningfulPos[t.Name()+"."+f.Name] {
					fmt.Fprintf(h, "%t\x00", v.Field(i).Interface().(token.Pos).IsValid())
				}
			case f.Type == commentType || f.Type == objectType || f.Type == scopeType:
			case t == emptyStmtType && f.Name == "Implicit": // a written ; or one before } or a newline
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

// isEmptyStmt reports whether a list element is an empty statement, which gofmt drops ({;}) or
// writes differently (L:; and L: before }). A labeled statement's empty body isn't in a list, so
// it still counts.
func isEmptyStmt(v reflect.Value) bool {
	s, ok := v.Interface().(ast.Stmt)
	if !ok {
		return false
	}
	_, ok = s.(*ast.EmptyStmt)
	return ok
}

// hashFile writes a whole file's tree: its package name, its imports in a fixed order (gofmt sorts
// them), and every other declaration in order.
func hashFile(h hash.Hash, f *ast.File) {
	hashNode(h, reflect.ValueOf(f.Name))
	fmt.Fprintf(h, "imports %q\x00", imports(f))
	decls := []ast.Decl{}
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); !ok || g.Tok != token.IMPORT {
			decls = append(decls, d)
		}
	}
	hashNode(h, reflect.ValueOf(decls))
}

// imports are a file's imports as "name path" (the name empty when it's the package's own), sorted:
// gofmt sorts them, and their order means nothing.
func imports(f *ast.File) []string {
	out := make([]string, 0, len(f.Imports))
	for _, s := range f.Imports {
		name := ""
		if s.Name != nil {
			name = s.Name.Name
		}
		path, err := strconv.Unquote(s.Path.Value)
		if err != nil {
			path = s.Path.Value
		}
		out = append(out, name+" "+path)
	}
	sort.Strings(out)
	return out
}

// meaningfulPos are the positions whose presence is code: the "=" of an alias and the "..." that
// passes a slice as a variadic argument. Every other position's presence is layout (a result
// list's parentheses, a grouped declaration's) or follows from the tree.
var meaningfulPos = map[string]bool{"TypeSpec.Assign": true, "CallExpr.Ellipsis": true}

var (
	posType       = reflect.TypeOf(token.NoPos)
	commentType   = reflect.TypeOf((*ast.CommentGroup)(nil))
	objectType    = reflect.TypeOf((*ast.Object)(nil))
	scopeType     = reflect.TypeOf((*ast.Scope)(nil))
	emptyStmtType = reflect.TypeOf(ast.EmptyStmt{})
)
