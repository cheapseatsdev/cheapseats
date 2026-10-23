package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cheapseats.toml")
	// daily_hour_utc in [budget] instead of [schedule]: silently no schedule, unless rejected.
	os.WriteFile(p, []byte("[brand]\nname = \"X\"\n[budget]\ndaily_usd = 1.0\ndaily_hour_utc = 13\n"), 0o644)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "budget.daily_hour_utc") {
		t.Fatalf("want unknown key error, got %v", err)
	}
}

func TestSlug(t *testing.T) {
	for name, want := range map[string]string{"Cheap Seats": "cheap-seats", "Acme Sandbox, Inc.": "acme-sandbox-inc", "  ": "brand", "Café 2": "caf-2"} {
		if got := (Brand{Name: name}).Slug(); got != want {
			t.Errorf("Slug(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSites(t *testing.T) {
	b := Brand{Domains: []string{"acme.com", "github.com/acme"}, Sites: []Site{
		{URL: "acme.com", Kind: "marketing", Owner: "web"},
		{URL: "https://docs.acme.com/", Kind: "docs", Owner: "docs team"},
		{URL: "acme.dev/blog", Kind: "blog"},
	}}
	if n := len(b.AllSites()); n != 4 {
		t.Fatalf("acme.com is in both lists; want 4 sites, got %d: %+v", n, b.AllSites())
	}
	for url, want := range map[string]string{
		"https://www.acme.com/pricing":        "acme.com",
		"https://docs.acme.com/guides/x":      "https://docs.acme.com/", // more specific than acme.com
		"https://eu.docs.acme.com/x":          "https://docs.acme.com/",
		"https://blog.acme.com/x":             "acme.com",
		"https://acme.dev/blog/post":          "acme.dev/blog",
		"https://acme.dev/pricing":            "",
		"https://github.com/acme/cli":         "github.com/acme",
		"https://github.com/acmecorp":         "",
		"https://notacme.com/":                "",
		"https://example.com/?ref=acme.com/x": "",
	} {
		s, ok := b.SiteOf(url)
		if ok != (want != "") || s.URL != want {
			t.Errorf("SiteOf(%s) = %q, %v; want %q", url, s.URL, ok, want)
		}
	}
	p := filepath.Join(t.TempDir(), "cheapseats.toml")
	os.WriteFile(p, []byte("[brand]\nname = \"X\"\n[[brand.sites]]\nurl = \"x.com\"\nkind = \"wiki\"\n"), 0o644)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "kind must be one of") {
		t.Fatalf("want a kind error, got %v", err)
	}
}

func TestSharedCompetitors(t *testing.T) {
	load := func(body string) (*Config, error) {
		p := filepath.Join(t.TempDir(), "cheapseats.toml")
		os.WriteFile(p, []byte("[brand]\nname = \"Acme\"\naliases = [\"acme.dev\"]\n"+body), 0o644)
		return Load(p)
	}
	c, err := load("[[competitors]]\nname = \"Peec AI\"\naliases = [\"Peec\"]\n")
	if err != nil || len(c.Competitors) != 1 || c.Competitors[0].Aliases[0] != "Peec" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []string{
		"[[competitors]]\nname = \"\"\n",
		"[[competitors]]\nname = \"Peec\"\n[[competitors]]\nname = \"peec\"\n",
		"[[competitors]]\nname = \"acme.dev\"\n",
	} {
		if _, err := load(bad); err == nil {
			t.Errorf("should fail: %q", bad)
		}
	}
}

// The grader and analyst run through the API or the claude CLI. Through the API, a run records the
// model alone, as it always has, so switching nothing changes no trend.
func TestGraderProvider(t *testing.T) {
	api, cli := Model{Model: "claude-haiku-5-5"}, Model{Model: "claude-haiku-5-5", Provider: ClaudeCode}
	if api.Ref() != "claude-haiku-5-5" || api.Via() != Anthropic {
		t.Errorf("API grader: %q via %s", api.Ref(), api.Via())
	}
	if cli.Ref() != "claude-code:claude-haiku-5-5" || ModelFromRef(cli.Ref()) != cli || ModelFromRef(api.Ref()) != api {
		t.Errorf("round trip: %q", cli.Ref())
	}
	p := filepath.Join(t.TempDir(), "cheapseats.toml")
	os.WriteFile(p, []byte("[brand]\nname = \"X\"\n[grader]\nmodel = \"m\"\nprovider = \"codex\"\n"), 0o644)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "[grader] provider") {
		t.Errorf("want a provider error, got %v", err)
	}
}

// The subscription example is the API example with every model on the CLIs: nothing else may drift.
func TestSubscriptionExampleMatchesTheAPIExample(t *testing.T) {
	api, err := Load("../../examples/cheapseats-config/cheapseats.toml")
	if err != nil {
		t.Fatal(err)
	}
	subs, err := Load("../../examples/cheapseats-config/cheapseats.subscriptions.toml")
	if err != nil {
		t.Fatal(err)
	}
	if subs.Grader.Via() != ClaudeCode || subs.Analyst.Via() != ClaudeCode || len(subs.Answerers) != len(api.Answerers) {
		t.Fatalf("grader %s, analyst %s, %d answerers", subs.Grader.Via(), subs.Analyst.Via(), len(subs.Answerers))
	}
	cli := map[string]string{Anthropic: ClaudeCode, OpenAI: Codex}
	for i, a := range subs.Answerers {
		b := api.Answerers[i]
		if a.Provider != cli[b.Provider] || a.Model != b.Model || a.Name != b.Name || a.Samples != b.Samples {
			t.Errorf("answerer %d: %+v is not %+v on the CLI", i, a, b)
		}
	}
	// Everything else: the same.
	subs.Answerers, api.Answerers = nil, nil
	subs.Grader.Provider, subs.Analyst.Provider = api.Grader.Provider, api.Analyst.Provider
	if a, b := fmt.Sprintf("%+v", *api), fmt.Sprintf("%+v", *subs); a != b {
		t.Errorf("the examples drifted apart:\n%s\n%s", a, b)
	}
}

func TestSourcesAndCompetitorDomains(t *testing.T) {
	load := func(body string) error {
		p := filepath.Join(t.TempDir(), "cheapseats.toml")
		os.WriteFile(p, []byte("[brand]\nname = \"X\"\ndomains = [\"x.com\"]\n"+body), 0o644)
		_, err := Load(p)
		return err
	}
	if err := load("[[competitors]]\nname = \"Y\"\ndomains = [\"y.com\", \"github.com/y\"]\n[sources]\nugc = [\"forum.z.org\"]\nreview = [\"z.com/reviews\"]\n"); err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]string{
		"[sources]\nsocial = [\"z.com\"]\n":                                "class must be one of",
		"[sources]\nugc = [\"\"]\n":                                        "not a host",
		"[sources]\nugc = [\"z.com\"]\neditorial = [\"https://z.com/\"]\n": "in both",
		"[[competitors]]\nname = \"Y\"\ndomains = [\" \"]\n":               "domains[0]",
		"[[competitors]]\nname = \"Y\"\ndomains = [\"docs.x.com\"]\n":      "one of the brand's sites",
	} {
		if err := load(body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want %q, got %v", body, want, err)
		}
	}
}

