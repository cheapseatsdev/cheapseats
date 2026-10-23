package audit

import (
	"context"
	"strings"
	"unicode/utf8"
)

// Reader reads a page's text some other way than plain HTTP, for a page plain HTTP can't read: an
// app shell that is empty without JavaScript, or a bot challenge served instead of the page. It
// returns the page's text and title, or ok false when it couldn't read it either. App wires Exa's
// contents endpoint here (docs/optional-providers.md); with none, the checks read what plain HTTP
// got, as they always did.
type Reader func(ctx context.Context, url string) (text, title string, ok bool)

// ViaExa marks a page, finding or citation whose text came from a Reader rather than plain HTTP.
const ViaExa = "exa"

// emptyText is the most text, in runes, an HTML page can carry and still read as empty: under it
// with an app mount point it's a shell, and under it with any script it is worth reading another
// way, since the text may be rendered in the browser.
const emptyText = 200

// emptyShell reports whether an HTML page that answered 200 is empty without JavaScript: under
// emptyText runes of visible text, and a <script> or <noscript> that suggests the browser would
// fill it in. A short static page with no script is just short.
func emptyShell(body []byte, textChars int) bool {
	if textChars >= emptyText {
		return false
	}
	l := strings.ToLower(string(body))
	return strings.Contains(l, "<script") || strings.Contains(l, "<noscript")
}

func runes(s string) int { return utf8.RuneCountInString(s) }
