package audit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRobots(t *testing.T) {
	const txt = `# a comment
User-agent: *
Disallow: /private
Allow: /private/ok$

User-agent: GPTBot
User-agent: CCBot
Disallow: /

User-agent: OAI-SearchBot
Disallow: /*.pdf$
Allow: /

Sitemap: https://example.com/sitemap.xml
`
	r := ParseRobots(txt)
	for _, c := range []struct {
		token, path    string
		allowed, named bool
	}{
		{"Claude-SearchBot", "/", true, false},
		{"Claude-SearchBot", "/private/x", false, false},
		{"Claude-SearchBot", "/private/ok", true, false},
		{"Claude-SearchBot", "/private/ok/more", false, false},
		{"gptbot", "/", false, true}, // tokens ignore case; a group can name several
		{"CCBot", "/docs", false, true},
		{"OAI-SearchBot", "/private", true, true}, // its own group replaces *'s
		{"OAI-SearchBot", "/a/report.pdf", false, true},
		{"OAI-SearchBot", "/a/report.pdf?x", true, true},
	} {
		got, named := r.Allowed(c.token, c.path)
		if got != c.allowed || named != c.named {
			t.Errorf("%s %s: allowed %v named %v, want %v %v", c.token, c.path, got, named, c.allowed, c.named)
		}
	}
	if len(r.Sitemaps) != 1 || r.Sitemaps[0] != "https://example.com/sitemap.xml" {
		t.Errorf("sitemaps: %v", r.Sitemaps)
	}
	if ok, _ := ParseRobots("User-agent: *\nDisallow:\n").Allowed("GPTBot", "/"); !ok {
		t.Error("an empty Disallow allows everything")
	}
	if ok, _ := (*Robots)(nil).Allowed("GPTBot", "/"); !ok {
		t.Error("no robots.txt allows everything")
	}
}

func TestParseDoc(t *testing.T) {
	body := `<!doctype html><html lang="en"><head><title> Cheap Seats:  AI visibility </title>
<meta name="description" content="Track how AI assistants answer your buyers.">
<meta name="robots" content="index, follow"><link rel="canonical" href="https://cheapseats.dev/">
<meta property="og:title" content="Cheap Seats"><meta property="og:description" content="x">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"Organization"},{"@type":["WebSite","Thing"]}]}</script>
<script type="application/ld+json">{not json</script><style>body{}</style></head>
<body><h1>Cheap Seats</h1><svg><title>logo</title></svg><script>var hidden = "no";</script><p>Visible   text.</p></body></html>`
	d := ParseDoc([]byte(body))
	if d.Title != "Cheap Seats: AI visibility" || d.Lang != "en" || d.H1 != 1 || d.Canonical != "https://cheapseats.dev/" || d.Noindex || !d.OGTitle || !d.OGDesc {
		t.Errorf("%+v", d)
	}
	if strings.Join(d.JSONLD, ",") != "Organization,Thing,WebSite" || d.BadJSONLD != 1 {
		t.Errorf("JSON-LD: %v, %d invalid", d.JSONLD, d.BadJSONLD)
	}
	if d.Text != "Cheap Seats Visible text." {
		t.Errorf("text: %q", d.Text)
	}
	spa := ParseDoc([]byte(`<html><head><meta name="robots" content="NOINDEX"></head><body><div id="root"></div><script src="/app.js"></script></body></html>`))
	if spa.AppRoot != "root" || spa.TextChars != 0 || !spa.Noindex {
		t.Errorf("spa: %+v", spa)
	}
}

// site is a fake site for the audit: its files by path, and the user agents its firewall refuses.
type site struct {
	files   map[string]string // path -> body; a path ending in .txt is text/plain, .xml XML, else HTML
	refuse  []string          // user agent substrings answered 403
	types   map[string]string // content types overriding the default
	robots5 bool              // robots.txt answers 500
}

func (s site) serve(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.UserAgent(), "CheapSeats-Audit/") && !strings.Contains(r.UserAgent(), "compatible;") {
			t.Errorf("unexpected user agent %q", r.UserAgent())
		}
		for _, ua := range s.refuse {
			if strings.Contains(r.UserAgent(), ua) {
				http.Error(w, "forbidden", 403)
				return
			}
		}
		if r.URL.Path == "/robots.txt" && s.robots5 {
			http.Error(w, "oops", 500)
			return
		}
		body, ok := s.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		ct := "text/html; charset=utf-8"
		switch {
		case strings.HasSuffix(r.URL.Path, ".txt"):
			ct = "text/plain; charset=utf-8"
		case strings.HasSuffix(r.URL.Path, ".xml"):
			ct = "application/xml"
		}
		if c, ok := s.types[r.URL.Path]; ok {
			ct = c
		}
		w.Header().Set("content-type", ct)
		fmt.Fprint(w, strings.ReplaceAll(body, "BASE", srv.URL))
	}))
	return srv
}

