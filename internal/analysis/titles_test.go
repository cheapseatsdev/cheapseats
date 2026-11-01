package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// An analyst titled a market finding "x" in production, and the card showed just that. A title
// that can't name its item drops the item, with a note in the insights that keeps its substance.
func TestDegenerateTitlesAreOmittedWithAReason(t *testing.T) {
	cfg, set, f, _, _ := verificationFixture()
	finding := Finding{Title: "Buyers compare on setup time", Detail: "Answers weigh how long setup takes.", Evidence: "Result 7", Prompts: []int{1}}
	inv := Investigation{Title: "Check setup route", Why: "Startup users cannot follow a retired documentation link.", Check: f.Check, Decision: f.Decision, Evidence: f.Evidence, Prompts: []int{1}, Target: f.Target}
	a := Analysis{Investigations: []Investigation{inv}, Findings: []Finding{finding}}
	for _, title := range []string{"x", "  ", "TBD", "Finding:", "Pricing", "AI UX"} {
		bad := finding
		bad.Title, bad.Detail = title, "Detail of "+title
		a.Findings = append(a.Findings, bad)
		badInv := inv
		badInv.Title = title
		a.Investigations = append(a.Investigations, badInv)
	}
	normalizeNotes(set, &a)
	if len(a.Findings) != 1 || a.Findings[0].Title != finding.Title || len(a.Investigations) != 1 || a.Investigations[0].Title != inv.Title {
		t.Fatalf("degenerate titles kept or good ones lost: %+v %+v", a.Findings, a.Investigations)
	}
	for _, want := range []string{`Omitted market finding "x": Its title is too short to say what it is. It said: Detail of x`, `Omitted market finding "TBD": Its title is a placeholder.`, `Omitted investigation "Pricing": Its title is too short`} {
		if !strings.Contains(a.Insights, want) {
			t.Errorf("insights lack %q:\n%s", want, a.Insights)
		}
	}

	f.Title = "x"
	got, _, err := verifyCandidates(context.Background(), cfg, set, Analysis{Fixes: []Fix{f}}, 0)
	if err != nil || len(got.Fixes)+len(got.Investigations) != 0 || !strings.Contains(got.Insights, `Omitted candidate "x": Its title is too short to say what it is. It claimed: `+f.Claim) {
		t.Fatalf("a candidate titled %q survived: %+v %v", f.Title, got, err)
	}
}

// A rebuild once got empty insights and one untitled candidate back. Verification omitted the
// candidate and the empty result was stored as a success over a good analysis. Output that thin
// is an error now, before the verification call.
func TestDegenerateAnalystOutputIsAnError(t *testing.T) {
	cfg, set, f, _, _ := verificationFixture()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	var reply Analysis
	calls := 0
	structuredCLI = func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
		calls++
		b, _ := json.Marshal(reply)
		return json.Unmarshal(b, out)
	}
	results := []run.Result{{ID: 7, PromptIdx: 0, Answerer: "fake", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Absent}, Answer: &answer.Answer{Text: "An answer."}}}
	untitled := f
	untitled.Title = ""
	insights := "Answers favour tools with a free tier; result 7 names two."
	finding := Finding{Title: "Buyers compare on setup time", Detail: "d", Evidence: "e", Prompts: []int{1}}
	for name, c := range map[string]struct {
		reply Analysis
		want  string
	}{
		"the production case":     {Analysis{Fixes: []Fix{untitled}}, "insights are 0 characters"},
		"near-empty insights":     {Analysis{Insights: "  x ", Findings: []Finding{finding}}, "insights are 1 characters"},
		"only untitled items":     {Analysis{Insights: insights, Fixes: []Fix{untitled}, Findings: []Finding{{Title: "x", Prompts: []int{1}}}}, "none of its 2 items has a usable title"},
		"one bad title among ok":  {Analysis{Insights: insights, Findings: []Finding{finding, {Title: "x", Prompts: []int{1}}}}, ""},
		"no items, real insights": {Analysis{Insights: insights}, ""},
	} {
		reply, calls = c.reply, 0
		_, _, err := Build(context.Background(), cfg, set, results, nil, nil)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if !errors.Is(err, ErrDegenerate) || !strings.Contains(err.Error(), c.want) || calls != 1 {
			t.Errorf("%s: want %q before verification, got %v after %d calls", name, c.want, err, calls)
		}
	}
}

