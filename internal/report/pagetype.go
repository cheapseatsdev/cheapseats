package report

import (
	"net/url"
	"strings"
)

// Page types: what kind of page a cited or surfaced URL is, from its URL and title. Source classes
// say whose site it is; page types say what the page does, so a run can show that answers lean on
// listicles or comparisons whoever wrote them.
const (
	Listicle   = "listicle"   // "best X", "top 10", "alternatives to", "tools for"
	Comparison = "comparison" // "X vs Y", "compare", "alternative to"
	ReviewPage = "review"     // review sites' product pages, and pages titled as reviews
	Docs       = "docs"       // documentation and package references
	Forum      = "forum"      // threads, questions, issues and discussions
	Repo       = "repo"       // a code repository's front page
	Article    = "article"    // blog posts, news and dated articles
	Product    = "product"    // home, pricing and feature pages
	OtherPage  = "other"
)

// PageTypeOrder is the order the run page lists page types in.
var PageTypeOrder = []string{Listicle, Comparison, ReviewPage, Docs, Forum, Repo, Article, Product, OtherPage}

// knownPageType reports whether t is one of PageTypeOrder.
func knownPageType(t string) bool {
	for _, x := range PageTypeOrder {
		if x == t {
			return true
		}
	}
	return false
}

// PageTypeDefinitions say what each type is, in the words of the constants' comments: the
// criteria Jev is given for a page the rules below call other (the page-types pass).
var PageTypeDefinitions = map[string]string{
	Listicle:   "a list or roundup of several products or tools: best X, top 10, alternatives to X, tools for Y",
	Comparison: "a comparison of two or more named products: X vs Y, compare, an alternative to X",
	ReviewPage: "a review of one product, or a review site's page about one product",
	Docs:       "documentation, a manual, an API or package reference",
	Forum:      "a thread, question, issue or discussion whose content users wrote",
	Repo:       "a code repository's front page",
	Article:    "a blog post, news story or dated article on one subject",
	Product:    "a vendor's own page about its product: home, pricing, features, solutions",
	OtherPage:  "none of the others, or not enough information to tell",
}

var (
	reviewHosts = []string{"g2.com", "capterra.com", "trustradius.com", "getapp.com", "softwareadvice.com",
		"trustpilot.com", "peerspot.com", "gartner.com/reviews", "producthunt.com/products"}
	docsHosts  = []string{"readthedocs.io", "readthedocs.org", "pkg.go.dev", "docs.rs", "developer.mozilla.org", "learn.microsoft.com"}
	repoHosts  = []string{"github.com", "gitlab.com", "codeberg.org", "bitbucket.org"}
	blogHosts  = []string{"medium.com", "dev.to", "substack.com", "hashnode.dev"}
	forumHosts = []string{"reddit.com", "quora.com", "lobste.rs", "discord.com"}
)

// PageType sorts a page by its URL and title, first rule that matches: forum (Reddit and other
// discussion sites, Hacker News items, Stack Exchange questions, GitHub issues, pull requests and
// discussions, forum.* and community.* hosts), repo (a repository's root on GitHub and the like),
// docs (docs.* hosts, /docs/ and /documentation paths, Read the Docs, pkg.go.dev), then by what
// the path and title say: comparison (vs, versus, compare, alternative to; a review site's
// compare pages too), review (a review site's other pages), listicle (best, top N, N best,
// alternatives, tools for; not "best practices"), review again (titled or pathed "review"), then
// article (/blog/, /news/, a dated path, blog.* hosts, Medium),
// product (the home page, /pricing, /features, /product) and other.
func PageType(rawURL, title string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		u, err = url.Parse("https://" + strings.TrimSpace(rawURL))
		if err != nil {
			return OtherPage
		}
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path := strings.ToLower(strings.Trim(u.Path, "/"))
	var segs []string
	if path != "" {
		segs = strings.Split(path, "/")
	}
	seg := func(names ...string) bool {
		for _, s := range segs {
			for _, n := range names {
				if s == n {
					return true
				}
			}
		}
		return false
	}
	on := func(hosts ...string) bool {
		for _, h := range hosts {
			hp := h
			if i := strings.Index(h, "/"); i >= 0 { // host/path prefix
				if !strings.HasPrefix(host+"/"+path+"/", h+"/") && host+"/"+path != h {
					continue
				}
				hp = h[:i]
			}
			if host == hp || strings.HasSuffix(host, "."+hp) {
				return true
			}
		}
		return false
	}
	// The path and title as lowercase words, so that a phrase test is a whole-word test and the
	// rules below need no regular expressions (a report runs them on thousands of pages).
	words := strings.FieldsFunc(strings.ToLower(path+" "+title), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') })
	text := " " + strings.Join(words, " ") + " "
	phrase := func(ps ...string) bool {
		for _, p := range ps {
			if strings.Contains(text, " "+p+" ") {
				return true
			}
		}
		return false
	}

	switch {
	case on(forumHosts...), host == "news.ycombinator.com" && seg("item"),
		on("stackoverflow.com", "stackexchange.com", "superuser.com", "serverfault.com", "askubuntu.com") && seg("questions", "q", "a"),
		on(repoHosts...) && len(segs) >= 3 && seg("issues", "discussions", "pull", "pulls", "merge_requests"),
		strings.HasPrefix(host, "forum.") || strings.HasPrefix(host, "forums.") || strings.HasPrefix(host, "community.") ||
			strings.HasPrefix(host, "discuss.") || strings.HasPrefix(host, "discourse."):
		return Forum
	case on(repoHosts...) && len(segs) == 2:
		return Repo
	case strings.HasPrefix(host, "docs.") || strings.HasPrefix(host, "doc.") || on(docsHosts...) ||
		seg("docs", "doc", "documentation", "reference", "api-reference"):
		return Docs
	case phrase("vs", "versus", "compare", "compares", "compared", "comparing", "comparison", "comparisons", "alternative to"):
		return Comparison
	case on(reviewHosts...):
		return ReviewPage
	case listicle(words):
		return Listicle
	case phrase("review", "reviews"):
		return ReviewPage
	case seg("blog", "blogs", "news", "article", "articles", "post", "posts", "insights", "stories", "press"),
		len(segs) > 1 && dated(segs[0]) || len(segs) > 2 && dated(segs[1]),
		strings.HasPrefix(host, "blog.") || strings.HasPrefix(host, "news."), on(blogHosts...):
		return Article
	case len(segs) == 0, seg("pricing", "plans", "features", "feature", "product", "products", "solutions", "integrations", "enterprise"):
		return Product
	}
	return OtherPage
}

// listicle reports a list or roundup by its words: best (but not best practices), top N, N best
// or N top, alternatives, or tools, apps, software or platforms for something.
func listicle(words []string) bool {
	next := func(i int) string {
		if i+1 < len(words) {
			return words[i+1]
		}
		return ""
	}
	bestOf := func(i int) bool { return words[i] == "best" && next(i) != "practice" && next(i) != "practices" }
	for i, w := range words {
		switch {
		case bestOf(i), w == "alternatives":
			return true
		case w == "top" && digits(next(i)):
			return true
		case digits(w) && (next(i) == "top" || i+1 < len(words) && bestOf(i+1)):
			return true
		case (w == "tools" || w == "apps" || w == "software" || w == "platforms") && next(i) == "for":
			return true
		}
	}
	return false
}

// dated reports a year path segment, 1900 to 2099.
func dated(seg string) bool {
	return len(seg) == 4 && (seg[:2] == "19" || seg[:2] == "20") && digits(seg)
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
