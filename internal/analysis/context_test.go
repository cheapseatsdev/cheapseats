package analysis

import (
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

// The context digested the whole brand, so adding an alias, a site or an unrelated fact demoted
// every verified action until a rebuild. Only what the verifier judged against demotes it now.
func TestOnlyBriefAndQuotedFactEditsNeedVerifyingAgain(t *testing.T) {
	base, set, f, _, _ := verificationFixture()
	base.Brand.Facts = append(base.Brand.Facts, "The free plan covers one prompt set.")
	f.Verification = analysisRecordedVerification(base, f)
	for i, c := range f.Verification.Checks {
		if c.Gate == "capability" {
			f.Verification.Checks[i].Source, f.Verification.Checks[i].Quote = "brand.facts", base.Brand.Facts[0]
		}
	}
	verified := func(edit func(*config.Config)) bool {
		cfg := *base
		cfg.Brand.Facts = append([]string(nil), base.Brand.Facts...)
		cfg.Brand.Advisory.Goals = append([]string(nil), base.Brand.Advisory.Goals...)
		edit(&cfg)
		got := Triage(&cfg, set, f)
		return got.Verification.Status == "verified" && got.ClaudePrompt != ""
	}
	for name, edit := range map[string]func(*config.Config){
		"nothing":  func(*config.Config) {},
		"an alias": func(c *config.Config) { c.Brand.Aliases = append(c.Brand.Aliases, "acme.example") },
		"a site": func(c *config.Config) {
			c.Brand.Sites = append(c.Brand.Sites, config.Site{URL: "blog.acme.example", Kind: "blog"})
		},
		"a domain":               func(c *config.Config) { c.Brand.Domains = append(c.Brand.Domains, "acme.example") },
		"the name":               func(c *config.Config) { c.Brand.Name = "Acme Tracker" },
		"a new fact":             func(c *config.Config) { c.Brand.Facts = append(c.Brand.Facts, "Setup takes ten minutes.") },
		"a fact it didn't quote": func(c *config.Config) { c.Brand.Facts[1] = "The free plan covers two prompt sets." },
	} {
		if !verified(edit) {
			t.Errorf("editing %s demoted a verified action", name)
		}
	}
	for name, edit := range map[string]func(*config.Config){
		"the fact it quoted": func(c *config.Config) { c.Brand.Facts[0] = "The documentation host is docs.example.org." },
		"about":              func(c *config.Config) { c.Brand.About = "An enterprise tracker." },
		"the audience":       func(c *config.Config) { c.Brand.Audience = "an enterprise buyer" },
		"the category":       func(c *config.Config) { c.Brand.Category = "Enterprise compliance suites" },
		"a goal": func(c *config.Config) {
			c.Brand.Advisory.Goals = append(c.Brand.Advisory.Goals, "Win enterprise buyers.")
		},
		"the positioning": func(c *config.Config) { c.Brand.Advisory.Positioning = "For enterprises." },
	} {
		if verified(edit) {
			t.Errorf("editing %s kept the action verified", name)
		}
	}

	// Actions stored under the whole-brand digest stay verified until the brand changes.
	f.Verification.Context = legacyVerificationContext(base)
	if !verified(func(*config.Config) {}) {
		t.Error("upgrading demoted an action verified under the old digest")
	}
	if verified(func(c *config.Config) { c.Brand.Aliases = append(c.Brand.Aliases, "acme.example") }) {
		t.Error("an old digest outlived the brand it was made for")
	}
}
