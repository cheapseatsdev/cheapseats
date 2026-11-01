package excerpt

import (
	"reflect"
	"testing"
)

// The two shapes seen on the next-steps page: escaped quotes copied from the analyst's %q input,
// and Markdown bold copied from an answer. Result IDs are dummies.
const (
	escapedEvidence = `101: "I don't have reliable information on \"Acme Widgets\"". 102: "I don’t have reliable current details on Acme Widgets’ pricing or features". 103: "I don’t know Acme Widgets’ current self-hosting requirements".`
	boldEvidence    = `201: "Starter is $95/month... roundups list Foo (~$29/mo), Bar (~$24/mo) and Baz (~$20/mo)". 202: "Add-ons can push the real cost to about **$76/month**".`
)

func TestParseEscapedEvidence(t *testing.T) {
	got := Parse(escapedEvidence)
	want := []Cite{
		{"result", 101, "I don’t have reliable information on “Acme Widgets”"},
		{"result", 102, "I don’t have reliable current details on Acme Widgets’ pricing or features"},
		{"result", 103, "I don’t know Acme Widgets’ current self-hosting requirements"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse:\n got %q\nwant %q", got, want)
	}
}

func TestParseMarkdownEvidence(t *testing.T) {
	got := Parse(boldEvidence)
	want := []Cite{
		{"result", 201, "Starter is $95/month… roundups list Foo (~$29/mo), Bar (~$24/mo) and Baz (~$20/mo)"},
		{"result", 202, "Add-ons can push the real cost to about $76/month"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse:\n got %q\nwant %q", got, want)
	}
}

func TestParseShapes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []Cite
	}{
		// Unescaped inner quotes: only a mark followed by a separator and the next item closes.
		{`Result 7: "named "Acme" first"; prompt 3: “cheap, fast”`, []Cite{{"result", 7, "named “Acme” first"}, {"prompt", 3, "cheap, fast"}}},
		// The analyst escaped the outer marks too.
		{`#9: \"a \"b\" c\" | 10: "d"`, []Cite{{"result", 9, "a “b” c"}, {"result", 10, "d"}}},
		{`"just a quote"`, []Cite{{"", 0, "just a quote"}}},
	} {
		if got := Parse(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parse(%s):\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// Prose is not a list: it falls back, cleaned but otherwise as written.
func TestParseFallsBack(t *testing.T) {
	for _, in := range []string{
		`Answers 4 and 5 call the \"free tier\" **limited**.`,
		`101: "quote" and then commentary`,
		`101: "never closed`,
		`101 said "x"`,
		`101: "" `,
		`https://example.com/pricing: "a fetched quote"`,
		``,
	} {
		if got := Parse(in); got != nil {
			t.Errorf("Parse(%s) = %q, want nil", in, got)
		}
	}
	if got, want := Lines(`Answers 4 and 5 call the \"free tier\" **limited**.`), []string{`Answers 4 and 5 call the "free tier" limited.`}; !reflect.DeepEqual(got, want) {
		t.Errorf("Lines fallback = %q, want %q", got, want)
	}
}

func TestLines(t *testing.T) {
	got := Lines(boldEvidence)
	want := []string{
		"result 201: “Starter is $95/month… roundups list Foo (~$29/mo), Bar (~$24/mo) and Baz (~$20/mo)”",
		"result 202: “Add-ons can push the real cost to about $76/month”",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines:\n got %q\nwant %q", got, want)
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		`"Use *Acme* or ` + "`acme-cli`" + ` — see [the docs](https://acme.example/docs)"`: "Use Acme or acme-cli — see the docs",
		"line one\\nLine  two\n\nthree\\n- four":                                           "line one Line two three - four",
		`a "b" and "c"`:                                                                    "a “b” and “c”",
		`5 * 3 * 2 stays`:                                                                  "5 * 3 * 2 stays",
		`it's 'fine'`:                                                                      "it’s ‘fine’",
		`C:\path\to stays`:                                                                 `C:\path\to stays`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%s) = %q, want %q", in, got, want)
		}
	}
}

// Prose keeps straight quotes and single asterisks, which can be code a reader copies.
func TestProse(t *testing.T) {
	in := `Add <meta name="description"> saying \"self-hosted\" and **free**; match *.md files`
	want := `Add <meta name="description"> saying "self-hosted" and free; match *.md files`
	if got := Prose(in); got != want {
		t.Fatalf("Prose = %q, want %q", got, want)
	}
	if got := Prose(`split on \n here`); got != `split on \n here` {
		t.Fatalf("Prose should leave \\n alone: %q", got)
	}
}
