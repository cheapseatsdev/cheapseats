package grade

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxEvidence is the longest quote kept, in characters of its folded form: the grader is asked for
// one sentence of about 300, and a longer one is more of the answer than a quote.
const maxEvidence = 600

// Quote is the grader's quote as the answer has it, or "" if the answer doesn't have it. The grader
// copies the answer, but not always exactly: it drops markdown, joins lines, straightens quotes. So
// both are folded first (case, runs of space, *, _, ` and ~ emphasis, curly quotes), and the quote
// is looked up in the folded answer. What's returned is the span of the original answer it covers,
// so it's always the answer's own words, and Mark finds it again.
func Quote(answer, quote string) string {
	q, _, _ := fold(quote)
	q = strings.TrimSpace(strings.Trim(strings.TrimSpace(q), `"'`))
	q = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(q, "..."), "…"))
	if q == "" || utf8.RuneCountInString(q) > maxEvidence {
		return ""
	}
	a, starts, ends := fold(answer)
	i := strings.Index(a, q)
	if i < 0 {
		return ""
	}
	start, end := starts[i], ends[i+len(q)-1]
	// Take in the marks around it too, so "**Popcorn** is" isn't cut to "Popcorn** is".
	for start > 0 && strings.IndexByte(marks, answer[start-1]) >= 0 {
		start--
	}
	for end < len(answer) && strings.IndexByte(marks, answer[end]) >= 0 {
		end++
	}
	return answer[start:end]
}

// marks are the markdown characters fold drops.
const marks = "*_`~"

// fold lowercases s, drops markdown emphasis and code marks, makes every run of space one space and
// curly quotes straight. starts and ends map each byte of the result to the start and end of the
// byte run in s it came from.
func fold(s string) (string, []int, []int) {
	var b strings.Builder
	var starts, ends []int
	space := false
	for i, r := range s {
		_, n := utf8.DecodeRuneInString(s[i:]) // 1 for a byte that isn't UTF-8
		end := i + n
		switch {
		case r < utf8.RuneSelf && strings.IndexByte(marks, byte(r)) >= 0:
			continue
		case unicode.IsSpace(r):
			if !space {
				space = true
				starts, ends = append(starts, i), append(ends, end)
				b.WriteByte(' ')
			}
			continue
		case r == '‘' || r == '’':
			r = '\''
		case r == '“' || r == '”':
			r = '"'
		}
		space = false
		r = unicode.ToLower(r)
		for range utf8.RuneLen(r) {
			starts, ends = append(starts, i), append(ends, end)
		}
		b.WriteRune(r)
	}
	return b.String(), starts, ends
}

// Marked is an answer split around its evidence, for a page to highlight without writing HTML.
type Marked struct{ Before, Match, After string }

// Mark splits text around quote, a grade's evidence; all of it is Before when the quote isn't there.
func Mark(text, quote string) Marked {
	i := -1
	if quote != "" {
		i = strings.Index(text, quote)
	}
	if i < 0 {
		return Marked{Before: text}
	}
	return Marked{text[:i], quote, text[i+len(quote):]}
}
