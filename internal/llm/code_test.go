package llm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func codeTestDigest(t *testing.T, src string, names ...string) string {
	t.Helper()
	s, err := CodeDigest(map[string][]byte{"p.go": []byte(src)}, names...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A comment or a reflow is not a change to what the code does; a changed token is, and a named
// digest follows only its names and what they use.
func TestCodeDigestIgnoresCommentsAndLayout(t *testing.T) {
	base := "package p\n\n// F adds.\nfunc F(a, b int) int {\n\treturn a + b\n}\n\nfunc G() {}\n"
	same := "package p\n\n// F adds two numbers.\nfunc F(a, b int) int {\n\n\t// sum\n\treturn a +\n\t\tb /* both */\n}\n\nfunc G() {}\n"
	changed := "package p\n\nfunc F(a, b int) int {\n\treturn a - b\n}\n\nfunc G() {}\n"
	other := "package p\n\nfunc F(a, b int) int {\n\treturn a + b\n}\n\nfunc G() { println() }\n"
	d := func(src string, names ...string) string { return codeTestDigest(t, src, names...) }
	if d(base) != d(same) || d(base, "F") != d(same, "F") {
		t.Errorf("comments and layout changed the digest")
	}
	if d(base) == d(changed) || d(base, "F") == d(changed, "F") {
		t.Errorf("a changed operator left the digest alone")
	}
	if d(base, "F") != d(other, "F") || d(base) == d(other) {
		t.Errorf("a named digest must follow only its names, and a whole one everything")
	}
	if _, err := CodeDigest(map[string][]byte{"p.go": []byte(base)}, "H"); err == nil {
		t.Errorf("a missing declaration must be an error")
	}
}

// gofmt keeps a short func on one line or spreads it over several, which adds semicolons the
// scanner puts in at line ends; the code is the same.
func TestCodeDigestOneLineAndSplitFuncsAreTheSame(t *testing.T) {
	one := "package p\n\ntype s map[string]any\n\nfunc (x s) M() ([]byte, error) { return f(x) }\n\nfunc f(s) ([]byte, error) { return nil, nil }\n"
	split := "package p\n\ntype s map[string]any\n\nfunc (x s) M() ([]byte, error) {\n\treturn f(x)\n}\n\nfunc f(s) ([]byte, error) {\n\treturn nil, nil\n}\n"
	if codeTestDigest(t, one, "s.M") != codeTestDigest(t, split, "s.M") || codeTestDigest(t, one) != codeTestDigest(t, split) {
		t.Errorf("splitting a one-line func changed the digest")
	}
}

// A root's digest covers what it calls, what those call, and a used type's methods, which
// encoding/json and the like call without naming them.
func TestCodeDigestFollowsWhatARootUses(t *testing.T) {
	base := "package p\n\nconst limit = 3\n\ntype T struct{}\n\nfunc (T) MarshalJSON() ([]byte, error) { return nil, nil }\n\n" +
		"func Root(v any) string { return helper(v) }\n\nfunc helper(v any) string { s, _ := v.(string); _ = T{}; return s[:limit] }\n\nfunc Unused() {}\n"
	for name, edit := range map[string][2]string{
		"a helper":             {"s, _ := v.(string)", "s, _ := v.(string); s += \" \""},
		"a const it uses":      {"const limit = 3", "const limit = 4"},
		"a used type's method": {"return nil, nil", "return []byte(\"null\"), nil"},
	} {
		if codeTestDigest(t, base, "Root") == codeTestDigest(t, strings.Replace(base, edit[0], edit[1], 1), "Root") {
			t.Errorf("changing %s left the root's digest alone", name)
		}
	}
	if codeTestDigest(t, base, "Root") != codeTestDigest(t, strings.Replace(base, "func Unused() {}", "func Unused() { println() }", 1), "Root") {
		t.Errorf("a declaration the root doesn't use changed its digest")
	}
}

// StructuredCode panics if a root was renamed or removed, and covers the helpers ReplyText reads
// the reply with.
func TestStructuredCodeCoversItsHelpers(t *testing.T) {
	files := GoFiles(source)
	roots := []string{"Anthropic", "Structured", "structuredBody", "sentSchema", "sentSchema.MarshalJSON", "ReplyText"}
	base, err := CodeDigest(files, roots...)
	if err != nil || base != StructuredCode() {
		t.Fatalf("%v, or StructuredCode has other roots", err)
	}
	src := string(files["llm.go"])
	edited := strings.Replace(src, "func Str(v any) string {\n\ts, _ := v.(string)\n\treturn s\n", "func Str(v any) string {\n\ts, _ := v.(string)\n\treturn strings.TrimSpace(s)\n", 1)
	if edited == src {
		t.Fatal("Str is not where this test expects it")
	}
	files["llm.go"] = []byte(edited)
	if d, _ := CodeDigest(files, roots...); d == base {
		t.Errorf("a change to Str, which ReplyText uses, left the digest alone")
	}
}

// A method on a generic type counts with its type whatever the number of type parameters: T[A]
// is an IndexExpr receiver, T[A, B] an IndexListExpr.
func TestCodeDigestFollowsGenericReceivers(t *testing.T) {
	for typ, src := range map[string]string{
		"Box": "package p\n\ntype Box[A any] struct{}\n\nfunc (x *Box[A]) MarshalJSON() ([]byte, error) { return nil, nil }\n\n" +
			"func Root() any { return Box[int]{} }\n",
		"Pair": "package p\n\ntype Pair[A, B any] struct{}\n\nfunc (x *Pair[A, B]) MarshalJSON() ([]byte, error) { return nil, nil }\n\n" +
			"func Root() any { return Pair[int, int]{} }\n",
	} {
		edited := strings.Replace(src, "return nil, nil", "return []byte(\"null\"), nil", 1)
		if codeTestDigest(t, src, "Root") == codeTestDigest(t, edited, "Root") {
			t.Errorf("changing a method of %s left the digest of a root that uses it alone", typ)
		}
		if _, err := CodeDigest(map[string][]byte{"p.go": []byte(src)}, typ+".MarshalJSON"); err != nil {
			t.Errorf("%s.MarshalJSON is not a declaration: %v", typ, err)
		}
	}
}

// The digest is of the syntax tree, so what gofmt changes (semicolons it removes or a for header
// it splits) doesn't count, and code whose tree differs does even where its tokens barely do.
func TestCodeDigestFollowsTheTree(t *testing.T) {
	fn := func(body string) string {
		return "package p\n\nvar x bool\n\nfunc a()        {}\nfunc b()        {}\nfunc f() bool   { return x }\nfunc g(...int)  {}\n\nfunc F() {\n" + body + "\n}\n"
	}
	for name, pair := range map[string][2]string{
		"if x { a(); b() } and the same over lines": {"if x { a(); b() }", "if x {\n\ta()\n\tb()\n}"},
		"for ; x; {} and for x {}":                  {"for ; x; {\n}", "for x {\n}"},
		"a three-clause for on one line and split":  {"for i := 0; i < 3; i++ {\n\ta()\n}", "for i := 0;\n\ti < 3;\n\ti++ {\n\ta()\n}"},
	} {
		if codeTestDigest(t, fn(pair[0]), "F") != codeTestDigest(t, fn(pair[1]), "F") || codeTestDigest(t, fn(pair[0])) != codeTestDigest(t, fn(pair[1])) {
			t.Errorf("%s hash differently", name)
		}
	}
	for name, pair := range map[string][2]string{
		// f() is the tag in the first and a statement before a tagless switch in the second.
		"switch f() {} and switch f(); {}": {"switch f() {\ncase false:\n\ta()\n}", "switch f(); {\ncase false:\n\ta()\n}"},
		// A newline after f() puts a semicolon in: the second is switch f(); {}.
		"switch f() { and switch f() then { on the next line": {"switch f() {\ncase false:\n\ta()\n}", "switch f()\n{\ncase false:\n\ta()\n}"},
		"g(xs...) and g(xs)": {"xs := []int{}\ng(xs...)", "xs := []int{}\ng(xs)"},
		// The same nodes in different slots: only the nil children's markers tell these apart.
		"s[1:] and s[:1]":                  {"s := []int{}\n_ = s[1:]", "s := []int{}\n_ = s[:1]"},
		"for a(); ; {} and for ; ; a() {}": {"for a(); ; {\n}", "for ; ; a() {\n}"},
	} {
		if codeTestDigest(t, fn(pair[0]), "F") == codeTestDigest(t, fn(pair[1]), "F") || codeTestDigest(t, fn(pair[0])) == codeTestDigest(t, fn(pair[1])) {
			t.Errorf("%s hash the same", name)
		}
	}
	alias, defined := "package p\n\ntype A = int\n", "package p\n\ntype A int\n"
	if codeTestDigest(t, alias, "A") == codeTestDigest(t, defined, "A") {
		t.Errorf("type A = int and type A int hash the same")
	}
}

// What the two switches parse to, so TestCodeDigestFollowsTheTree tests the difference it means to.
func TestSwitchFormsParseDifferently(t *testing.T) {
	for src, wantInit := range map[string]bool{
		"package p\n\nfunc F() {\n\tswitch f() {\n\t}\n}\n":    false,
		"package p\n\nfunc F() {\n\tswitch f(); {\n\t}\n}\n":   true,
		"package p\n\nfunc F() {\n\tswitch f()\n\t{\n\t}\n}\n": true,
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		sw := f.Decls[0].(*ast.FuncDecl).Body.List[0].(*ast.SwitchStmt)
		if (sw.Init != nil) != wantInit || (sw.Tag != nil) == wantInit {
			t.Errorf("%q: Init %v, Tag %v", src, sw.Init, sw.Tag)
		}
	}
}
