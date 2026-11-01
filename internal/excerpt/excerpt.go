// Package excerpt prepares model-written text that quotes AI answers for display: the analyst's
// evidence, the grader's evidence and reasoning, theme and misstatement quotes.
//
// The analyst reads each answer as a Go-quoted string (fmt %q, see analysis.results2text), so
// the quotes it copies into evidence often keep their escapes: `3756: "on \"Cheap Seats\""`.
// Answers are Markdown, so quotes also carry `**bold**` and the like. Stored text stays as the
// model wrote it; these functions clean it only for people to read. They return plain text:
// HTML escaping is the template's job, so nothing here ever becomes markup.
package excerpt

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Prose cleans model-written prose that may embed a quote (a claim, a reason, a summary): it
// drops the backslash in \" and \' outside code spans and URLs, and changes nothing else.
// Prose can carry code, globs or markup a reader copies (**/README.md, <meta name="x">), so
// asterisks and quotes stay.
func Prose(s string) string {
	var b strings.Builder
	for _, sg := range segments(strings.TrimSpace(s), false) {
		if sg.raw {
			b.WriteString(sg.text)
		} else {
			b.WriteString(unescape(sg.text, false))
		}
	}
	return b.String()
}

// Quote cleans one quoted excerpt of an answer for display between “ and ”. Code spans and URLs
// pass through byte for byte (a code span loses only its backticks); in the plain text around
// them it undoes \", \' and \\, drops well-formed inline Markdown emphasis, makes runs of white
// space one space, writes ... as … and turns straight quote marks into typographic ones. A
// Markdown link reads "text (url)", or the URL alone when that is its text. A matching pair of
// outer quote marks is removed. When a mark could be content (a glob, a path), it stays.
func Quote(s string) string {
	var b strings.Builder
	prev := ' '
	for _, sg := range segments(trimOuter(strings.TrimSpace(s)), true) {
		t := sg.text
		if !sg.raw {
			t = unescape(t, true)
			t = untilStable(t, bold, "$1$2$3")
			t = untilStable(t, italic, "$1$2$3")
			t = space.ReplaceAllString(t, " ")
			t = strings.ReplaceAll(t, "...", "…")
			t = curl(t, prev)
		}
		b.WriteString(t)
		if r, _ := utf8.DecodeLastRuneInString(t); r != utf8.RuneError {
			prev = r
		}
	}
	return strings.TrimSpace(b.String())
}

// Emphasis is a span whose marks touch text on the inside and nothing word-like on the outside,
// so **/README.md, a**b**c and 5 * 3 * 2 are left alone. Its text can't start with / or . (a
// glob). The outer characters are captured, since Go's regexp has no lookaround.
var (
	bold   = regexp.MustCompile(`(^|[^\p{L}\p{N}_/.*])\*\*([^\s*/.](?:[^*\n]*?[^\s*])?)\*\*($|[^\p{L}\p{N}_/.*])`)
	italic = regexp.MustCompile(`(^|[^\p{L}\p{N}_/.*])\*([^\s*/.](?:[^*\n]*?[^\s*])?)\*($|[^\p{L}\p{N}_/.*])`)
	space  = regexp.MustCompile(`\s+`)
	link   = regexp.MustCompile(`^\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	// A bare URL runs to white space, a quote mark, a backtick, a backslash or an angle bracket;
	// trailing sentence punctuation is not part of it.
	bareURL = regexp.MustCompile("^https?://[^\\s<>\"'“”‘’`\\\\]+")
)

// untilStable applies re until nothing changes: neighbouring spans share the character between
// them, which a single non-overlapping pass leaves for the second span.
func untilStable(s string, re *regexp.Regexp, repl string) string {
	for i := 0; i < 8; i++ {
		t := re.ReplaceAllString(s, repl)
		if t == s {
			break
		}
		s = t
	}
	return s
}

// segment is a run of text; raw text (code, a URL) must reach the reader exactly as written.
type segment struct {
	text string
	raw  bool
}