const goodPage = `<!doctype html><html lang="en"><head><title>Cheap Seats: track AI answers</title>
<meta name="description" content="Cheap Seats asks AI assistants your buyers' questions and grades whether they recommend you.">
<link rel="canonical" href="BASE/"><meta property="og:title" content="Cheap Seats"><meta property="og:description" content="Track AI answers">
<script type="application/ld+json">{"@type":"SoftwareApplication"}</script></head>
<body><h1>Cheap Seats</h1><p>` + "Cheap Seats asks the questions your buyers ask, of every assistant, and grades the answers. " + "It runs on your laptop on the subscription you already have, and tells you which pages to write next to be recommended more often." + `</p></body></html>`

func TestAudit(t *testing.T) {
	defer AllowLoopbackForTests()()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	good := map[string]string{
		"/":            goodPage,
		"/robots.txt":  "User-agent: *\nAllow: /\nSitemap: BASE/sitemap.xml\n",
		"/llms.txt":    "# Cheap Seats\n> AI visibility\n\n- [Docs](BASE/docs): how it works\n",
		"/sitemap.xml": `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>BASE/</loc><lastmod>2026-10-01</lastmod></url><url><loc>BASE/docs</loc><lastmod>2026-09-01T10:00:00Z</lastmod></url></urlset>`,
		"/docs":        strings.Replace(goodPage, "BASE/", "BASE/docs", 1),
	}
	with := func(changes map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range good {
			out[k] = v
		}
		for k, v := range changes {
			if v == "" {
				delete(out, k)
			} else {
				out[k] = v
			}
		}
		return out
	}
	for _, c := range []struct {
		name  string
		site  site
		want  map[string]Severity // check id -> severity it must have
		clean bool                // no errors or warnings
	}{
		{name: "clean", site: site{files: good}, clean: true},
		{name: "robots blocks search, training and an engine", site: site{files: with(map[string]string{
			"/robots.txt": "User-agent: OAI-SearchBot\nDisallow: /\n\nUser-agent: GPTBot\nDisallow: /\n\nUser-agent: Bingbot\nDisallow: /\n",
		})}, want: map[string]Severity{"robots.blocks-ai-search": Error, "robots.blocks-training": Info, "robots.blocks-search-engine": Error, "sitemap.missing": "", "llms.missing": ""}},
		{name: "robots.txt server error", site: site{files: good, robots5: true}, want: map[string]Severity{"robots.server-error": Error}},
		{name: "firewall refuses Claude-User", site: site{files: good, refuse: []string{"Claude-User"}}, want: map[string]Severity{"waf.blocks-ai-bot": Error}},
		{name: "llms.txt missing, sitemap stale", site: site{files: with(map[string]string{
			"/llms.txt":    "",
			"/sitemap.xml": `<urlset><url><loc>BASE/docs</loc><lastmod>2025-01-01</lastmod></url></urlset>`,
		})}, want: map[string]Severity{"llms.missing": Warning, "sitemap.stale": Warning}},
		{name: "llms.txt served as HTML, no heading or links", site: site{files: with(map[string]string{"/llms.txt": "Cheap Seats tracks AI answers."}), types: map[string]string{"/llms.txt": "application/octet-stream"}},
			want: map[string]Severity{"llms.content-type": Warning, "llms.heading": Warning, "llms.no-links": Warning}},
		{name: "catch-all route for llms.txt", site: site{files: with(map[string]string{"/llms.txt": goodPage}), types: map[string]string{"/llms.txt": "text/html"}},
			want: map[string]Severity{"llms.missing": Warning}},
		{name: "single-page app shell", site: site{files: with(map[string]string{
			"/": `<!doctype html><html><head><title>App</title></head><body><div id="root"></div><script src="/app.js"></script></body></html>`,
		})}, want: map[string]Severity{"page.needs-js": Error, "page.lang": Warning, "page.description-missing": Warning, "page.h1": Warning, "page.canonical": Warning, "page.jsonld-missing": Warning, "page.open-graph": Warning}},
		{name: "noindex, long title, bad JSON-LD, short description", site: site{files: with(map[string]string{
			"/docs": strings.NewReplacer(`<title>Cheap Seats: track AI answers</title>`, `<title>`+strings.Repeat("Cheap Seats ", 10)+`</title><meta name="robots" content="noindex">`,
				`{"@type":"SoftwareApplication"}`, `{"@type":`, `content="Cheap Seats asks AI assistants your buyers' questions and grades whether they recommend you."`, `content="Short."`).Replace(good["/docs"]),
		})}, want: map[string]Severity{"page.noindex": Error, "page.title-long": Warning, "page.jsonld-invalid": Error, "page.description-length": Warning}},
		{name: "sitemap page gone", site: site{files: with(map[string]string{"/docs": ""})}, want: map[string]Severity{"page.status": Error}},
		{name: "sitemap doesn't parse", site: site{files: with(map[string]string{"/sitemap.xml": "<urlset><url>"})}, want: map[string]Severity{"sitemap.invalid": Error}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := c.site.serve(t)
			defer srv.Close()
			res := Run(context.Background(), []string{srv.URL}, Options{Now: func() time.Time { return now }})
			got := map[string]Severity{}
			for _, f := range res.Findings {
				got[f.Check] = f.Severity
				if f.Fix == "" || f.Message == "" || f.Site != srv.URL {
					t.Errorf("incomplete finding: %+v", f)
				}
			}
			if c.clean && res.Errors()+res.Warnings() > 0 {
				t.Errorf("want no errors or warnings, got %v", res.Findings)
			}
			for check, sev := range c.want {
				if sev == "" { // must not be reported
					if _, ok := got[check]; ok {
						t.Errorf("%s reported: %v", check, res.Findings)
					}
				} else if got[check] != sev {
					t.Errorf("%s: %q, want %q; findings %v", check, got[check], sev, res.Findings)
				}
			}
		})
	}
}