func TestThemesAndSampling(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cheapseats.toml")
	load := func(body string) (*Config, error) {
		t.Helper()
		os.WriteFile(p, []byte("[brand]\nname = \"X\"\n"+body), 0o644)
		return Load(p)
	}
	c, err := load("")
	if err != nil || !c.ThemesOn() || c.Sampling.Adaptive {
		t.Fatalf("themes are on and adaptive sampling off by default: %+v %v", c, err)
	}
	if c, err = load("[grader]\nmodel = \"m\"\nthemes = false\n[sampling]\nadaptive = true\nmax_extra = 12\n"); err != nil || c.ThemesOn() || c.Sampling.MaxExtra != 12 {
		t.Fatalf("%+v %v", c, err)
	}
	for body, want := range map[string]string{
		"[analyst]\nthemes = true\n":       "belongs in [grader]",
		"[sampling]\nadaptive = true\n":    "needs max_extra",
		"[sampling]\nmax_extra = -1\n":     "0..1000",
		"[sampling]\nmax_extra = 100000\n": "0..1000",
	} {
		if _, err := load(body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want %q, got %v", body, want, err)
		}
	}
}

// A config repo from before the rename has popcorn.toml, and CHEAPSEATS_CONFIG (or the image's
// default) names cheapseats.toml.
func TestLoadFindsTheOldConfigName(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile("../../examples/cheapseats-config/cheapseats.toml")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "popcorn.toml"), b, 0o644)
	if _, err := Load(filepath.Join(dir, "cheapseats.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestOptionalProviders(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cheapseats.toml")
	os.WriteFile(p, []byte("[brand]\nname = \"Acme\"\n"), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.FoldNamesOn() || c.CoverageOn() || c.TypeSafe != nil || c.Exa != nil {
		t.Error("absent tables mean off")
	}
	if c.TypeSafeModel() != "jev-latest" || c.FoldConfidence() != 0.8 || c.CoverageResults() != 3 {
		t.Error("defaults")
	}
	os.WriteFile(p, []byte("[brand]\nname = \"Acme\"\n[typesafe]\nmodel = \"jev-1.14.0\"\nmin_confidence = 0.9\n[exa]\nresults = 5\n"), 0o644)
	c, err = Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.FoldNamesOn() || !c.CoverageOn() || c.TypeSafeModel() != "jev-1.14.0" || c.FoldConfidence() != 0.9 || c.CoverageResults() != 5 {
		t.Errorf("present tables mean on, with their settings: %+v %+v", c.TypeSafe, c.Exa)
	}
	off := false
	c.TypeSafe.FoldNames, c.Exa.Coverage = &off, &off
	if c.FoldNamesOn() || c.CoverageOn() {
		t.Error("false turns each off")
	}
	for _, bad := range []string{"[typesafe]\nmin_confidence = 1.5\n", "[exa]\nresults = 11\n", "[typesafe]\nfold = true\n"} {
		os.WriteFile(p, []byte("[brand]\nname = \"Acme\"\n"+bad), 0o644)
		if _, err := Load(p); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv(TypeSafeBaseURLVar, "")
	t.Setenv(ExaBaseURLVar, "")
	if Missing(TypeSafeProvider) == "" || Missing(ExaProvider) == "" {
		t.Error("no key and no proxy is missing")
	}
	t.Setenv("EXA_API_KEY", "k")
	if Missing(ExaProvider) != "" || Missing(TypeSafeProvider) == "" {
		t.Error("each provider has its own key")
	}
	if KeyVar(TypeSafeProvider) != "TYPESAFE_API_KEY" || KeyVar(ExaProvider) != "EXA_API_KEY" {
		t.Error("key variables")
	}
}
