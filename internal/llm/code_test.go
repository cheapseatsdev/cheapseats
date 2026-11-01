package llm

import "testing"

// A comment or a reflow is not a change to what the code does; a changed token is, and a named
// digest follows only its names.
func TestCodeDigestIgnoresCommentsAndLayout(t *testing.T) {
	base := "package p\n\n// F adds.\nfunc F(a, b int) int {\n\treturn a + b\n}\n\nfunc G() {}\n"
	same := "package p\n\n// F adds two numbers.\nfunc F(a, b int) int {\n\n\t// sum\n\treturn a +\n\t\tb /* both */\n}\n\nfunc G() {}\n"
	changed := "package p\n\nfunc F(a, b int) int {\n\treturn a - b\n}\n\nfunc G() {}\n"
	other := "package p\n\nfunc F(a, b int) int {\n\treturn a + b\n}\n\nfunc G() { println() }\n"
	d := func(src string, names ...string) string {
		t.Helper()
		s, err := CodeDigest(map[string][]byte{"p.go": []byte(src)}, names...)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
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

// StructuredCode panics if a declaration it names was renamed or removed.
func TestStructuredCodeFindsItsDeclarations(t *testing.T) {
	if len(StructuredCode()) != 64 {
		t.Fatal("not a SHA-256")
	}
}