func TestAuditSite(t *testing.T) {
	defer AllowLoopbackForTests()()
	srv := site{files: map[string]string{
		"/robots.txt": "User-agent: Claude-SearchBot\nDisallow: /team\n",
		"/team":       "<html><body>hi</body></html>",
	}}.serve(t)
	defer srv.Close()
	res := Run(context.Background(), []string{srv.URL + "/team", "not a url:"}, Options{Hosted: map[string]bool{srv.URL + "/team": true}})
	s := res.Sites[0]
	if s.Home != srv.URL+"/team" || len(s.Pages) != 1 || s.Pages[0].Status != 200 {
		t.Fatalf("%+v", s)
	}
	checks := map[string]bool{}
	for _, f := range res.Findings {
		checks[f.Check] = true
	}
	// On a platform the brand doesn't control, the host's robots.txt isn't the brand's to fix:
	// the block is recorded against the bot, not raised as a finding. Nor are the host's
	// llms.txt, sitemap or markup checked.
	var blocked []string
	for _, b := range s.Bots {
		if !b.Allowed {
			blocked = append(blocked, b.Bot)
		}
	}
	if len(blocked) != 1 || blocked[0] != "Claude-SearchBot" {
		t.Errorf("blocked %v, want Claude-SearchBot", blocked)
	}
	if checks["robots.blocks-ai-search"] || checks["llms.missing"] || checks["sitemap.missing"] || !checks["site.invalid"] || checks["page.jsonld-missing"] {
		t.Errorf("%v", res.Findings)
	}
	// A path on a host the brand does control: robots.txt applies to the path and is reported;
	// llms.txt and the sitemap are the host's and aren't checked.
	res = Run(context.Background(), []string{srv.URL + "/team"}, Options{})
	checks = map[string]bool{}
	for _, f := range res.Findings {
		checks[f.Check] = true
	}
	if !checks["robots.blocks-ai-search"] || checks["llms.missing"] || checks["sitemap.missing"] {
		t.Errorf("%v", res.Findings)
	}
}

// A robots.txt written for Popcorn-Audit, before the rename, still applies.
func TestRobotsHonorsTheOldToken(t *testing.T) {
	old := ParseRobots("User-agent: Popcorn-Audit\nDisallow: /private\n")
	if allowsUs(old, "/private/x") || !allowsUs(old, "/public") {
		t.Error("ignored a rule for Popcorn-Audit")
	}
	both := ParseRobots("User-agent: Popcorn-Audit\nDisallow: /\n\nUser-agent: CheapSeats-Audit\nAllow: /\n")
	if !allowsUs(both, "/x") {
		t.Error("a rule for CheapSeats-Audit should win over the old token's")
	}
}
