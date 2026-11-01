package excerpt

import (
	"reflect"
	"strings"
	"testing"
)

// The two shapes seen on the next-steps page: escaped quotes copied from the analyst's %q input,
// and Markdown bold copied from an answer. Result IDs are dummies.
const (
	escapedEvidence = `101: "I don't have reliable information on \"Acme Widgets\"". 102: "I don’t have reliable current details on Acme Widgets’ pricing or features". 103: "I don’t know Acme Widgets’ current self-hosting requirements".`
	boldEvidence    = `201: "Starter is $95/month... roundups list Foo (~$29/mo), Bar (~$24/mo) and Baz (~$20/mo)". 202: "Add-ons can push the real cost to about **$76/month**".`
)

func TestLinesEscapedEvidence(t *testing.T) {
	got := Lines(escapedEvidence)
	want := []string{
		`result 101: “I don't have reliable information on "Acme Widgets"”`,
		"result 102: “I don’t have reliable current details on Acme Widgets’ pricing or features”",
		"result 103: “I don’t know Acme Widgets’ current self-hosting requirements”",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines:\n got %q\nwant %q", got, want)
	}
}

func TestLinesMarkdownEvidence(t *testing.T) {
	got := Lines(boldEvidence)
	want := []string{
		"result 201: “Starter is $95/month... roundups list Foo (~$29/mo), Bar (~$24/mo) and Baz (~$20/mo)”",
		"result 202: “Add-ons can push the real cost to about $76/month”",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines:\n got %q\nwant %q", got, want)
	}
	cs := Parse(boldEvidence)
	if got, want := string(HTML(cs[1].Text)), "Add-ons can push the real cost to about <strong>$76/month</strong>"; got != want {
		t.Fatalf("HTML = %q, want %q", got, want)
	}
}

