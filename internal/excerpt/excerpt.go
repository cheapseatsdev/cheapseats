// Package excerpt renders model-written text that quotes AI answers: the analyst's insights and
// evidence, the grader's evidence and reasoning, theme and misstatement quotes.
//
// The analyst reads each answer as a Go-quoted string (fmt %q, see analysis.results2text), so
// the quotes it copies into evidence often keep their escapes: `3756: "on \"Cheap Seats\""`.
// Answers are Markdown, so quotes also carry **bold**, `code` and links. Stored text stays as the
// model wrote it. For display it is read as CommonMark with goldmark, the parser the insights
// already use, so a backslash escape, a code span or a link destination means what the spec
// says it means, with no hand-written rules to get wrong: \" shows as ", **x** as bold, and code
// and URLs exactly as written.
package excerpt

import (
	"bytes"
	"fmt"
	"html/template"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// markdown renders Markdown to HTML. goldmark escapes raw HTML unless told otherwise (it's never
// told), drops javascript: and similar link destinations, and makes no links of bare URLs; an
// image becomes its alt text, so model output can't make a reader's browser fetch anything.
var markdown = goldmark.New(goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(altTextImages{}, 500))))

// inline reads text as one or more paragraphs of inline Markdown only: emphasis, code spans,
// links, autolinks and raw HTML, but no headings, lists, quotes or link reference definitions,
// so a quote that starts "1." or "#" or "[1]: url" reads as written.
var inline = parser.NewParser(
	parser.WithBlockParsers(util.Prioritized(parser.NewParagraphParser(), 100)),
	parser.WithInlineParsers(parser.DefaultInlineParsers()...),
)

// Block renders Markdown with its blocks, for the analyst's insights.
func Block(md string) template.HTML {
	var b bytes.Buffer
	if markdown.Convert([]byte(md), &b) != nil {
		return template.HTML(template.HTMLEscapeString(md))
	}
	return template.HTML(b.String())
}

// HTML renders text as inline Markdown with no surrounding <p>: escapes as the spec reads them,
// emphasis as <strong>/<em>, code spans and links as such. Raw HTML in the text is shown as
// text (insights omit it), since a claim like `Add <meta name="description">` must keep it.
// The result comes only from goldmark's renderer, which escapes everything it writes.
func HTML(s string) template.HTML {
	src := []byte(s)
	doc := parseInline(src)
	var b bytes.Buffer
	if markdown.Renderer().Render(&b, src, doc) != nil {
		return template.HTML(template.HTMLEscapeString(s))
	}
	return template.HTML(strings.TrimSpace(b.String()))
}

// parseInline parses src as inline Markdown, turning each paragraph into a text block (which
// renders without <p>) and each piece of raw HTML into raw text (which renders escaped).
func parseInline(src []byte) ast.Node {
	doc := inline.Parse(text.NewReader(src))
	for c := doc.FirstChild(); c != nil; {
		next := c.NextSibling()
		if p, ok := c.(*ast.Paragraph); ok {
			tb := ast.NewTextBlock()
			tb.SetLines(p.Lines())
			for k := p.FirstChild(); k != nil; {
				kn := k.NextSibling()
				tb.AppendChild(tb, k)
				k = kn
			}
			doc.ReplaceChild(doc, p, tb)
		}
		c = next
	}
	var raws []*ast.RawHTML
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if r, ok := n.(*ast.RawHTML); ok && entering {
			raws = append(raws, r)
		}
		return ast.WalkContinue, nil
	})
	for _, r := range raws {
		parent := r.Parent()
		for i := 0; i < r.Segments.Len(); i++ {
			parent.InsertBefore(parent, r, ast.NewRawTextSegment(r.Segments.At(i)))
		}
		parent.RemoveChild(parent, r)
	}
	return doc
}

// Text is the plain text of inline Markdown, for a terminal or anything else that isn't HTML:
// escapes resolved, emphasis marks dropped, code span content verbatim, a link as "text (url)"
// (or the URL alone when that is its text), an image as its alt text and raw HTML as written.
func Text(s string) string {
	src := []byte(s)
	var b strings.Builder
	for c := inline.Parse(text.NewReader(src)).FirstChild(); c != nil; c = c.NextSibling() {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		writeText(&b, src, c)
	}
	return b.String()
}

func writeText(b *strings.Builder, src []byte, n ast.Node) {
	switch n := n.(type) {
	case *ast.Text:
		v := n.Segment.Value(src)
		if !n.IsRaw() {
			v = util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(v)))
		}
		b.Write(v)
		if n.SoftLineBreak() || n.HardLineBreak() {
			b.WriteByte(' ')
		}
		return
	case *ast.String:
		b.Write(n.Value)
		return
	case *ast.CodeSpan:
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				v := t.Segment.Value(src)
				if bytes.HasSuffix(v, []byte("\n")) {
					v = append(v[:len(v)-1:len(v)-1], ' ')
				}
				b.Write(v)
			}
		}
		return
	case *ast.RawHTML:
		for i := 0; i < n.Segments.Len(); i++ {
			seg := n.Segments.At(i)
			b.Write(seg.Value(src))
		}
		return
	case *ast.AutoLink:
		b.Write(n.URL(src))
		return
	case *ast.Link:
		var label strings.Builder
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			writeText(&label, src, c)
		}
		if dest := string(n.Destination); label.String() != dest {
			b.WriteString(label.String() + " (" + dest + ")")
		} else {
			b.WriteString(dest)
		}
		return
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		writeText(b, src, c)
	}
}

