// Package scaffold starts a config repo from a brand's website, for `cheapseats init`: it reads what
// the home page, /llms.txt and /sitemap.xml say about the brand (its name, what it is, its docs,
// blog and code), then writes cheapseats.toml and starter prompt sets with TODOs where a person must
// decide. Every fetch goes through audit's Fetcher, so it reaches public addresses only.
package scaffold

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"golang.org/x/net/html"
)

// Site is what Discover found.
type Site struct {
	URL  string `json:"url"`  // the home page, after redirects
	Host string `json:"host"` // without www.
	Name string `json:"name"`
	// Where the name came from: og:site_name, application-name, json-ld, title, llms.txt or domain.
	NameFrom string        `json:"name_from"`
	About    string        `json:"about"`
	Sites    []config.Site `json:"sites"`
	Aliases  []string      `json:"aliases"`
	LLMs     bool          `json:"llms_txt"`
	Sitemap  int           `json:"sitemap_pages"` // pages read from the sitemap
}

// maxSitemapPages is the most sitemap entries read for docs and blog paths.
const maxSitemapPages = 5000

// Discover reads a brand's home page, and its /llms.txt and /sitemap.xml when they're there. Only
// the home page has to answer.
func Discover(ctx context.Context, f *audit.Fetcher, home string) (Site, error) {
	if !strings.Contains(home, "://") {
		home = "https://" + home
	}
	resp, err := f.Get(ctx, home)
	if err != nil {
		return Site{}, err
	}
	if resp.Status < 200 || resp.Status > 299 {
		return Site{}, fmt.Errorf("%s answered %d", home, resp.Status)
	}
	base, err := url.Parse(resp.URL)
	if err != nil {
		return Site{}, err
	}
	s := Site{URL: resp.URL, Host: strings.TrimPrefix(strings.ToLower(base.Hostname()), "www.")}
	h := readHead(resp.Body, base)
	links := h.links

	root := base.Scheme + "://" + base.Host
	var llmsName, llmsAbout string
	if r, err := f.Get(ctx, root+"/llms.txt"); err == nil && r.Status == 200 && !strings.Contains(r.ContentType(), "html") {
		s.LLMs = true
		llmsName, llmsAbout = readLLMs(string(r.Body))
		links = append(links, mdLinks(string(r.Body), base)...)
	}
	pages := readSitemap(ctx, f, root+"/sitemap.xml")
	s.Sitemap = len(pages)
	for _, p := range pages {
		if u, err := url.Parse(p); err == nil {
			links = append(links, u)
		}
	}

	label := domainLabel(s.Host)
	switch {
	case good(h.siteName):
		s.Name, s.NameFrom = h.siteName, "og:site_name"
	case good(h.appName):
		s.Name, s.NameFrom = h.appName, "application-name"
	case good(h.ldName):
		s.Name, s.NameFrom = h.ldName, "json-ld"
	default:
		if n := TitleName(h.title, label); n != "" {
			s.Name, s.NameFrom = n, "title"
		} else if good(llmsName) {
			s.Name, s.NameFrom = llmsName, "llms.txt"
		} else {
			s.Name, s.NameFrom = titleCase(label), "domain"
		}
	}
	for _, d := range []string{h.description, h.ogDescription, h.ldDescription, llmsAbout} {
		if d != "" {
			s.About = d
			break
		}
	}
	// Everything here is the site's to say, and ends up in TOML and its comments: one line each.
	s.Name, s.About, s.URL = Clean(s.Name), Clean(s.About), Clean(s.URL)
	s.Sites = DetectSites(s.Host, s.Name, links)
	s.Aliases = Aliases(s.Name, s.Host)
	return s, nil
}

// Aliases are the other names answers may use for the brand: its domain, and its name without
// spaces.
func Aliases(name, host string) []string {
	var out []string
	add := func(a string) {
		if a == "" || strings.EqualFold(a, name) {
			return
		}
		for _, x := range out {
			if strings.EqualFold(x, a) {
				return
			}
		}
		out = append(out, a)
	}
	add(host)
	add(strings.Join(strings.Fields(name), ""))
	return out
}

// Clean is text from a site as one line: control characters dropped, whitespace collapsed, so a
// name can't break out of a TOML comment or string.
func Clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// head is what readHead takes from a page.
type head struct {
	title, siteName, appName, description, ogDescription string
	ldName, ldDescription                                string
	links                                                []*url.URL
}

