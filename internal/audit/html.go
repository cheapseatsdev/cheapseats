package audit

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// Doc is what one HTML page says about itself, read from the raw HTML as a crawler that runs no
// JavaScript sees it.
type Doc struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	H1          int      `json:"h1"`
	Lang        string   `json:"lang"`
	Canonical   string   `json:"canonical"`
	Noindex     bool     `json:"noindex"`
	JSONLD      []string `json:"jsonld_types"`   // @types, deduplicated
	BadJSONLD   int      `json:"jsonld_invalid"` // ld+json blocks that don't parse
	OGTitle     bool     `json:"og_title"`
	OGDesc      bool     `json:"og_description"`
	TextChars   int      `json:"text_chars"` // visible text in <body>, whitespace collapsed
	// A <div id="root"> or the like: the mount point of an app rendered in the browser.
	AppRoot string `json:"app_root,omitempty"`
	Text    string `json:"-"`
}

// appRoots are the ids single-page app frameworks mount into.
var appRoots = map[string]bool{"root": true, "app": true, "__next": true, "___gatsby": true, "__nuxt": true, "svelte": true}

// skipText is elements whose content isn't visible text.
var skipText = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true, "title": true}

// ParseDoc reads an HTML page.
func ParseDoc(body []byte) Doc {
	var d Doc
	z := html.NewTokenizer(bytes.NewReader(body))
	var text strings.Builder
	types := map[string]bool{}
	skip := 0       // depth inside elements whose text isn't visible
	var in []string // those elements, innermost last
	inBody := false
	var title, ld strings.Builder
	inTitle, inLD := false, false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			attr := func(k string) string {
				for _, a := range t.Attr {
					if a.Key == k {
						return a.Val
					}
				}
				return ""
			}
			switch t.Data {
			case "html":
				d.Lang = strings.TrimSpace(attr("lang"))
			case "body":
				inBody = true
			case "title":
				if !inBody && d.Title == "" {
					inTitle = true
				}
			case "h1":
				d.H1++
			case "meta":
				name := strings.ToLower(attr("name"))
				prop := strings.ToLower(attr("property"))
				content := strings.TrimSpace(attr("content"))
				switch {
				case name == "description" && d.Description == "":
					d.Description = content
				case name == "robots" || name == "googlebot":
					if strings.Contains(strings.ToLower(content), "noindex") {
						d.Noindex = true
					}
				case prop == "og:title" && content != "":
					d.OGTitle = true
				case prop == "og:description" && content != "":
					d.OGDesc = true
				}
			case "link":
				if hasToken(attr("rel"), "canonical") && d.Canonical == "" {
					d.Canonical = strings.TrimSpace(attr("href"))
				}
			case "script":
				if strings.EqualFold(strings.TrimSpace(attr("type")), "application/ld+json") {
					inLD = true
					ld.Reset()
				}
			case "div", "main", "section":
				if id := attr("id"); appRoots[id] && d.AppRoot == "" {
					d.AppRoot = id
				}
			}
			if skipText[t.Data] && tt == html.StartTagToken {
				skip++
				in = append(in, t.Data)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			n := string(name)
			switch n {
			case "title":
				if inTitle {
					d.Title = strings.Join(strings.Fields(title.String()), " ")
					inTitle = false
				}
			case "script":
				if inLD {
					inLD = false
					if ts, ok := ldTypes(ld.String()); ok {
						for _, x := range ts {
							types[x] = true
						}
					} else {
						d.BadJSONLD++
					}
				}
			}
			if skipText[n] && len(in) > 0 && in[len(in)-1] == n {
				in = in[:len(in)-1]
				skip--
			}
		case html.TextToken:
			raw := z.Text()
			switch {
			case inTitle:
				title.Write(raw)
			case inLD:
				ld.Write(raw)
			case skip == 0:
				text.Write(raw)
				text.WriteByte(' ')
			}
		}
	}
	d.Text = strings.Join(strings.Fields(text.String()), " ")
	d.TextChars = len([]rune(d.Text))
	for t := range types {
		d.JSONLD = append(d.JSONLD, t)
	}
	sort.Strings(d.JSONLD)
	return d
}

// hasToken reports whether a space-separated attribute value (rel) holds a token.
func hasToken(v, tok string) bool {
	for _, f := range strings.Fields(strings.ToLower(v)) {
		if f == tok {
			return true
		}
	}
	return false
}

// ldTypes is every @type in a JSON-LD block, at any depth (an @graph, a nested author), or false
// when it doesn't parse.
func ldTypes(s string) ([]string, bool) {
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &v); err != nil {
		return nil, false
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			switch t := x["@type"].(type) {
			case string:
				out = append(out, t)
			case []any:
				for _, e := range t {
					if s, ok := e.(string); ok {
						out = append(out, s)
					}
				}
			}
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out, true
}

// Text is a page's visible text: HTML without its markup, scripts and styles, or anything else as
// it is.
func Text(body []byte, contentType string) string {
	if strings.Contains(strings.ToLower(contentType), "html") || contentType == "" && looksHTML(body) {
		return ParseDoc(body).Text
	}
	return strings.Join(strings.Fields(string(body)), " ")
}

func looksHTML(b []byte) bool {
	head := strings.ToLower(string(b[:min(len(b), 512)]))
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html")
}
