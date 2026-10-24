package report

import (
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

func TestPageType(t *testing.T) {
	for _, c := range []struct{ url, title, want string }{
		// Listicles, by path or title.
		{"https://zapier.com/blog/best-note-taking-apps/", "", Listicle},
		{"https://example.com/top-10-ci-tools", "", Listicle},
		{"https://example.com/p/123", "The 7 Best Sandboxes for AI Agents in 2026", Listicle},
		{"https://example.com/docker-alternatives", "", Listicle},
		{"https://example.com/guide", "Tools for running agents safely", Listicle},
		{"https://example.com/blog/security-best-practices", "Security best practices", Article},
		{"https://example.com/p/1", "5 best practices for CI", OtherPage},
		{"https://example.com/page", "Top 10x faster builds", OtherPage},
		{"https://example.com/p/1", "The top 10", Listicle},
		{"https://example.com/p/1", "12 top tools", Listicle},
		{"https://example.com/p/1", "Alternative tools for developers", Listicle},
		// Comparisons win over listicles and review sites.
		{"https://example.com/blog/notion-vs-obsidian", "", Comparison},
		{"https://example.com/x", "Docker versus Podman", Comparison},
		{"https://example.com/x", "Comparing Docker and Podman", Comparison},
		{"https://example.com/x", "A comparably good tool", OtherPage},
		{"https://example.com/x", "Vscode tips", OtherPage},
		{"https://example.com/compare/acme", "", Comparison},
		{"https://example.com/an-alternative-to-docker", "", Comparison},
		{"https://www.g2.com/compare/acme-vs-beta", "", Comparison},
		// Reviews.
		{"https://www.g2.com/products/acme/reviews", "", ReviewPage},
		{"https://www.capterra.com/p/123/acme/", "", ReviewPage},
		{"https://example.com/acme-review", "", ReviewPage},
		{"https://example.com/p/1", "Acme reviewed", OtherPage},
		{"https://example.com/best-code-review-tools", "", Listicle},
		// Docs come before what the path says.
		{"https://docs.docker.com/ai/sandboxes/security/", "", Docs},
		{"https://example.com/docs/compare", "", Docs},
		{"https://example.com/documentation/start", "", Docs},
		{"https://acme.readthedocs.io/en/latest/", "", Docs},
		{"https://pkg.go.dev/net/http", "", Docs},
		// Forums and discussions.
		{"https://www.reddit.com/r/devops/comments/abc/best_ci/", "", Forum},
		{"https://news.ycombinator.com/item?id=1", "", Forum},
		{"https://stackoverflow.com/questions/1/how", "", Forum},
		{"https://github.com/acme/widget/issues/12", "", Forum},
		{"https://github.com/acme/widget/discussions/3", "", Forum},
		{"https://community.example.com/t/help/9", "", Forum},
		// A repository's front page, but not its files.
		{"https://github.com/acme/widget", "", Repo},
		{"https://github.com/acme/widget/", "", Repo},
		{"https://github.com/acme/widget/blob/main/README.md", "", OtherPage},
		// Articles.
		{"https://example.com/blog/launch", "", Article},
		{"https://example.com/2026/05/launch", "", Article},
		{"https://example.com/en/2026/05/launch", "", Article},
		{"https://acme.substack.com/p/launch", "", Article},
		{"https://blog.example.com/launch", "", Article},
		// Product pages.
		{"https://acme.example", "", Product},
		{"https://acme.example/", "", Product},
		{"https://acme.example/pricing", "", Product},
		{"https://acme.example/features/sandbox", "", Product},
		// The rest.
		{"https://en.wikipedia.org/wiki/Sandbox", "", OtherPage},
		{"not a url at all", "", OtherPage},
	} {
		if got := PageType(c.url, c.title); got != c.want {
			t.Errorf("PageType(%q, %q) = %s, want %s", c.url, c.title, got, c.want)
		}
	}
}

// Each answer counts once per type however many of its pages are of it; a page's type comes from
// the first title any answer gave it; citation shares add up to 100%.
func TestPageTypeRollUp(t *testing.T) {
	g := grade.Grade{Verdict: grade.Mentioned}
	res := []run.Result{
		graded(0, "p", g, &answer.Answer{
			Searches:  []answer.Query{{Query: "q", Results: []answer.Link{{URL: "https://example.com/p/1", Title: "Acme vs Beta"}, {URL: "https://docs.acme.example/start"}}}},
			Citations: []answer.Link{{URL: "https://example.com/p/1"}, {URL: "https://example.com/top-5-sandboxes"}, {URL: "https://example.com/best-sandboxes"}},
		}),
		graded(0, "p", g, &answer.Answer{Citations: []answer.Link{{URL: "https://docs.acme.example/start"}}}),
	}
	s := Build(config.Brand{Name: "Acme"}, res)
	by := map[string]TypeUse{}
	share := 0.0
	for _, u := range s.PageTypes {
		by[u.Type] = u
		share += u.CitationShare
	}
	if len(s.PageTypes) != len(PageTypeOrder) || !near(share, 1) {
		t.Fatalf("page types: %+v", s.PageTypes)
	}
	if l := by[Listicle]; l.Cited != 1 || l.Pages != 2 || !near(l.CitationShare, 2.0/4) {
		t.Errorf("listicles: %+v", l)
	}
	if c := by[Comparison]; c.Cited != 1 || c.Surfaced != 1 || !near(c.CitationShare, 1.0/4) {
		t.Errorf("comparison, by the title its search result gave it: %+v", c)
	}
	if d := by[Docs]; d.Cited != 1 || d.Surfaced != 1 {
		t.Errorf("docs: %+v", d)
	}
	for _, p := range s.Pages {
		if p.URL == "https://example.com/p/1" && p.Type != Comparison {
			t.Errorf("page: %+v", p)
		}
	}
}
