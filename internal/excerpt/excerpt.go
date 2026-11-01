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
	"strings"
	"unicode"
	"unicode/utf8"
)

// Prose cleans model-written prose that may embed a quote (a claim, a reason, a summary): it
// drops escapes before quote marks and Markdown bold markers, and leaves everything else as it
// was. Straight quotes stay straight, since prose can carry code or markup a reader copies.
func Prose(s string) string {
	return strings.TrimSpace(bold.ReplaceAllString(unescape(s, false), "$1"))
}

// Quote cleans one quoted excerpt of an answer for display between “ and ”: escapes and inline
// Markdown (bold, italic, code, links) are dropped, runs of white space become one space, a
// matching pair of outer quote marks is removed, and the straight quote marks left inside
// become typographic ones.
func Quote(s string) string {
	s = unescape(s, true)
	s = link.ReplaceAllString(s, "$1")
	s = bold.ReplaceAllString(s, "$1")
	s = italic.ReplaceAllString(s, "$1$2$3")
	s = code.ReplaceAllString(s, "$1")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "...", "…")
	s = trimOuter(s)
	return curl(s)
}

var (
	bold   = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	italic = regexp.MustCompile(`(^|[\s(\["“'‘])\*([^*\s](?:[^*\n]*?[^*\s])?)\*($|[\s).,;:!?\]"”'’])`)
	code   = regexp.MustCompile("`([^`\n]+)`")
	link   = regexp.MustCompile(`\[([^\]\n]+)\]\((?:https?://)[^)\s]+\)`)
)

// unescape drops the backslash before a quote mark. In an excerpt (all), it also undoes the rest
// of what %q writes: \n, \r and \t become spaces and \\ one backslash. A \n or \t before a
// lowercase letter is more likely a path (C:\notes) than a line break, so it stays.
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
		case all && (c == 'n' || c == 'r' || c == 't') && !(i+2 < len(s) && 'a' <= s[i+2] && s[i+2] <= 'z'):
			b.WriteByte(' ')
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
// closing punctuation closes, any other opens; an apostrophe inside a word is ’.
func curl(s string) string {
	if !strings.ContainsAny(s, `"'`) {
		return s
	}
	var b strings.Builder
	prev := ' '
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
		fmt.Sscan(s[m[4]:m[5]], &c.N)
		s = s[m[1]:]
	}
	open, closers := "", ""
	switch {
	case strings.HasPrefix(s, `\"`):
		open, closers = `\"`, `\"`
	case strings.HasPrefix(s, `"`):
		open, closers = `"`, `"”`
	case strings.HasPrefix(s, "“"):
		open, closers = "“", `”"`
	default:
		return c, "", false
	}
	body := s[len(open):]
	for i := 0; i < len(body); {
		r, n := utf8.DecodeRuneInString(body[i:])
		end := -1
		switch {
		case open == `\"` && strings.HasPrefix(body[i:], `\"`):
			end = i + 2
		case open != `\"` && strings.ContainsRune(closers, r) && (r != '"' || i == 0 || body[i-1] != '\\'):
			end = i + n
		}
		if end > 0 && ends(body[end:]) {
			c.Text = Quote(body[:i])
			return c, body[end:], c.Text != ""
		}
		i += n
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