// readHead reads the page's names, descriptions and links, with relative links resolved on base.
func readHead(body []byte, base *url.URL) head {
	var h head
	z := html.NewTokenizer(bytes.NewReader(body))
	var title, ld strings.Builder
	inTitle, inLD, inBody := false, false, false
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
						return Clean(a.Val)
					}
				}
				return ""
			}
			switch t.Data {
			case "body":
				inBody = true
			case "title":
				inTitle = !inBody && h.title == ""
			case "meta":
				key := strings.ToLower(attr("property") + attr("name"))
				c := attr("content")
				first := func(p *string) {
					if *p == "" {
						*p = c
					}
				}
				switch key {
				case "og:site_name":
					first(&h.siteName)
				case "application-name":
					first(&h.appName)
				case "description":
					first(&h.description)
				case "og:description":
					first(&h.ogDescription)
				}
			case "script":
				if strings.EqualFold(attr("type"), "application/ld+json") {
					inLD = true
					ld.Reset()
				}
			case "a", "link":
				if href := attr("href"); href != "" && (t.Data == "a" || hasRel(attr("rel"), "alternate", "me")) {
					if u, err := base.Parse(href); err == nil {
						h.links = append(h.links, u)
					}
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "title":
				if inTitle {
					h.title = Clean(title.String())
					inTitle = false
				}
			case "script":
				if inLD {
					inLD = false
					n, d := ldOrg(ld.String())
					if h.ldName == "" {
						h.ldName = n
					}
					if h.ldDescription == "" {
						h.ldDescription = d
					}
				}
			}
		case html.TextToken:
			switch {
			case inTitle:
				title.Write(z.Text())
			case inLD:
				ld.Write(z.Text())
			}
		}
	}
	return h
}

func hasRel(rel string, want ...string) bool {
	for _, f := range strings.Fields(strings.ToLower(rel)) {
		for _, w := range want {
			if f == w {
				return true
			}
		}
	}
	return false
}

// ldTypes are the JSON-LD types whose name is the brand's.
var ldTypes = map[string]bool{"Organization": true, "Corporation": true, "SoftwareApplication": true, "WebApplication": true, "MobileApplication": true}

// ldOrg is the name and description of the first Organization or SoftwareApplication in a JSON-LD
// block, at any depth (an @graph, a publisher).
func ldOrg(s string) (name, desc string) {
	var v any
	if json.Unmarshal([]byte(strings.TrimSpace(s)), &v) != nil {
		return "", ""
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			match := false
			switch t := x["@type"].(type) {
			case string:
				match = ldTypes[t]
			case []any:
				for _, e := range t {
					if s, ok := e.(string); ok && ldTypes[s] {
						match = true
					}
				}
			}
			if n, ok := x["name"].(string); match && ok && strings.TrimSpace(n) != "" {
				name = Clean(n)
				desc, _ = x["description"].(string)
				desc = Clean(desc)
				return true
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys) // the same answer every time
			for _, k := range keys {
				if walk(x[k]) {
					return true
				}
			}
		case []any:
			for _, e := range x {
				if walk(e) {
					return true
				}
			}
		}
		return false
	}
	walk(v)
	return name, desc
}

// titleSeps split a title into its parts: "Acme | Project management for teams".
var titleSeps = []string{" | ", " – ", " — ", " · ", ": ", " - "}

// generic are title parts that are never the brand.
var generic = map[string]bool{"home": true, "homepage": true, "home page": true, "welcome": true, "official site": true, "official website": true, "index": true}

// TitleName is the brand in a page title: the part that spells the domain's name, else the
// shortest part that looks like a name (four words or fewer). "" when no part does.
func TitleName(title, label string) string {
	parts := []string{title}
	for _, sep := range titleSeps {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}
	best := ""
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if !good(p) || len(strings.Fields(p)) > 4 {
			continue
		}
		if label != "" && key(p) == label {
			return p
		}
		if best == "" || len(p) < len(best) {
			best = p
		}
	}
	return best
}

// good reports whether a candidate looks like a brand name: some letters, not too long, not
// "Home".
func good(s string) bool {
	s = strings.TrimSpace(s)
	return key(s) != "" && len([]rune(s)) <= 60 && !generic[strings.ToLower(s)]
}

// key is a name's letters and digits, lowercased.
func key(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// domainLabel is the host's name without www. and its suffix: "acme" for www.acme.co.uk.
func domainLabel(host string) string {
	h := strings.TrimPrefix(strings.ToLower(host), "www.")
	if i := strings.IndexByte(h, '.'); i >= 0 {
		h = h[:i]
	}
	return key(h)
}

func titleCase(s string) string {
	if s == "" {
		return "Brand"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// readLLMs is an llms.txt's name (its H1) and summary (the blockquote under it).
func readLLMs(s string) (name, about string) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case name == "" && strings.HasPrefix(line, "# "):
			name = Clean(line[2:])
		case about == "" && strings.HasPrefix(line, ">"):
			about = Clean(strings.TrimPrefix(line, ">"))
		}
	}
	return name, about
}