func TestParseShapes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []Cite
	}{
		// Escaped and curly quotes inside are content; a curly quote can be the outer mark.
		{`Result 7: "named \"Acme\" and “Foo” first"; prompt 3: “cheap, fast”`, []Cite{{"result", 7, `named \"Acme\" and “Foo” first`}, {"prompt", 3, "cheap, fast"}}},
		// The analyst escaped the outer marks too.
		{`#9: \"a b c\" | 10: "d"`, []Cite{{"result", 9, "a b c"}, {"result", 10, "d"}}},
		{`"just a quote"`, []Cite{{"", 0, "just a quote"}}},
	} {
		if got := Parse(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parse(%s):\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// Prose is not a list, and a list whose split is ambiguous is not trusted: both fall back.
func TestParseFallsBack(t *testing.T) {
	for _, in := range []string{
		// An unescaped " inside a quote: splitting here would credit result 12 with words from
		// result 7's quote.
		`7: "Use the format "name". 12: "value" as shown"`,
		`Result 7: "named "Acme" first"; prompt 3: “cheap, fast”`,
		`#9: \"a \"b\" c\" | 10: "d"`,
		// A straight " inside curly or escaped outer marks is held to the same rule.
		`5: “say "hi" now”`,
		`5: \"say "hi" now\"`,
		`Answers 4 and 5 call the \"free tier\" limited.`,
		`101: "quote" and then commentary`,
		`101: "never closed`,
		`101 said "x"`,
		`101: "" `,
		// Zero-padded IDs are neither result 8 (octal) nor result 10.
		`010: "x"`,
		`result 007: "x"`,
		`010: "x". 10: "y"`,
		`https://example.com/pricing: "a fetched quote"`,
		``,
	} {
		if got := Parse(in); got != nil {
			t.Errorf("Parse(%s) = %q, want nil", in, got)
		}
	}
	if got, want := Lines(`7: "Use the format "name". 12: "value" as shown"`), []string{`7: "Use the format "name". 12: "value" as shown"`}; !reflect.DeepEqual(got, want) {
		t.Errorf("Lines ambiguous = %q, want %q", got, want)
	}
	if got, want := Lines(`Answers 4 and 5 call the \"free tier\" **limited**.`), []string{`Answers 4 and 5 call the "free tier" limited.`}; !reflect.DeepEqual(got, want) {
		t.Errorf("Lines fallback = %q, want %q", got, want)
	}
}

// Code spans, link destinations and URLs come out as CommonMark reads them, in HTML and in text.
func TestCodeAndURLsExact(t *testing.T) {
	for _, tc := range []struct{ in, html, text string }{
		{"Use `*config*` as the filename glob", "Use <code>*config*</code> as the filename glob", "Use *config* as the filename glob"},
		{"https://github.com/a/b/compare/main...feature", "https://github.com/a/b/compare/main...feature", "https://github.com/a/b/compare/main...feature"},
		{"<https://x.example/**a**/>", `<a href="https://x.example/**a**/">https://x.example/**a**/</a>`, "https://x.example/**a**/"},
		{"Ignore **/README.md and **/LICENSE", "Ignore **/README.md and **/LICENSE", "Ignore **/README.md and **/LICENSE"},
		{"Use `\\n` to separate lines", "Use <code>\\n</code> to separate lines", `Use \n to separate lines`},
		{`C:\path\to \"x\"`, `C:\path\to &quot;x&quot;`, `C:\path\to "x"`},
		{"see [here](https://x.example/self-hosted)", `see <a href="https://x.example/self-hosted">here</a>`, "see here (https://x.example/self-hosted)"},
		{"[https://x.example](https://x.example)", `<a href="https://x.example">https://x.example</a>`, "https://x.example"},
		// Codex's four: a code span in a link label, an apostrophe in a URL, a parenthesis in a
		// link destination, and spaces in a code span (the spec strips one each side, no more).
		{"[`*config*`](https://x.example)", `<a href="https://x.example"><code>*config*</code></a>`, "*config* (https://x.example)"},
		{"https://x.example/it's...here", "https://x.example/it's...here", "https://x.example/it's...here"},
		{"[docs](https://x.example/a_(b)...tail)", `<a href="https://x.example/a_(b)...tail">docs</a>`, "docs (https://x.example/a_(b)...tail)"},
		{"`  a  b  `", "<code> a  b </code>", " a  b "},
		{"about **$76/month**", "about <strong>$76/month</strong>", "about $76/month"},
		// A line ending inside a code span reads as a space, as the spec says.
		{"`a\nb`", "<code>a b</code>", "a b"},
		// Backslash escapes don't apply inside code.
		{"`a\\*b \\\"c\\\"` and \\*d\\*", "<code>a\\*b \\&quot;c\\&quot;</code> and *d*", "a\\*b \\\"c\\\" and *d*"},
	} {
		if got := string(HTML(tc.in)); got != tc.html {
			t.Errorf("HTML(%s) = %q, want %q", tc.in, got, tc.html)
		}
		if got := Text(tc.in); got != tc.text {
			t.Errorf("Text(%s) = %q, want %q", tc.in, got, tc.text)
		}
	}
}

// Quotes and prose read as inline text: no paragraphs, lists, headings or reference definitions.
func TestInlineOnly(t *testing.T) {
	for in, want := range map[string]string{
		"1. first\n# not a heading": "1. first\n# not a heading",
		"[1]: https://x.example":    "[1]: https://x.example",
		"one\n\ntwo":                "one\ntwo",
	} {
		if got := string(HTML(in)); got != want {
			t.Errorf("HTML(%q) = %q, want %q", in, got, want)
		}
	}
}

// Text is untrusted: raw HTML shows as text, and a script link loses its destination.
func TestHTMLEscapesUntrusted(t *testing.T) {
	out := string(HTML(`<script>alert(1)</script> and **<b>x</b>** [x](javascript:alert(1)) <img src=https://evil.example/y.png>`))
	for _, bad := range []string{"<script", "<b>", "<img", `href="javascript`} {
		if strings.Contains(out, bad) {
			t.Errorf("unescaped %q in %s", bad, out)
		}
	}
	for _, want := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "<strong>&lt;b&gt;x&lt;/b&gt;</strong>", "&lt;img src=https://evil.example/y.png&gt;"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in %s", want, out)
		}
	}
}
