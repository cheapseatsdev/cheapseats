// Package audit checks what AI assistants' crawlers can read on the brand's sites, with plain HTTP
// and no model calls: whether robots.txt or a firewall shuts them out, whether pages say what they
// are without JavaScript (title, description, headings, structured data), and whether llms.txt and
// a current sitemap point them at the rest. It also checks the third-party pages answers cite, for
// whether they name the brand (citations.go).
package audit

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cheapseatsdev/cheapseats/internal/bots"
	"github.com/cheapseatsdev/cheapseats/internal/version"
)

type Severity string

const (
	Error   Severity = "error"   // AI crawlers can't read the site, or can't tell what a page is
	Warning Severity = "warning" // they can, but miss something that helps them
	Info    Severity = "info"    // worth knowing; often a choice
)

var severityOrder = map[Severity]int{Error: 0, Warning: 1, Info: 2}

// Finding is one problem on one site or page, with what to do about it.
type Finding struct {
	Severity Severity `json:"severity"`
	Site     string   `json:"site"`
	URL      string   `json:"url"`
	Check    string   `json:"check"`
	Message  string   `json:"message"`
	Fix      string   `json:"fix"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%s %s %s: %s", f.Severity, f.Check, f.URL, f.Message)
}

// BotAccess is one crawler's way into a site: what robots.txt says, and what the home page
// answered when fetched with its user agent (0 when not tried).
type BotAccess struct {
	Bot      string     `json:"bot"`
	Operator string     `json:"operator"`
	Class    bots.Class `json:"class"`
	Allowed  bool       `json:"allowed"`
	Named    bool       `json:"named"` // robots.txt has a group for it rather than only *
	Status   int        `json:"status,omitempty"`
	Blocked  bool       `json:"blocked,omitempty"` // the firewall refused it or served a challenge
}

// Page is one page checked.
type Page struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	Error  string `json:"error,omitempty"`
	HTML   bool   `json:"html"`
	Doc
}

// SiteResult is one site's audit.
type SiteResult struct {
	Site         string      `json:"site"`
	Home         string      `json:"home"`
	RobotsStatus int         `json:"robots_status"` // 0 when it couldn't be fetched
	Bots         []BotAccess `json:"bots"`
	LLMsStatus   int         `json:"llms_status,omitempty"`
	Sitemap      string      `json:"sitemap,omitempty"`
	SitemapURLs  int         `json:"sitemap_urls"`
	// The sitemap is an index listing more sitemaps than were read: SitemapURLs is a lower bound.
	SitemapPartial bool   `json:"sitemap_partial,omitempty"`
	Newest         string `json:"sitemap_newest,omitempty"` // the newest lastmod, as a date
	Pages          []Page `json:"pages"`
}

// Result is one audit of every site.
type Result struct {
	At       string       `json:"at"`
	Version  string       `json:"version"`
	Sites    []SiteResult `json:"sites"`
	Findings []Finding    `json:"findings"`
}

func (r Result) Errors() int   { return r.Count(Error) }
func (r Result) Warnings() int { return r.Count(Warning) }
func (r Result) Infos() int    { return r.Count(Info) }

// Count is how many findings have a severity.
func (r Result) Count(s Severity) int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == s {
			n++
		}
	}
	return n
}

type Options struct {
	Fetcher *Fetcher
	// Pages from the sitemap to check besides the home page (10 when 0, none when negative).
	Pages int
	Now   func() time.Time
	// Sites on a platform the brand doesn't control (its GitHub, a forum): their pages are
	// fetched and crawler access recorded, but the host's robots.txt, firewall and markup aren't
	// the brand's to fix, so they raise no findings.
	Hosted map[string]bool
}

// Run audits each site: a host (example.com) or a host and path (github.com/example), with or
// without a scheme (https when none). A path on a host is the brand's part of someone else's site,
// so its llms.txt and sitemap, which belong to the host, aren't checked.
func Run(ctx context.Context, sites []string, o Options) Result {
	if o.Fetcher == nil {
		o.Fetcher = NewFetcher(nil, 4)
	}
	if o.Pages == 0 {
		o.Pages = 10
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	res := Result{At: o.Now().UTC().Format(time.RFC3339), Version: version.String(), Sites: make([]SiteResult, len(sites)), Findings: []Finding{}}
	found := make([][]Finding, len(sites))
	each(len(sites), func(i int) {
		a := &siteAudit{o: o, site: sites[i]}
		a.run(ctx)
		res.Sites[i], found[i] = a.res, a.findings
	})
	for _, f := range found {
		res.Findings = append(res.Findings, f...)
	}
	sort.SliceStable(res.Findings, func(i, j int) bool {
		x, y := res.Findings[i], res.Findings[j]
		if x.Site != y.Site {
			return x.Site < y.Site
		}
		if x.Severity != y.Severity {
			return severityOrder[x.Severity] < severityOrder[y.Severity]
		}
		if x.Check != y.Check {
			return x.Check < y.Check
		}
		return x.URL < y.URL
	})
	return res
}

type siteAudit struct {
	o        Options
	site     string
	base     string // scheme://host
	prefix   string // the site's path on the host, "" for the whole host
	res      SiteResult
	robots   *Robots
	mu       sync.Mutex // pages are checked at once
	findings []Finding
}

func (a *siteAudit) add(sev Severity, check, u, msg, fix string) {
	if a.o.Hosted[a.site] && !strings.HasPrefix(check, "page.") && check != "site.invalid" {
		return // the host's robots.txt, firewall, llms.txt and sitemap: not the brand's to fix (bot access is still recorded in Bots)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.findings = append(a.findings, Finding{Severity: sev, Site: a.site, URL: u, Check: check, Message: msg, Fix: fix})
}

// siteURL splits a site into its scheme and host, and its path ("" for the whole host).
func siteURL(site string) (base, prefix string, err error) {
	s := strings.TrimSpace(site)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("not a site: %q", site)
	}
	return u.Scheme + "://" + u.Host, strings.TrimSuffix(u.Path, "/"), nil
}

func (a *siteAudit) run(ctx context.Context) {
	a.res = SiteResult{Site: a.site, Bots: []BotAccess{}, Pages: []Page{}}
	var err error
	a.base, a.prefix, err = siteURL(a.site)
	if err != nil {
		a.add(Error, "site.invalid", a.site, err.Error(), "List the site as a host (example.com) or a host and path (github.com/example).")
		return
	}
	a.res.Home = a.base + a.prefix + "/"
	if a.prefix != "" {
		a.res.Home = a.base + a.prefix
	}
	a.checkRobots(ctx)
	home := a.checkHome(ctx)
	if a.prefix == "" {
		a.checkLLMs(ctx)
	}
	var sample []string
	if a.prefix == "" {
		sample = a.checkSitemap(ctx)
	}
	pages := make([]Page, len(sample))
	each(len(sample), func(i int) { pages[i] = a.page(ctx, sample[i], nil) })
	if home != nil {
		a.res.Pages = append(a.res.Pages, *home)
	}
	a.res.Pages = append(a.res.Pages, pages...)
}

// checkRobots reads robots.txt and what it allows each AI crawler.
func (a *siteAudit) checkRobots(ctx context.Context) {
	u := a.base + "/robots.txt"
	r, err := a.o.Fetcher.Get(ctx, u)
	switch {
	case err != nil:
		a.add(Warning, "robots.unreachable", u, "robots.txt couldn't be fetched: "+err.Error(), "Make sure /robots.txt answers quickly over HTTPS.")
	case r.Status >= 500:
		a.res.RobotsStatus = r.Status
		a.add(Error, "robots.server-error", u, fmt.Sprintf("robots.txt answered %d. Crawlers read a server error there as \"fetch nothing\".", r.Status), "Serve /robots.txt with a 200, or a 404 if you don't want one.")
	case r.Status != 200:
		a.res.RobotsStatus = r.Status
		a.add(Info, "robots.missing", u, fmt.Sprintf("No robots.txt (%d): every crawler may fetch everything.", r.Status), "Optional. Add one if you want to point crawlers at your sitemap.")
	default:
		a.res.RobotsStatus = r.Status
		a.robots = ParseRobots(string(r.Body))
	}
	path := a.prefix + "/"
	for _, b := range bots.Audited() {
		ok, named := a.robots.Allowed(b.Token, path)
		a.res.Bots = append(a.res.Bots, BotAccess{Bot: b.Token, Operator: b.Operator, Class: b.Class, Allowed: ok, Named: named})
		if ok {
			continue
		}
		rule := "User-agent: *"
		if named {
			rule = "User-agent: " + b.Token
		}
		switch b.Class {
		case bots.Assistant, bots.Search:
			a.add(Error, "robots.blocks-ai-search", u, fmt.Sprintf("robots.txt (%s) blocks %s from %s. %s's assistant can't read or cite this site when it searches.", rule, b.Token, path, b.Operator),
				fmt.Sprintf("Allow it: add \"User-agent: %s\" and \"Allow: /\" to robots.txt.", b.Token))
		case bots.Engine:
			a.add(Error, "robots.blocks-search-engine", u, fmt.Sprintf("robots.txt (%s) blocks %s from %s. Assistants that search the web through %s's index won't find this site.", rule, b.Token, path, b.Operator),
				fmt.Sprintf("Allow it: add \"User-agent: %s\" and \"Allow: /\" to robots.txt.", b.Token))
		case bots.Training:
			a.add(Info, "robots.blocks-training", u, fmt.Sprintf("robots.txt (%s) blocks %s, %s's training crawler, from %s. That's a choice: it keeps pages out of future models' training data, not out of search answers.", rule, b.Token, b.Operator, path),
				"Nothing to do unless you want future models to learn from these pages.")
		}
	}
}

// challenge reports whether a response is a bot challenge rather than the page: a Cloudflare
// managed challenge, a CAPTCHA, or a "checking your browser" interstitial.
func challenge(r *Response) bool {
	if r.Header.Get("cf-mitigated") == "challenge" {
		return true
	}
	head := strings.ToLower(string(r.Body[:min(len(r.Body), 16<<10)]))
	for _, s := range []string{"challenge-platform", "cf-chl-", "just a moment...", "checking your browser", "captcha-delivery", "g-recaptcha", "hcaptcha.com", "px-captcha", "_incapsula_resource"} {
		if strings.Contains(head, s) {
			return true
		}
	}
	return false
}

// checkHome fetches the home page as Cheap Seats, then as each AI crawler that fetches pages: one
// refused when Cheap Seats was let in is blocked by a firewall, whatever robots.txt says.
func (a *siteAudit) checkHome(ctx context.Context) *Page {
	r, err := a.o.Fetcher.Get(ctx, a.res.Home)
	p := a.page(ctx, a.res.Home, &fetched{r, err})
	if err != nil || r.Status != 200 || challenge(r) {
		return &p // already reported by page; with no baseline there's nothing to compare
	}
	var withUA []int
	for i, b := range a.res.Bots {
		if bot := botByToken(b.Bot); bot.UA != "" {
			withUA = append(withUA, i)
		}
	}
	each(len(withUA), func(k int) {
		i := withUA[k]
		b := &a.res.Bots[i]
		br, err := a.o.Fetcher.Get(ctx, a.res.Home, botByToken(b.Bot).UA)
		if err != nil {
			return
		}
		b.Status = br.Status
		b.Blocked = br.Status == 401 || br.Status == 403 || br.Status == 429 || challenge(br)
	})
	for _, b := range a.res.Bots {
		if !b.Blocked {
			continue
		}
		what := fmt.Sprintf("answered %d", b.Status)
		if b.Status == 200 {
			what = "served a challenge page"
		}
		a.add(Error, "waf.blocks-ai-bot", a.res.Home, fmt.Sprintf("The home page %s to %s's user agent, though it answered 200 to a browser's. %s can't read the site when someone asks %s about it.", what, b.Bot, b.Bot, b.Operator),
			fmt.Sprintf("Allow %s in your firewall or CDN bot settings (Cloudflare: Security > Bots, AI Crawl Control). If they let only verified bots through by IP, the real %s may get in where this check, which only sends its user agent, didn't: check the firewall's log for it.", b.Bot, b.Bot))
	}
	return &p
}

func botByToken(t string) bots.Bot {
	for _, b := range bots.All {
		if b.Token == t {
			return b
		}
	}
	return bots.Bot{}
}

var mdLink = regexp.MustCompile(`\[[^\]]+\]\([^)\s]+\)`)

// checkLLMs looks for /llms.txt (llmstxt.org): a markdown index of the site for language models.
func (a *siteAudit) checkLLMs(ctx context.Context) {
	u := a.base + "/llms.txt"
	r, err := a.o.Fetcher.Get(ctx, u)
	fix := "Publish /llms.txt: a markdown file starting with \"# <site name>\", a one-line summary in a > quote, then sections of [title](url): description links to your key pages."
	if err != nil {
		a.add(Warning, "llms.missing", u, "llms.txt couldn't be fetched: "+err.Error(), fix)
		return
	}
	a.res.LLMsStatus = r.Status
	body := strings.TrimPrefix(strings.TrimSpace(string(r.Body)), "\ufeff")
	ct := r.ContentType()
	if r.Status != 200 {
		a.add(Warning, "llms.missing", u, fmt.Sprintf("No llms.txt (%d).", r.Status), fix)
		return
	}
	if strings.Contains(ct, "html") || looksHTML(r.Body) {
		a.add(Warning, "llms.missing", u, "/llms.txt returns an HTML page (a catch-all route), not an llms.txt.", fix)
		return
	}
	if !strings.HasPrefix(ct, "text/plain") && !strings.Contains(ct, "markdown") {
		a.add(Warning, "llms.content-type", u, fmt.Sprintf("llms.txt is served as %q.", ct), "Serve it as text/plain or text/markdown.")
	}
	if !strings.HasPrefix(body, "# ") {
		a.add(Warning, "llms.heading", u, "llms.txt doesn't start with a \"# \" heading naming the site.", "Start it with \"# <site name>\".")
	}
	if !mdLink.MatchString(body) {
		a.add(Warning, "llms.no-links", u, "llms.txt has no markdown links.", "List your key pages as [title](url) links, so a model can go straight to them.")
	}
}

// checkSitemap reads the sitemaps robots.txt lists (else /sitemap.xml), reports their size and
// freshness, and returns a sample of the pages in them to check.
func (a *siteAudit) checkSitemap(ctx context.Context) []string {
	var tries []string
	if a.robots != nil {
		for _, s := range a.robots.Sitemaps {
			if a.onSite(s) {
				tries = append(tries, s)
			}
		}
	}
	if len(tries) == 0 {
		tries = []string{a.base + "/sitemap.xml"}
	}
	var urls []sitemapEntry
	var children []sitemapEntry
	found := false
	for _, u := range tries[:min(len(tries), 3)] {
		r, err := a.o.Fetcher.Get(ctx, u)
		if err != nil || r.Status != 200 {
			continue
		}
		us, cs, err := parseSitemap(r.Body)
		if err != nil {
			a.add(Error, "sitemap.invalid", u, "The sitemap doesn't parse: "+err.Error(), "Serve valid sitemap XML (sitemaps.org): a <urlset> of <url><loc> entries, or a <sitemapindex>.")
			continue
		}
		found = true
		if a.res.Sitemap == "" {
			a.res.Sitemap = u
		}
		urls, children = append(urls, us...), append(children, cs...)
	}
	// An index: read the first few sitemaps it lists.
	for _, c := range children[:min(len(children), 5)] {
		r, err := a.o.Fetcher.Get(ctx, c.Loc)
		if err != nil || r.Status != 200 {
			continue
		}
		if us, _, err := parseSitemap(r.Body); err == nil {
			urls = append(urls, us...)
		}
	}
	if !found {
		a.add(Warning, "sitemap.missing", tries[0], "No sitemap found (none listed in robots.txt, and nothing at /sitemap.xml).", "Publish /sitemap.xml listing every page with its <lastmod>, and add \"Sitemap: <url>\" to robots.txt.")
		return nil
	}
	a.res.SitemapURLs = len(urls)
	if len(children) > 5 {
		a.res.SitemapPartial = true
	}
	if len(urls) == 0 {
		a.add(Warning, "sitemap.empty", a.res.Sitemap, "The sitemap lists no pages.", "List every public page in it.")
		return nil
	}
	var newest time.Time
	for _, u := range append(urls, children...) {
		if u.LastMod.After(newest) {
			newest = u.LastMod
		}
	}
	if newest.IsZero() {
		a.add(Info, "sitemap.no-lastmod", a.res.Sitemap, "The sitemap gives no <lastmod> dates, so crawlers can't tell which pages changed.", "Add <lastmod> to each <url>, from the page's real last change.")
	} else {
		a.res.Newest = newest.UTC().Format("2006-01-02")
		if age := a.o.Now().Sub(newest); age > 180*24*time.Hour {
			a.add(Warning, "sitemap.stale", a.res.Sitemap, fmt.Sprintf("The newest <lastmod> in the sitemap is %s, %d days ago.", a.res.Newest, int(age.Hours()/24)), "Regenerate the sitemap when pages change, with each page's real <lastmod>.")
		}
	}
	// The sample: evenly spread through the sitemap, so it reaches more than one section.
	var on []string
	seen := map[string]bool{} // a sitemap can be huge: linear, not a scan of on per entry
	for _, u := range urls {
		if a.onSite(u.Loc) && strings.TrimSuffix(u.Loc, "/") != strings.TrimSuffix(a.res.Home, "/") && !seen[u.Loc] {
			seen[u.Loc] = true
			on = append(on, u.Loc)
		}
	}
	n := a.o.Pages
	if n < 0 {
		n = 0
	}
	if len(on) <= n {
		return on
	}
	var sample []string
	for i := 0; i < n; i++ {
		sample = append(sample, on[i*len(on)/n])
	}
	return sample
}

// onSite reports whether a URL is on this site's host (www. or not).
func (a *siteAudit) onSite(raw string) bool {
	u, err := url.Parse(raw)
	b, _ := url.Parse(a.base)
	if err != nil || b == nil {
		return false
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.") == strings.TrimPrefix(strings.ToLower(b.Host), "www.") && strings.HasPrefix(u.Path, a.prefix)
}

type fetched struct {
	r   *Response
	err error
}

// page checks one page: fetched here, or already (f).
func (a *siteAudit) page(ctx context.Context, u string, f *fetched) Page {
	if f == nil {
		r, err := a.o.Fetcher.Get(ctx, u)
		f = &fetched{r, err}
	}
	p := Page{URL: u}
	if f.err != nil {
		p.Error = f.err.Error()
		a.add(Error, "page.unreachable", u, "The page couldn't be fetched: "+f.err.Error(), "Make sure it answers over HTTPS within a few seconds.")
		return p
	}
	r := f.r
	p.Status = r.Status
	if r.Status != 200 {
		a.add(Error, "page.status", u, fmt.Sprintf("The page answered %d to a browser's user agent.", r.Status), "Serve it with a 200, or drop it from the sitemap if it's gone.")
		return p
	}
	if challenge(r) {
		a.add(Error, "page.challenge", u, "The page served a bot challenge (CAPTCHA or \"checking your browser\") instead of its content.", "Let crawlers and plain HTTP clients through to public pages in your firewall or CDN settings.")
		return p
	}
	ct := r.ContentType()
	p.HTML = strings.Contains(ct, "html") || ct == "" && looksHTML(r.Body)
	if !p.HTML || a.o.Hosted[a.site] {
		return p
	}
	p.Doc = ParseDoc(r.Body)
	d := p.Doc
	if strings.Contains(strings.ToLower(r.Header.Get("x-robots-tag")), "noindex") {
		d.Noindex, p.Noindex = true, true
	}
	switch n := utf8.RuneCountInString(d.Title); {
	case n == 0:
		a.add(Warning, "page.title-missing", u, "The page has no <title>.", "Give it a <title> that says what it is, in the words buyers search with.")
	case n > 60:
		a.add(Warning, "page.title-long", u, fmt.Sprintf("The <title> is %d characters; past about 60 it gets cut off.", n), "Shorten it to 60 characters or fewer, the key words first.")
	}
	switch n := utf8.RuneCountInString(d.Description); {
	case n == 0:
		a.add(Warning, "page.description-missing", u, "The page has no meta description.", "Add <meta name=\"description\"> with a 50-160 character summary that answers what the page is for.")
	case n < 50 || n > 160:
		a.add(Warning, "page.description-length", u, fmt.Sprintf("The meta description is %d characters; 50-160 is what search results and assistants quote.", n), "Rewrite it as a 50-160 character summary.")
	}
	if d.H1 != 1 {
		a.add(Warning, "page.h1", u, fmt.Sprintf("The page has %d <h1> headings; one says what it's about.", d.H1), "Give it exactly one <h1>.")
	}
	if d.Lang == "" {
		a.add(Warning, "page.lang", u, "The <html> element has no lang attribute.", "Add lang=\"en\" (or the page's language) to <html>.")
	}
	if d.Noindex {
		a.add(Error, "page.noindex", u, "The page asks not to be indexed (noindex in a robots meta tag or X-Robots-Tag), so search indexes, assistants' included, leave it out.", "Remove noindex from public pages.")
	}
	if d.Canonical == "" {
		a.add(Warning, "page.canonical", u, "The page has no canonical link.", "Add <link rel=\"canonical\" href=\"<its URL>\">, so copies of it count as one page.")
	}
	if d.BadJSONLD > 0 {
		a.add(Error, "page.jsonld-invalid", u, fmt.Sprintf("%d JSON-LD block(s) don't parse, so their structured data is ignored.", d.BadJSONLD), "Fix the JSON in each <script type=\"application/ld+json\">.")
	} else if len(d.JSONLD) == 0 {
		a.add(Warning, "page.jsonld-missing", u, "The page has no JSON-LD structured data.", "Add schema.org JSON-LD that says what it is: Organization and WebSite on the home page, SoftwareApplication or Product for the product, Article or TechArticle for guides, FAQPage for FAQs.")
	}
	if !d.OGTitle || !d.OGDesc {
		a.add(Warning, "page.open-graph", u, "The page is missing og:title or og:description.", "Add <meta property=\"og:title\"> and <meta property=\"og:description\">.")
	}
	if d.TextChars < 200 {
		if d.AppRoot != "" {
			a.add(Error, "page.needs-js", u, fmt.Sprintf("Without JavaScript the page is an empty app shell (<div id=%q>, %d characters of text). AI crawlers don't run JavaScript, so they see nothing.", d.AppRoot, d.TextChars), "Render pages on the server or pre-render them at build time (SSR or SSG), so the HTML carries the content.")
		} else {
			a.add(Warning, "page.thin", u, fmt.Sprintf("The page's HTML has only %d characters of text.", d.TextChars), "Make sure the content is in the HTML, not loaded by JavaScript afterwards.")
		}
	}
	return p
}