// Run r-20261101-050336-9f0f23 got empty insights from the API twice, each billed, and the
// error said only that. Through the API, the error now carries what the reply was (stop reason,
// tokens, blocks, text length) and the prompt's size, so the stored fixes_error tells the next
// one apart from a truncation, a reply that went to thinking or a short empty answer.
func TestDegenerateAPIOutputSaysWhatTheReplyWas(t *testing.T) {
	cfg, set, _, _, _ := verificationFixture()
	cfg.Analyst = config.Model{Model: "claude-opus-5-5"}
	text := `{"fixes":[],"insights":"","investigations":[],"market_findings":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "end_turn",
			"content": []any{
				map[string]any{"type": "thinking", "thinking": "", "signature": "s"},
				map[string]any{"type": "text", "text": text},
			},
			"usage": map[string]any{"input_tokens": 118000, "output_tokens": 950},
		})
	}))
	t.Cleanup(srv.Close)
	old := llm.AnthropicURL
	llm.AnthropicURL = srv.URL
	t.Cleanup(func() { llm.AnthropicURL = old })
	t.Setenv("ANTHROPIC_API_KEY", "test")
	t.Setenv(config.AnthropicBaseURLVar, "")
	results := []run.Result{{ID: 7, PromptIdx: 0, Answerer: "fake", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Absent}, Answer: &answer.Answer{Text: "An answer."}}}
	_, _, err := Build(context.Background(), cfg, set, results, nil, nil)
	if !errors.Is(err, ErrDegenerate) {
		t.Fatalf("want ErrDegenerate, got %v", err)
	}
	for _, want := range []string{"insights are 0 characters", "stop_reason end_turn", "118000 input tokens", "950 output tokens", "blocks [thinking text]", fmt.Sprintf("%d characters of text", len(text)), "prompt "} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

// The floor counted space-separated words, so a title in Chinese, Japanese or Thai, written
// without spaces, was always too short, and an analysis written all in one of them failed as
// degenerate.
func TestTitlesInScriptsWithoutSpacesCountTheirLetters(t *testing.T) {
	for _, title := range []string{
		"定价页面缺少免费版",      // Chinese: the pricing page omits the free tier
		"价格异议",           // Chinese: pricing objections
		"料金ページに無料プランがない", // Japanese
		"料金ページ",          // Japanese: pricing page
		"หน้าราคาไม่มีแพ็กเกจฟรี", // Thai: the pricing page has no free plan
		"가격 페이지", // Korean is spaced, with dense syllables
		"Pricing 页面",
		"ꀀꀁꀂ",        // Yi is written without spaces, with syllabic characters
		"मूल्य सूची", // Hindi: price list, vowel signs and a virama on its consonants
		"नया पेज",    // Hindi: new page
		"ಬೆಲೆ ಪುಟ",   // Kannada: price page
	} {
		if why := titleProblem(title); why != "" {
			t.Errorf("%q rejected: %s", title, why)
		}
	}
	for _, title := range []string{
		"定价", "価格", "ราคา", "가격", "x 价", "ꀀꀁ",
		"x\u0301\u0302\u0303\u0304 y\u0301\u0302", // accents on Latin letters are not letters
		"x\u093e\u093f\u0940 y\u0940",             // nor are Devanagari vowel signs on a Latin letter
		"का\u093f\u0940\u0941 की",                 // a pile of vowel signs counts once
		"\u093e\u093f\u0940 \u0941\u0942\u0947",   // marks with no letter count for nothing
	} {
		if titleProblem(title) == "" {
			t.Errorf("%q accepted", title)
		}
	}

	cfg, set, _, _, _ := verificationFixture()
	old := structuredCLI
	t.Cleanup(func() { structuredCLI = old })
	structuredCLI = func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
		b, _ := json.Marshal(Analysis{Insights: "回答推荐了提供免费版的工具，第7条结果提到了两个。", Findings: []Finding{{Title: "买家比较上手时间", Detail: "回答权衡上手所需时间。", Evidence: "结果7", Prompts: []int{1}}}})
		return json.Unmarshal(b, out)
	}
	results := []run.Result{{ID: 7, PromptIdx: 0, Answerer: "fake", Track: answer.Search, Status: "done", Grade: &grade.Grade{Verdict: grade.Absent}, Answer: &answer.Answer{Text: "An answer."}}}
	a, _, err := Build(context.Background(), cfg, set, results, nil, nil)
	if err != nil || len(a.Findings) != 1 {
		t.Fatalf("a Chinese analysis failed or lost its finding: %v %+v", err, a)
	}
}
