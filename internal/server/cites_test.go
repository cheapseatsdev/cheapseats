package server

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// Evidence as the analyst writes it, with escapes copied from its %q input and Markdown copied
// from answers, renders as one linked quote per line on the hub and the run page. Result IDs are
// dummies.
func TestCitesRenderEvidenceAsLinkedQuotes(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	id := execute()
	an := analysis.Analysis{
		Insights: "fixture",
		Investigations: []analysis.Investigation{{Title: "Check the pricing page lists add-ons", Why: `Answers call it \"cheap\"`, Check: "Read the pricing page.", Decision: "Whether to list add-ons.", Prompts: []int{1},
			Evidence: `201: "Starter is $95/month... roundups list Foo (~$29/mo), Bar (~$24/mo) and Baz (~$20/mo)". 202: "Add-ons can push the real cost to about **$76/month**".`}},
		Findings: []analysis.Finding{
			{Title: "Assistants lack current facts about the product", Detail: "Memory answers decline.", Prompts: []int{1},
				Evidence: `101: "I don't have reliable information on \"Acme Widgets\"". 102: "I don’t have reliable current details on Acme Widgets’ pricing or features". 103: "I don’t know Acme Widgets’ current self-hosting requirements".`},
			// Ambiguous (an unescaped " inside a quote): shown as written, never credited to result 12.
			{Title: "Ambiguous evidence falls back to text", Detail: "d", Prompts: []int{1}, Evidence: `7: "Use the format "name". 12: "value" as shown"`},
		},
	}
	if _, err := db.Exec(`UPDATE runs SET fixes=?, fixes_at=? WHERE id=?`, analysis.Encode(an), store.Now(), id); err != nil {
		t.Fatal(err)
	}
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(path string) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		return w.Body.String()
	}
	run := get("/runs/" + id)
	findings := get("/next-steps?view=market-findings")
	investigations := get("/next-steps?view=investigations")
	link := `<a href="/runs/` + id + `#result-101">result 101</a>`
	for name, c := range map[string]struct {
		body string
		want []string
	}{
		"hub findings": {findings, []string{
			`<blockquote class="evidence">“I don’t have reliable information on “Acme Widgets””</blockquote><figcaption class="muted small">— ` + link + `</figcaption>`,
			`<blockquote class="evidence">“I don’t know Acme Widgets’ current self-hosting requirements”</blockquote>`,
			`<p><b>Evidence:</b> 7: &#34;Use the format &#34;name&#34;. 12: &#34;value&#34; as shown&#34;</p>`,
		}},
		"hub investigations": {investigations, []string{
			`<b>Why:</b> Answers call it &#34;cheap&#34;`,
			`<blockquote class="evidence">“Add-ons can push the real cost to about $76/month”</blockquote>`,
			`<a href="/runs/` + id + `#result-202">result 202</a>`,
		}},
		"run page": {run, []string{link, `“Starter is $95/month… roundups list Foo (~$29/mo), Bar (~$24/mo) and Baz (~$20/mo)”`}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.body, w) {
				t.Errorf("%s: want %s", name, w)
			}
		}
		for _, bad := range []string{`\&#34;`, `\"`, `**$76`, `101: &#34;`, `#result-12"`} {
			if strings.Contains(c.body, bad) {
				t.Errorf("%s: shows %s", name, bad)
			}
		}
	}
}

// Evidence is model output about third-party text: everything is escaped, inside a parsed quote,
// in the fallback, and in the run ID that goes into a link.
func TestCitesEscapeUntrustedEvidence(t *testing.T) {
	a, _, _, _ := fixFixture(t)
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	tpl := s.pages["run"]
	for _, ev := range []string{
		`7: "<script>alert(1)</script> and **<b>x</b>**"`,
		`<script>alert(1)</script> **<b>x</b>**`,
	} {
		var b bytes.Buffer
		if err := tpl.ExecuteTemplate(&b, "cites", newCites("Evidence", `r"><script>`, ev)); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		for _, bad := range []string{"<script>", "<b>x", `r"><`} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: unescaped %q in %s", ev, bad, out)
			}
		}
		if !strings.Contains(out, "&lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(out, "&lt;b&gt;x&lt;/b&gt;") {
			t.Errorf("%s: want the text escaped, got %s", ev, out)
		}
	}
}
