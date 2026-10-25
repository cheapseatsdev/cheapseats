package scaffold

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/suggest"
)

// serve answers each path with its body, SRV in it replaced with the server's URL; anything else
// is a 404.
func serve(t *testing.T, pages map[string]string) *httptest.Server {
	t.Helper()
	t.Cleanup(audit.AllowLoopbackForTests())
	var base string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, ".xml"):
			w.Header().Set("content-type", "application/xml")
		case strings.HasSuffix(r.URL.Path, ".txt"):
			w.Header().Set("content-type", "text/plain")
		default:
			w.Header().Set("content-type", "text/html")
		}
		w.Write([]byte(strings.ReplaceAll(body, "SRV", base)))
	}))
	base = "http://" + srv.Listener.Addr().String()
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func discover(t *testing.T, pages map[string]string) Site {
	t.Helper()
	srv := serve(t, pages)
	s, err := Discover(context.Background(), audit.NewFetcher(nil, 2), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNamePrecedence(t *testing.T) {
	const ld = `<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebSite","name":"Acme Site"},{"@type":"Organization","name":"Acme LD","description":"From JSON-LD."}]}</script>`
	for _, c := range []struct {
		head, name, from string
	}{
		{`<meta property="og:site_name" content="Acme OG"><meta name="application-name" content="Acme App">` + ld + `<title>Acme Title | Home</title>`, "Acme OG", "og:site_name"},
		{`<meta name="application-name" content="Acme App">` + ld + `<title>Acme Title</title>`, "Acme App", "application-name"},
		{ld + `<title>Acme Title</title>`, "Acme LD", "json-ld"},
		{`<meta property="og:site_name" content="Home"><title>Project management for busy teams | Acme</title>`, "Acme", "title"},
		{`<title>Welcome</title>`, "LLMs Acme", "llms.txt"},
	} {
		s := discover(t, map[string]string{
			"/":         "<html><head>" + c.head + "</head><body><p>hi</p></body></html>",
			"/llms.txt": "# LLMs Acme\n\n> What it is, for llms.txt.\n",
		})
		if s.Name != c.name || s.NameFrom != c.from {
			t.Errorf("%s: got %q from %s, want %q from %s", c.head, s.Name, s.NameFrom, c.name, c.from)
		}
	}
	// Nothing on the page or in llms.txt: the domain.
	s := discover(t, map[string]string{"/": "<html><head><title>Home</title></head></html>"})
	if s.NameFrom != "domain" || s.About != "" {
		t.Errorf("got %q from %s, about %q", s.Name, s.NameFrom, s.About)
	}
}

func TestTitleName(t *testing.T) {
	for _, c := range []struct{ title, label, want string }{
		{"Home | Acme", "acme", "Acme"},
		{"Acme — The project tool for teams", "acme", "Acme"},
		{"Kindle to PDF - Export your Kindle books", "kindletopdf", "Kindle to PDF"},
		{"The best project tool for teams · Acme Inc", "getacme", "Acme Inc"},
		{"Welcome", "acme", ""},
	} {
		if got := TitleName(c.title, c.label); got != c.want {
			t.Errorf("TitleName(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

func TestDescriptionPrecedence(t *testing.T) {
	s := discover(t, map[string]string{"/": `<html><head><meta property="og:description" content="OG words."><meta name="description" content="Meta words."></head></html>`})
	if s.About != "Meta words." {
		t.Errorf("about %q", s.About)
	}
	s = discover(t, map[string]string{"/": `<html><head><script type="application/ld+json">{"@type":"SoftwareApplication","name":"Acme","description":"LD words."}</script></head></html>`})
	if s.About != "LD words." || s.Name != "Acme" {
		t.Errorf("about %q name %q", s.About, s.Name)
	}
}

func TestDetectSites(t *testing.T) {
	var links []*url.URL
	for _, l := range []string{
		"https://acme.com/pricing", "https://www.acme.com/docs/start", "https://docs.acme.com/api",
		"https://acme.com/blog/launch", "https://community.acme.com/t/1", "https://app.acme.com/login",
		"https://github.com/acme/acme", "https://github.com/acme/acme/issues", "https://github.com/acme",
		"https://github.com/vercel/next.js", "https://github.com/features", "https://acme.readthedocs.io/en/latest/",
		"mailto:hi@acme.com", "https://twitter.com/acme",
	} {
		u, _ := url.Parse(l)
		links = append(links, u)
	}
	got := DetectSites("acme.com", "Acme", links)
	want := []config.Site{
		{URL: "acme.com", Kind: "marketing"},
		{URL: "acme.com/docs", Kind: "docs"},
		{URL: "docs.acme.com", Kind: "docs"},
		{URL: "acme.com/blog", Kind: "blog"},
		{URL: "community.acme.com", Kind: "community"},
		{URL: "acme.readthedocs.io", Kind: "docs"},
		{URL: "github.com/acme/acme", Kind: "code"}, // the owner alone is covered by its repo
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("site %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

// Sites come from the sitemap and llms.txt as well as the home page's links.
func TestDiscoverReadsSitemapAndLLMs(t *testing.T) {
	srv := serve(t, map[string]string{
		"/":            `<html><head><meta property="og:site_name" content="Acme"><meta name="description" content="Acme is a project management app for remote teams."></head><body><a href="/pricing">Pricing</a><a href="https://github.com/acme-hq/acme">GitHub</a></body></html>`,
		"/llms.txt":    "# Acme\n\n- [Docs](/docs/index.md)\n",
		"/sitemap.xml": `<?xml version="1.0"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><sitemap><loc>SRV/blog.xml</loc></sitemap></sitemapindex>`,
		"/blog.xml":    `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>SRV/blog/hello</loc></url></urlset>`,
	})
	s, err := Discover(context.Background(), audit.NewFetcher(nil, 2), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, x := range s.Sites {
		kinds[x.URL] = x.Kind
	}
	if kinds[s.Host] != "marketing" || kinds[s.Host+"/docs"] != "docs" || kinds[s.Host+"/blog"] != "blog" || kinds["github.com/acme-hq/acme"] != "code" || len(kinds) != 4 {
		t.Errorf("sites %v", s.Sites)
	}
	if !s.LLMs || s.Sitemap != 1 {
		t.Errorf("llms %v sitemap %d", s.LLMs, s.Sitemap)
	}
	if got := strings.Join(s.Aliases, ","); got != s.Host {
		t.Errorf("aliases %q", got)
	}
}

func TestCategory(t *testing.T) {
	for _, c := range []struct{ about, want string }{
		{"Acme is a project management app for remote teams.", "project management app"},
		{"Cheap Seats is an open-source, self-hosted tool that asks AI assistants questions.", "self-hosted tool"},
		{"Build production-grade applications with a Postgres database, and more.", "Postgres database"},
		{"Application performance monitoring for developers.", "application performance monitoring"},
		{"Plausible is a lightweight and open-source Google Analytics alternative.", "open-source Google Analytics alternative"},
		{"Purpose-built for planning products with AI agents.", ""},
		{"We help teams ship faster.", ""},
		{"Acme is the Acme platform.", ""}, // names the brand
	} {
		got, ok := Category(c.about, "Acme")
		if c.want == "" {
			if ok || got != "tool" {
				t.Errorf("Category(%q) = %q %v, want the stand-in", c.about, got, ok)
			}
		} else if got != c.want || !ok {
			t.Errorf("Category(%q) = %q, want %q", c.about, got, c.want)
		}
	}
}

func site() Site {
	return Site{URL: "https://acme.com/", Host: "acme.com", Name: "Acme Cloud", NameFrom: "og:site_name",
		About:   "Acme Cloud is a project management app for remote teams.",
		Sites:   []config.Site{{URL: "acme.com", Kind: "marketing"}, {URL: "acme.com/docs", Kind: "docs"}, {URL: "github.com/acme/acme", Kind: "code"}},
		Aliases: Aliases("Acme Cloud", "acme.com")}
}

// The config init writes passes cheapseats check as it is, with and without competitors, and only
// head-to-heads names the brand.
func TestGeneratedConfigChecks(t *testing.T) {
	for _, comps := range [][]string{nil, {"Asana", "Trello", "Monday.com"}} {
		p := Build(site(), comps)
		cfg, err := Check(p.Files())
		if err != nil {
			t.Fatalf("%v: %v", comps, err)
		}
		if cfg.Brand.Name != "Acme Cloud" || len(cfg.Brand.Sites) != 3 || len(cfg.Competitors) != len(comps) || cfg.Schedule.DailyHourUTC != nil {
			t.Errorf("config %+v", cfg.Brand)
		}
		if got := strings.Join(cfg.Brand.Aliases, ","); got != "acme.com,AcmeCloud" {
			t.Errorf("aliases %q", got)
		}
		if len(cfg.Answerers) != 1 || cfg.Answerers[0].Provider != config.ClaudeCode || cfg.Grader.Via() != config.ClaudeCode || cfg.Analyst.Via() != config.ClaudeCode {
			t.Errorf("not subscription-first: %+v %+v %+v", cfg.Answerers, cfg.Grader, cfg.Analyst)
		}
		ids := map[string]bool{}
		infoIDs := map[string]bool{}
		for _, s := range p.Sets {
			ids[s.ID] = true
			for _, x := range s.Prompts {
				infoIDs[x.ID] = x.Info()
				if names := sets.Names(x.Text, "Acme Cloud"); names != (s.ID == sets.HeadToHeads) {
					t.Errorf("%s: %q", s.ID, x.Text)
				}
				if s.ID != sets.HeadToHeads && strings.Contains(x.Text, " vs ") {
					t.Errorf("closed competitor comparison is not a brand visibility question: %q", x.Text)
				}
			}
		}
		// Correct explanations must not be counted as failed product selections.
		for _, id := range []string{"how-to-choose", "what-to-look-for", "typical-cost", "acme-cloud-downsides"} {
			if !infoIDs[id] {
				t.Errorf("explanation prompt %s must be informational", id)
			}
		}
		for _, id := range []string{"discovery", "use-cases", "alternatives", "pricing", sets.HeadToHeads} {
			if !ids[id] {
				t.Errorf("no %s set", id)
			}
		}
		if p.Category != "project management app" {
			t.Errorf("category %q", p.Category)
		}
	}
	// A name that needs escaping still makes valid TOML.
	s := site()
	s.Name, s.About = `Acme "Quoted" \ Co`, ""
	s.Aliases = Aliases(s.Name, s.Host)
	if _, err := Check(Build(s, []string{`Rival "One"`}).Files()); err != nil {
		t.Fatal(err)
	}
}

func TestSuggestAddsToStarterSets(t *testing.T) {
	defer func(f func(context.Context, *config.Config, sets.Set, []sets.Set, int) (suggest.Result, error)) {
		suggestPrompts = f
	}(suggestPrompts)
	var asked sets.Set
	suggestPrompts = func(_ context.Context, _ *config.Config, set sets.Set, _ []sets.Set, n int) (suggest.Result, error) {
		asked = set
		return suggest.Result{CostUSD: 0.01, Prompts: []suggest.Prompt{
			{Stage: "awareness", Text: "Which project management app keeps remote teams in sync?", Intent: "choice"},
			{Stage: "pricing", Text: "Do project management apps charge per seat?", Intent: "info"},
			{Stage: "implementation", Text: "How long does moving to a new project management app take?", Intent: "info"},
		}}, nil
	}
	p := Build(site(), nil)
	cfg, err := Check(p.Files())
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]int{}
	for _, s := range p.Sets {
		before[s.ID] = len(s.Prompts)
	}
	if err := p.Suggest(context.Background(), cfg, 3); err != nil {
		t.Fatal(err)
	}
	if asked.ID == sets.HeadToHeads || len(asked.Prompts) == 0 {
		t.Errorf("asked about %+v", asked)
	}
	for _, s := range p.Sets {
		want := before[s.ID]
		if s.ID == "discovery" || s.ID == "pricing" || s.ID == "use-cases" {
			want++
		}
		if len(s.Prompts) != want {
			t.Errorf("%s: %d prompts, want %d", s.ID, len(s.Prompts), want)
		}
	}
	if p.Suggested != 3 || p.CostUSD != 0.01 {
		t.Errorf("suggested %d, $%v", p.Suggested, p.CostUSD)
	}
	if _, err := Check(p.Files()); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	files := Build(site(), nil).Files()
	if err := os.MkdirAll(filepath.Join(dir, "prompt-sets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt-sets", "pricing.toml"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Write(dir, files)
	if err == nil || !strings.Contains(err.Error(), "pricing.toml") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cheapseats.toml")); err == nil {
		t.Error("wrote cheapseats.toml beside a file it refused to overwrite")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "prompt-sets", "pricing.toml")); string(b) != "mine" {
		t.Error("overwrote pricing.toml")
	}
	os.Remove(filepath.Join(dir, "prompt-sets", "pricing.toml"))
	if err := Write(dir, files); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, files); err == nil {
		t.Error("a second write succeeded")
	}
}

// A name from the site can't add TOML of its own: not through the header comment, not through a
// value.
func TestSiteTextCantInjectTOML(t *testing.T) {
	const payload = "Acme\n[[answerers]]\nid = \"x\"\n#"
	ld, _ := json.Marshal(map[string]any{"@type": "Organization", "name": payload, "description": "Acme is a CRM.\n[schedule]\ndaily_hour_utc = 1"})
	s := discover(t, map[string]string{"/": `<html><head><script type="application/ld+json">` + string(ld) + `</script></head></html>`})
	if s.Name != `Acme [[answerers]] id = "x" #` || strings.ContainsAny(s.Name+s.About, "\n\r") {
		t.Fatalf("name %q about %q", s.Name, s.About)
	}
	// And when a caller hands Build the raw text.
	raw := s
	raw.Name, raw.URL = payload, "https://acme.com/\n[[answerers]]"
	for _, x := range []Site{s, raw} {
		dir := t.TempDir()
		if err := Write(dir, Build(x, []string{"Rival"}).Files()); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if _, err := toml.DecodeFile(filepath.Join(dir, "cheapseats.toml"), &got); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, " ") != "analyst answerers brand budget competitors grader owners schedule tiers" {
			t.Errorf("tables %v", keys)
		}
		if a := got["answerers"].([]map[string]any); len(a) != 1 || a[0]["id"] != "claude-code-opus" {
			t.Errorf("answerers %v", a)
		}
		if sch := got["schedule"].(map[string]any); len(sch) != 0 {
			t.Errorf("schedule %v", sch)
		}
		if n := got["brand"].(map[string]any)["name"].(string); n != x.Name {
			t.Errorf("name %q, want %q", n, x.Name)
		}
	}
}