// segments splits s into plain text, code spans and URLs. A code span is a run of backticks up
// to the next run of the same length; one with no closing run is plain text. For display
// (quote), a code span drops its backticks and a Markdown link becomes its text, as plain text,
// and its URL.
func segments(s string, quote bool) []segment {
	var out []segment
	plain := 0 // start of the pending plain text
	flush := func(i int) {
		if i > plain {
			out = append(out, segment{text: s[plain:i]})
		}
	}
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			fence := s[i : i+n]
			end := closingFence(s[i+n:], fence)
			if end < 0 {
				i += n
				continue
			}
			flush(i)
			body := s[i+n : i+n+end]
			if quote {
				out = append(out, segment{text: body, raw: true})
			} else {
				out = append(out, segment{text: fence + body + fence, raw: true})
			}
			i += n + end + n
			plain = i
		case quote && s[i] == '[' && link.MatchString(s[i:]):
			m := link.FindStringSubmatch(s[i:])
			flush(i)
			if m[1] != m[2] {
				out = append(out, segment{text: m[1] + " ("}, segment{text: m[2], raw: true}, segment{text: ")"})
			} else {
				out = append(out, segment{text: m[2], raw: true})
			}
			i += len(m[0])
			plain = i
		case (s[i] == 'h') && bareURL.MatchString(s[i:]) && (i == 0 || !isWord(s[i-1])):
			u := trimURL(bareURL.FindString(s[i:]))
			flush(i)
			out = append(out, segment{text: u, raw: true})
			i += len(u)
			plain = i
		default:
			i++
		}
	}
	flush(len(s))
	return out
}

// closingFence is the offset in s of the next backtick run exactly as long as fence, or -1.
func closingFence(s, fence string) int {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		n := 1
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		if n == len(fence) {
			return i
		}
		i += n
	}
	return -1
}

// trimURL drops sentence punctuation after a URL, and a closing parenthesis it didn't open.
func trimURL(u string) string {
	for len(u) > 0 {
		switch c := u[len(u)-1]; {
		case strings.IndexByte(".,;:!?", c) >= 0:
			u = u[:len(u)-1]
		case c == ')' && strings.Count(u, "(") < strings.Count(u, ")"):
			u = u[:len(u)-1]
		default:
			return u
		}
	}
	return u
}

func isWord(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// unescape drops the backslash in \" and \'. In an excerpt (all), \\ also becomes one
// backslash. Every other sequence, \n included, stays as written: it may be content.
func unescape(s string, all bool) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch c := s[i+1]; {
		case c == '"' || c == '\'':
			b.WriteByte(c)
		case all && c == '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			continue
		}
		i++
	}
	return b.String()
}

// trimOuter removes one pair of quote marks around the whole of s, when no other mark of the
// same kind is inside (so `"a" and "b"` keeps its marks).
func trimOuter(s string) string {
	for _, p := range [][2]string{{`"`, `"`}, {"“", "”"}} {
		if len(s) > len(p[0])+len(p[1]) && strings.HasPrefix(s, p[0]) && strings.HasSuffix(s, p[1]) {
			in := s[len(p[0]) : len(s)-len(p[1])]
			if !strings.Contains(in, p[0]) && !strings.Contains(in, p[1]) {
				return strings.TrimSpace(in)
			}
		}
	}
	return s
}

// curl turns straight quote marks into typographic ones: a mark after a letter, digit or
// closing punctuation closes, any other opens; an apostrophe inside a word is ’. prev is the
// character before s, from the segment before it.
func curl(s string, prev rune) string {
	if !strings.ContainsAny(s, `"'`) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			if opens(prev) {
				r = '“'
			} else {
				r = '”'
			}
		case '\'':
			if opens(prev) {
				r = '‘'
			} else {
				r = '’'
			}
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

func opens(prev rune) bool {
	return unicode.IsSpace(prev) || strings.ContainsRune("([{“‘—–-/", prev)
}

// Cite is one quote from an evidence string with the answer or prompt it names, if any.
type Cite struct {
	// "result", "prompt", or "" when the quote names neither.
	Kind string
	N    int
	// The quote, cleaned by Quote, without its marks.
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
		return "“" + c.Text + "”"
	}
	return c.Label() + ": “" + c.Text + "”"
}

// Parse splits evidence written the way the analyst cites answers, `3756: "quote". 3762:
// "quote"`, into its quotes. A number alone or after "result" or "answer" is a result ID, and
// after "prompt" a prompt number; items may be separated by ".", ";", "," or "|". Parse returns
// nil unless the whole string is such a list, so prose falls back to Prose.
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
			c.Text = Quote(body[:i])
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
// parses, else the whole text cleaned by Prose.
func Lines(s string) []string {
	cs := Parse(s)
	if cs == nil {
		if p := Prose(s); p != "" {
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