// altTextImages renders an image as nothing but its children, the alt text.
type altTextImages struct{}

func (altTextImages) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindImage, func(util.BufWriter, []byte, ast.Node, bool) (ast.WalkStatus, error) {
		return ast.WalkContinue, nil
	})
}

// Cite is one quote from an evidence string with the answer or prompt it names, if any.
type Cite struct {
	// "result", "prompt", or "" when the quote names neither.
	Kind string
	N    int
	// The quote as written, without its outer marks: Markdown, for HTML or Text.
	Text string
}

// Label names what the quote is from, "result 3756" or "prompt 4", or "" for neither.
func (c Cite) Label() string {
	if c.Kind == "" {
		return ""
	}
	return fmt.Sprintf("%s %d", c.Kind, c.N)
}

// String is the cite as one line of plain text: `result 3756: “…”`.
func (c Cite) String() string {
	if c.Kind == "" {
		return "“" + Text(c.Text) + "”"
	}
	return c.Label() + ": “" + Text(c.Text) + "”"
}

// Parse splits evidence written the way the analyst cites answers, `3756: "quote". 3762:
// "quote"`, into its quotes. A number alone or after "result" or "answer" is a result ID, and
// after "prompt" a prompt number; items may be separated by ".", ";", "," or "|". Parse returns
// nil unless the whole string is such a list, so prose falls back to text.
func Parse(s string) []Cite {
	s = strings.TrimSpace(s)
	var out []Cite
	for s != "" {
		c, rest, ok := item(s)
		if !ok {
			return nil
		}
		out = append(out, c)
		s = strings.TrimLeft(rest, " \t\n")
		if s == "" {
			break
		}
		if !strings.ContainsRune(".;,|", rune(s[0])) {
			return nil
		}
		s = strings.TrimLeft(s[1:], " \t\n")
	}
	return out
}

var header = regexp.MustCompile(`^(?i:(result|answer|prompt)s?\s*)?#?(\d{1,12})\s*:\s*`)

// item reads one `N: "quote"` (or a bare `"quote"`) from the front of s.
func item(s string) (Cite, string, bool) {
	var c Cite
	if m := header.FindStringSubmatchIndex(s); m != nil {
		c.Kind = "result"
		if m[2] >= 0 && strings.EqualFold(s[m[2]:m[3]], "prompt") {
			c.Kind = "prompt"
		}
		// Decimal, and written plainly: 010 is not result 8 (or 10), so it falls back.
		digits := s[m[4]:m[5]]
		n, err := strconv.Atoi(digits)
		if err != nil || len(digits) > 1 && digits[0] == '0' {
			return c, "", false
		}
		c.N = n
		s = s[m[1]:]
	}
	var open, closer string
	switch {
	case strings.HasPrefix(s, `\"`):
		open, closer = `\"`, `\"`
	case strings.HasPrefix(s, `"`):
		open, closer = `"`, `"`
	case strings.HasPrefix(s, "“"):
		open, closer = "“", "”"
	default:
		return c, "", false
	}
	// The quote ends at the first closing mark; an unescaped " anywhere inside makes the split
	// ambiguous (`7: "a "b". 12: "c" d"` could credit result 12 with the wrong words), so the
	// whole string falls back rather than guess.
	body := s[len(open):]
	for i := 0; i < len(body); i++ {
		if strings.HasPrefix(body[i:], closer) {
			if !ends(body[i+len(closer):]) {
				return c, "", false
			}
			c.Text = strings.TrimSpace(body[:i])
			return c, body[i+len(closer):], c.Text != ""
		}
		switch body[i] {
		case '\\':
			i++ // an escaped character, \" included, is content
		case '"':
			return c, "", false
		}
	}
	return c, "", false
}

// ends reports whether a closing quote mark followed by rest ends an item: nothing follows, or
// a separator and then the next item or nothing.
func ends(rest string) bool {
	rest = strings.TrimLeft(rest, " \t\n")
	if rest == "" {
		return true
	}
	if !strings.ContainsRune(".;,|", rune(rest[0])) {
		return false
	}
	rest = strings.TrimLeft(rest[1:], " \t\n")
	return rest == "" || header.MatchString(rest) || strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, `\"`) || strings.HasPrefix(rest, "“")
}

// Lines is evidence as plain text for a terminal or a message: one line per quote when it
// parses, else the whole text as one line (see Text).
func Lines(s string) []string {
	cs := Parse(s)
	if cs == nil {
		if p := strings.TrimSpace(Text(s)); p != "" {
			return []string{p}
		}
		return nil
	}
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.String()
	}
	return out
}
