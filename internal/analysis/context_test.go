package analysis

import (
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

// The context digested the whole brand, so adding an alias, a site or an unrelated fact demoted
// every verified action until a rebuild. Only what the verifier judged against demotes it now:
// the brief, the whole facts its checks quoted, and whether the pages it rests on are the brand's.
func TestOnlyBriefQuotedFactAndSiteEditsNeedVerifyingAgain(t *testing.T) {
	base, set, f, pages, d := verificationFixture()
	base.Brand.Facts = []string{
		"Every plan includes the command-line tool.",
		"The Team plan includes the command-line tool and single sign-on.",
		"The free plan covers one prompt set.",
	}
	base.Brand.Sites = []config.Site{{URL: "acme.example", Kind: "marketing"}, {URL: "status.acme.dev", Kind: "other"}}
	review := "https://reviews.example/acme"
	pages = append(pages, artifact{URL: review, Text: "Reviewers list Acme as a starter tracker for small teams."})
	d.Checks["capability"] = proof{Passed: true, Source: "brand.facts", Quote: "plan includes the command-line tool", Reason: "The fix only points to existing documentation for a capability every plan has."}
	d.Checks["eligibility"] = proof{Passed: true, Source: review, Quote: "Reviewers list Acme as a starter tracker", Reason: "The independent listing already places Acme in the starter category this page serves."}
	v, why := verifyDecision(d, f, base, pages)
	if why != "" {
		t.Fatalf("fixture did not verify: %s", why)
	}
	f.Verification = v

	verified := func(edit func(*config.Config)) bool {
		cfg := *base
		cfg.Brand.Facts = append([]string(nil), base.Brand.Facts...)
		cfg.Brand.Sites = append([]config.Site(nil), base.Brand.Sites...)
		cfg.Brand.Advisory.Goals = append([]string(nil), base.Brand.Advisory.Goals...)
		edit(&cfg)
		got := Triage(&cfg, set, f)
		return got.Verification.Status == "verified" && got.ClaudePrompt != ""
	}
	for name, edit := range map[string]func(*config.Config){
		"nothing":  func(*config.Config) {},
		"an alias": func(c *config.Config) { c.Brand.Aliases = append(c.Brand.Aliases, "Acme Inc") },
		"an unrelated site": func(c *config.Config) {
			c.Brand.Sites = append(c.Brand.Sites, config.Site{URL: "blog.acme.dev", Kind: "blog"})
		},
		"a narrower site over the target": func(c *config.Config) {
			c.Brand.Sites = append(c.Brand.Sites, config.Site{URL: "acme.example/docs", Kind: "docs", Owner: "docs-team"})
		},
		"removing an unrelated site": func(c *config.Config) { c.Brand.Sites = c.Brand.Sites[:1] },
		"a domain":                   func(c *config.Config) { c.Brand.Domains = append(c.Brand.Domains, "acme.dev") },
		"the name":                   func(c *config.Config) { c.Brand.Name = "Acme Tracker" },
		"the owners":                 func(c *config.Config) { c.Owners.Default = "growth" },
		"a new fact":                 func(c *config.Config) { c.Brand.Facts = append(c.Brand.Facts, "Setup takes ten minutes.") },
		"a fact it didn't quote":     func(c *config.Config) { c.Brand.Facts[2] = "The free plan covers two prompt sets." },
		"the order of the facts":     func(c *config.Config) { c.Brand.Facts[0], c.Brand.Facts[2] = c.Brand.Facts[2], c.Brand.Facts[0] },
	} {
		if !verified(edit) {
			t.Errorf("editing %s demoted a verified action", name)
		}
	}
	for name, edit := range map[string]func(*config.Config){
		"the fact it quoted": func(c *config.Config) { c.Brand.Facts[0] = "Every plan includes the web dashboard." },
		"a prefix on the quoted fact": func(c *config.Config) {
			c.Brand.Facts[0] = "No longer true: every plan includes the command-line tool."
		},
		"a suffix on the quoted fact": func(c *config.Config) {
			c.Brand.Facts[0] = "Every plan includes the command-line tool, except the Free plan."
		},
		// The quoted words still occur, inside the Team plan fact, but the fact they came from is gone.
		"removing the quoted fact":   func(c *config.Config) { c.Brand.Facts = c.Brand.Facts[1:] },
		"removing the target's site": func(c *config.Config) { c.Brand.Sites = c.Brand.Sites[1:] },
		"a site over a page it quoted as a third party's": func(c *config.Config) {
			c.Brand.Sites = append(c.Brand.Sites, config.Site{URL: "reviews.example", Kind: "community"})
		},
		"about":        func(c *config.Config) { c.Brand.About = "An enterprise tracker." },
		"the audience": func(c *config.Config) { c.Brand.Audience = "an enterprise buyer" },
		"the category": func(c *config.Config) { c.Brand.Category = "Enterprise compliance suites" },
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
	if verified(func(c *config.Config) { c.Brand.Aliases = append(c.Brand.Aliases, "Acme Inc") }) {
		t.Error("an old digest outlived the brand it was made for")
	}
}

func TestQuotedFactsCoverEveryFactAQuoteTouches(t *testing.T) {
	facts := []string{"Alpha plan has SSO.", "Beta plan has SSO.", "Gamma has exports."}
	if got := quotedFacts(facts, "plan has SSO"); len(got) != 2 || got[0] != facts[0] || got[1] != facts[1] {
		t.Errorf("repeated wording: %q", got)
	}
	if got := quotedFacts(facts, "has SSO.\nGamma has"); len(got) != 2 || got[0] != facts[1] || got[1] != facts[2] {
		t.Errorf("quote across two facts: %q", got)
	}
	if got := quotedFacts(facts, "Delta has nothing"); len(got) != 0 {
		t.Errorf("missing quote matched: %q", got)
	}
}
