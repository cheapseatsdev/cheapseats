package report

import (
	"net/url"
	"regexp"
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

var (
	reviewHosts = []string{"g2.com", "capterra.com", "trustradius.com", "getapp.com", "softwareadvice.com",
		"trustpilot.com", "peerspot.com", "gartner.com/reviews", "producthunt.com/products"}
	docsHosts  = []string{"readthedocs.io", "readthedocs.org", "pkg.go.dev", "docs.rs", "developer.mozilla.org", "learn.microsoft.com"}
	repoHosts  = []string{"github.com", "gitlab.com", "codeberg.org", "bitbucket.org"}
	blogHosts  = []string{"medium.com", "dev.to", "substack.com", "hashnode.dev"}
	forumHosts = []string{"reddit.com", "quora.com", "lobste.rs", "discord.com"}

	comparisonText = regexp.MustCompile(`\bvs\b|\bversus\b|\bcompar(e|es|ed|ing|ison|isons)\b|\balternative to\b`)
	listicleText   = regexp.MustCompile(`\bbest\b|\btop \d+\b|\b\d+ (best|top)\b|\balternatives\b|\b(tools|apps|software|platforms) for\b`)
	reviewText     = regexp.MustCompile(`\breviews?\b`)
	dated          = regexp.MustCompile(`^(19|20)\d\d$`)
	nonWord        = regexp.MustCompile(`[^a-z0-9]+`)
	bestPractice   = regexp.MustCompile(`\bbest practices?\b`)
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
	text := " " + strings.TrimSpace(nonWord.ReplaceAllString(path+" "+strings.ToLower(title), " ")) + " "

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
	case comparisonText.MatchString(text):
		return Comparison
	case on(reviewHosts...):
		return ReviewPage
	case listicleText.MatchString(bestPractice.ReplaceAllString(text, " ")):
		return Listicle
	case reviewText.MatchString(text):
		return ReviewPage
	case seg("blog", "blogs", "news", "article", "articles", "post", "posts", "insights", "stories", "press"),
		len(segs) > 1 && dated.MatchString(segs[0]) || len(segs) > 2 && dated.MatchString(segs[1]),
		strings.HasPrefix(host, "blog.") || strings.HasPrefix(host, "news."), on(blogHosts...):
		return Article
	case len(segs) == 0, seg("pricing", "plans", "features", "feature", "product", "products", "solutions", "integrations", "enterprise"):
		return Product
	}
	return OtherPage
}