// mdLinks are the targets of markdown links: [text](url).
func mdLinks(s string, base *url.URL) []*url.URL {
	var out []*url.URL
	for {
		i := strings.Index(s, "](")
		if i < 0 {
			return out
		}
		s = s[i+2:]
		j := strings.IndexAny(s, ") \n")
		if j < 0 {
			return out
		}
		if u, err := base.Parse(s[:j]); err == nil {
			out = append(out, u)
		}
		s = s[j:]
	}
}

// readSitemap is a sitemap's pages, following an index to its first few sitemaps.
func readSitemap(ctx context.Context, f *audit.Fetcher, u string) []string {
	var pages []string
	queue := []string{u}
	for fetched := 0; len(queue) > 0 && fetched < 4 && len(pages) < maxSitemapPages; fetched++ {
		r, err := f.Get(ctx, queue[0])
		queue = queue[1:]
		if err != nil || r.Status != 200 {
			continue
		}
		p, children, err := audit.ParseSitemap(r.Body)
		if err != nil {
			continue
		}
		pages = append(pages, p...)
		queue = append(queue, children...)
	}
	if len(pages) > maxSitemapPages {
		pages = pages[:maxSitemapPages]
	}
	return pages
}

// subKinds and pathKinds say what a subdomain or a first path segment is for.
var (
	subKinds  = map[string]string{"docs": "docs", "doc": "docs", "developer": "docs", "developers": "docs", "help": "docs", "support": "docs", "blog": "blog", "community": "community", "forum": "community", "forums": "community", "discuss": "community"}
	pathKinds = map[string]string{"docs": "docs", "doc": "docs", "documentation": "docs", "developers": "docs", "help": "docs", "blog": "blog", "community": "community", "forum": "community"}
	// githubReserved are github.com paths that aren't an owner.
	githubReserved = map[string]bool{"about": true, "features": true, "pricing": true, "login": true, "join": true, "signup": true, "sponsors": true, "marketplace": true, "orgs": true, "topics": true, "apps": true, "settings": true, "explore": true, "enterprise": true, "security": true, "collections": true, "trending": true, "site": true, "contact": true, "customer-stories": true, "readme": true, "solutions": true, "resources": true}
)

// DetectSites is the brand's sites, from the links its pages make: the host itself (marketing),
// docs., blog. and community subdomains and /docs, /blog and /community paths on it, docs on
// readthedocs.io or gitbook.io named after it, and GitHub repositories and owners named after it or
// its domain (code, at most three).
func DetectSites(host, name string, links []*url.URL) []config.Site {
	out := []config.Site{{URL: host, Kind: "marketing"}}
	seen := map[string]bool{host: true}
	add := func(u, kind string) {
		if !seen[u] {
			seen[u] = true
			out = append(out, config.Site{URL: u, Kind: kind})
		}
	}
	label := domainLabel(host)
	ours := func(s string) bool {
		k := key(s)
		return label != "" && strings.Contains(k, label) || key(name) != "" && strings.Contains(k, key(name))
	}
	var repos, owners []string
	for _, l := range links {
		if l == nil || l.Scheme != "http" && l.Scheme != "https" {
			continue
		}
		h := strings.TrimPrefix(strings.ToLower(l.Hostname()), "www.")
		segs := strings.FieldsFunc(l.Path, func(r rune) bool { return r == '/' })
		switch {
		case h == host:
			if len(segs) > 0 {
				if k, ok := pathKinds[strings.ToLower(segs[0])]; ok {
					add(host+"/"+strings.ToLower(segs[0]), k)
				}
			}
		case strings.HasSuffix(h, "."+host):
			sub := strings.TrimSuffix(h, "."+host)
			if k, ok := subKinds[sub]; ok {
				add(h, k)
			}
		case (strings.HasSuffix(h, ".readthedocs.io") || strings.HasSuffix(h, ".gitbook.io")) && label != "" && strings.Contains(key(h), label):
			add(h, "docs")
		case h == "github.com" && len(segs) > 0 && !githubReserved[strings.ToLower(segs[0])]:
			owner := strings.ToLower(segs[0])
			if len(segs) > 1 {
				if r := strings.TrimSuffix(strings.ToLower(segs[1]), ".git"); ours(owner) || ours(r) {
					repos = appendNew(repos, owner+"/"+r)
				}
			} else if ours(owner) {
				owners = appendNew(owners, owner)
			}
		}
	}
	// A repository says more than its owner; an owner alone is kept when no repository of theirs
	// is linked.
	n := 0
	for _, r := range repos {
		if n < 3 {
			add("github.com/"+r, "code")
			n++
		}
	}
	for _, o := range owners {
		covered := false
		for _, r := range repos {
			covered = covered || strings.HasPrefix(r, o+"/")
		}
		if !covered && n < 3 {
			add("github.com/"+o, "code")
			n++
		}
	}
	return out
}

func appendNew(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}
