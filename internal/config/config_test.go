package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "popcorn.toml")
	// daily_hour_utc in [budget] instead of [schedule]: silently no schedule, unless rejected.
	os.WriteFile(p, []byte("[brand]\nname = \"X\"\n[budget]\ndaily_usd = 1.0\ndaily_hour_utc = 13\n"), 0o644)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "budget.daily_hour_utc") {
		t.Fatalf("want unknown key error, got %v", err)
	}
}

func TestSlug(t *testing.T) {
	for name, want := range map[string]string{"Popcorn": "popcorn", "Acme Sandbox, Inc.": "acme-sandbox-inc", "  ": "brand", "Café 2": "caf-2"} {
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
	p := filepath.Join(t.TempDir(), "popcorn.toml")
	os.WriteFile(p, []byte("[brand]\nname = \"X\"\n[[brand.sites]]\nurl = \"x.com\"\nkind = \"wiki\"\n"), 0o644)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "kind must be one of") {
		t.Fatalf("want a kind error, got %v", err)
	}
}

func TestSharedCompetitors(t *testing.T) {
	load := func(body string) (*Config, error) {
		p := filepath.Join(t.TempDir(), "popcorn.toml")
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
