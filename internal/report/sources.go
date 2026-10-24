package report

import (
	"slices"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// Source classes: what kind of site a page an answer drew on is. Own and competitor come from the
// config (the brand's sites, competitors' domains); the rest from Classes and [sources].
const (
	Own           = "own"
	Competitor    = "competitor"
	UGC           = "ugc"           // forums, Q&A, video and social: what users write
	Review        = "review"        // software review and comparison sites
	Reference     = "reference"     // encyclopedias, documentation, package registries
	Institutional = "institutional" // government, academic and intergovernmental
	Editorial     = "editorial"     // everything else: publishers, blogs, vendors
)

// ClassOrder is the order the run page lists classes in.
var ClassOrder = []string{Own, Competitor, UGC, Review, Reference, Institutional, Editorial}

// ClassDefinitions say what each third-party class is, in the words of the constants' comments:
// the criteria Jev is given for a host the lists below would call editorial (the page-types
// pass). Own and competitor come from the config and are never asked about.
var ClassDefinitions = map[string]string{
	UGC:           "a forum, Q&A, video or social site: what users write",
	Review:        "a software review and comparison site",
	Reference:     "an encyclopedia, documentation or a package registry",
	Institutional: "a government, academic or intergovernmental body",
	Editorial:     "everything else: a publisher, a blog or a vendor",
}

// Classes are the built-in hosts (or host/path prefixes) of each class; a host covers its
// subdomains. [sources] in cheapseats.toml adds to them and wins over them.
var Classes = map[string][]string{
	UGC: {"reddit.com", "youtube.com", "youtu.be", "stackoverflow.com", "stackexchange.com", "superuser.com",
		"serverfault.com", "askubuntu.com", "news.ycombinator.com", "quora.com", "medium.com", "dev.to",
		"hashnode.dev", "x.com", "twitter.com", "linkedin.com", "facebook.com", "instagram.com", "tiktok.com",
		"threads.net", "discord.com", "lobste.rs", "gist.github.com", "community.openai.com", "indiehackers.com"},
	Review: {"g2.com", "capterra.com", "trustradius.com", "producthunt.com", "gartner.com/reviews",
		"getapp.com", "softwareadvice.com", "trustpilot.com", "peerspot.com", "alternativeto.net",
		"saasworthy.com", "sourceforge.net/software"},
	Reference: {"wikipedia.org", "wikimedia.org", "wiktionary.org", "developer.mozilla.org", "readthedocs.io",
		"readthedocs.org", "pkg.go.dev", "npmjs.com", "pypi.org", "crates.io", "docs.rs", "w3.org",
		"ietf.org", "rfc-editor.org", "github.com"},
}

// Classifier sorts URLs into source classes.
type Classifier struct {
	brand       config.Brand
	competitors []competitorSite
	overrides   siteIndex
}

// competitorSite is one of a competitor's domains, cleaned once for matching.
type competitorSite struct{ name, site string }

func NewClassifier(brand config.Brand, competitors []sets.Competitor, overrides map[string][]string) Classifier {
	c := Classifier{brand: brand, overrides: indexSites(overrides)}
	for _, comp := range competitors {
		for _, d := range comp.Domains {
			c.competitors = append(c.competitors, competitorSite{comp.Name, config.CleanSite(d)})
		}
	}
	return c
}

// Class is the URL's source class, and for a competitor's page, which competitor. The brand's
// sites come first, then competitors' domains, then [sources], then the built-in lists, then the
// rules: government and academic domains are institutional, docs.* hosts reference, and
// everything else editorial. Within the lists the most specific entry wins, so gartner.com/reviews
// beats gartner.com. Issues and discussions on github.com are ugc; the rest of it is reference.
func (c Classifier) Class(u string) (class, competitor string) {
	class, competitor, _ = c.classify(u)
	return class, competitor
}

// classify is Class, and the host the URL counts under: its host, or the brand's or a
// competitor's site when that's a host/path (github.com/acme), so the site is its own row.
func (c Classifier) classify(u string) (class, competitor, host string) {
	host = Host(NormalizeURL(u))
	if site, ok := c.brand.SiteOf(u); ok {
		if d := config.CleanSite(site.URL); strings.Contains(d, "/") {
			host = d
		}
		return Own, "", host
	}
	cu := config.CleanSite(u)
	best, bestSite := -1, ""
	for _, comp := range c.competitors {
		if n := config.MatchCleanSite(comp.site, cu); n > best {
			best, bestSite, competitor = n, comp.site, comp.name
		}
	}
	if best >= 0 {
		if strings.Contains(bestSite, "/") {
			host = bestSite
		}
		return Competitor, competitor, host
	}
	if cl := c.overrides.longest(cu); slices.Contains(ClassOrder, cl) {
		return cl, "", host
	}
	if host == "github.com" {
		for _, seg := range strings.Split(strings.ToLower(u), "/") {
			if seg == "issues" || seg == "discussions" {
				return UGC, "", host
			}
		}
	}
	if cl := builtin.longest(cu); cl != "" {
		return cl, "", host
	}
	if institutional(host) {
		return Institutional, "", host
	}
	if strings.HasPrefix(host, "docs.") {
		return Reference, "", host
	}
	return Editorial, "", host
}

// siteIndex is class lists (class -> sites) keyed by each site's host, so finding the entries that
// can match a URL walks its host's suffixes (docs.example.com, example.com, com) rather than every
// entry. A run's report classifies thousands of URLs against the hundreds of hosts Jev has classed.
type siteIndex map[string][]siteEntry

type siteEntry struct{ site, class string } // site as config.CleanSite leaves it

var builtin = indexSites(Classes)

func indexSites(lists map[string][]string) siteIndex {
	ix := siteIndex{}
	for cl, sites := range lists {
		for _, s := range sites {
			d := config.CleanSite(s)
			if d == "" {
				continue
			}
			h := d
			if i := strings.IndexAny(h, "/?#"); i >= 0 {
				h = h[:i]
			}
			ix[h] = append(ix[h], siteEntry{d, cl})
		}
	}
	return ix
}

// longest is the class whose entry matches the URL (already cleaned) most specifically, or "":
// the longest matching entry, and of two the same length, the class that sorts first.
func (ix siteIndex) longest(cu string) string {
	host := cu
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	class, best := "", -1
	for h := host; ; {
		for _, e := range ix[h] {
			if n := config.MatchCleanSite(e.site, cu); n > best || n == best && n >= 0 && e.class < class {
				class, best = e.class, n
			}
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return class
}

// institutional reports a government, military, academic or intergovernmental host: .gov, .edu,
// .mil and .int, and their country forms (gov.uk, ac.uk, edu.au).
func institutional(host string) bool {
	host = strings.ToLower(host)
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	labels := strings.Split(host, ".")
	n := len(labels)
	if n < 2 {
		return false
	}
	switch labels[n-1] {
	case "gov", "edu", "mil", "int":
		return true
	}
	if len(labels[n-1]) == 2 {
		switch labels[n-2] {
		case "gov", "edu", "ac", "mil":
			return true
		}
	}
	return false
}
