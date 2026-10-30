package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// fakeJevJudge answers each kind of prompt question by pattern and counts the requests per kind. It
// says 0.86 for any duplicate pair, 0.9 for a prompt about sourdough and 0.05 otherwise for off
// target, info at 0.65 for a "How does" prompt and selection at 0.9 otherwise, and 0.92 for a
// prompt naming cheapseats.dev and 0.03 otherwise for branded.
type fakeJevJudge struct {
	mu       sync.Mutex
	requests []map[string]any
	byKind   map[string]int
}

func (f *fakeJevJudge) serve(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	if f.byKind == nil {
		f.byKind = map[string]int{}
	}
	f.mu.Unlock()
	state := req["state"].(map[string]any)
	answers := map[string]any{}
	for kind := range req["questions"].(map[string]any) {
		f.mu.Lock()
		f.byKind[kind]++
		f.mu.Unlock()
		prompt, _ := state["prompt"].(string)
		switch kind {
		case JudgeDuplicate:
			answers[kind] = map[string]any{"type": "noul", "noul": 0.86}
		case JudgeOffTarget:
			p := 0.05
			if strings.Contains(prompt, "sourdough") {
				p = 0.9
			}
			answers[kind] = map[string]any{"type": "noul", "noul": p}
		case JudgeIntent:
			choice, p := "selection", 0.9
			if strings.HasPrefix(prompt, "How does") {
				choice, p = "info", 0.65
			}
			answers[kind] = map[string]any{"type": "choice", "choice": choice, "confidence": 0.5, "probabilities": map[string]float64{choice: p, map[string]string{"info": "selection", "selection": "info"}[choice]: 1 - p}}
		case JudgeBranded:
			p := 0.03
			if strings.Contains(strings.ToLower(prompt), "cheapseats") {
				p = 0.92
			}
			answers[kind] = map[string]any{"type": "noul", "noul": p}
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": answers, "usage": map[string]any{"input_tokens": 200, "output_tokens": 0}})
}

func (f *fakeJevJudge) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byKind[kind]
}

func (f *fakeJevJudge) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// judgeApp is providersApp with a brand category (so the off-target question is asked) and a
// five-prompt set: two near-duplicates sharing words, one off target, one phrased as a how-does
// with no intent, one naming cheapseats.dev.
func judgeApp(t *testing.T) (*App, sets.Set, *fakeJevJudge) {
	t.Helper()
	a, _ := providersApp(t)
	a.Cfg.Brand.Category = "AI visibility trackers"
	dir := filepath.Join(a.Cfg.Dir, "prompt-sets")
	os.WriteFile(filepath.Join(dir, "five.toml"), []byte(`id = "five"
name = "Five"
[[prompts]]
id = "f1"
text = "What's the best AI visibility tracker for a small team?"
[[prompts]]
id = "f2"
text = "Which AI visibility tracker should a small team use?"
[[prompts]]
id = "f3"
text = "What's a good recipe for sourdough bread?"
[[prompts]]
id = "f4"
text = "How does share of voice tracking work?"
[[prompts]]
id = "f5"
text = "Is cheapseats.dev worth it for measuring AI answers?"
`), 0o644)
	all, err := sets.LoadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.sets = sets.Share(all, a.Cfg.Competitors)
	set, ok := a.Set("five")
	if !ok {
		t.Fatal("no set five")
	}
	f := &fakeJevJudge{}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, srv.URL)
	return a, set, f
}

func TestJudgePromptsAsksEachQuestionOnceAndFlagsAtTheThreshold(t *testing.T) {
	a, set, f := judgeApp(t)
	ctx := context.Background()

	j, err := a.JudgePrompts(ctx, set)
	if err != nil {
		t.Fatal(err)
	}
	// One pair shares a meaningful word (visibility, tracker, team); every prompt gets the other
	// three questions. Sixteen requests, one question each.
	if f.count(JudgeDuplicate) != 1 || f.count(JudgeOffTarget) != 5 || f.count(JudgeIntent) != 5 || f.count(JudgeBranded) != 5 || f.total() != 16 || j.Asked != 16 {
		t.Fatalf("requests per kind: %v (asked %d, %v)", f.byKind, j.Asked, j.AskedByKind)
	}
	for _, req := range f.requests {
		qs := req["questions"].(map[string]any)
		if len(qs) != 1 {
			t.Fatalf("one question per request: %v", qs)
		}
		state := req["state"].(map[string]any)
		for kind, q := range qs {
			q := q.(map[string]any)
			switch kind {
			case JudgeDuplicate:
				if state["category"] != "AI visibility trackers" || state["a"] == "" || state["b"] == "" || q["type"] != "noul" {
					t.Errorf("duplicate state: %v", state)
				}
			case JudgeOffTarget:
				if state["audience"] != "a software developer" || state["category"] != "AI visibility trackers" || q["type"] != "noul" {
					t.Errorf("off_target state: %v", state)
				}
			case JudgeIntent:
				c := q["criteria"].(map[string]any)
				if q["type"] != "choice" || c["selection"] == nil || c["info"] == nil || state["category"] != "AI visibility trackers" {
					t.Errorf("intent criteria: %v %v", q, state)
				}
				// A product being useful for the job must not force a method into selection.
				selection, info := c["selection"].(string), c["info"].(string)
				if !strings.Contains(selection, "required to satisfy") || strings.Contains(selection, "even when none was asked for") ||
					!strings.Contains(info, "manual/API method") || !strings.Contains(info, "without selecting a product") {
					t.Errorf("intent must allow complete product-free methods: selection=%q info=%q", selection, info)
				}
			case JudgeBranded:
				b := state["brand"].(map[string]any)
				if b["name"] != "Cheap Seats" || !strings.Contains(mustJSON(b["sites"]), "cheapseats.dev") || b["about"] == "" || q["type"] != "noul" {
					t.Errorf("branded state: %v", state)
				}
			}
		}
	}
	// The flags at the default threshold (0.7): the pair, the recipe, the domain; the info
	// reading at 0.65 is below it.
	ids := func(fs []PromptJudgment) string {
		var out []string
		for _, f := range fs {
			out = append(out, strings.Join(f.Prompts, "+"))
		}
		return strings.Join(out, " ")
	}
	if ids(j.Duplicates) != "f1+f2" || ids(j.OffTarget) != "f3" || ids(j.Intent) != "" || ids(j.Branded) != "f5" {
		t.Errorf("flags: dup %q off %q intent %q branded %q", ids(j.Duplicates), ids(j.OffTarget), ids(j.Intent), ids(j.Branded))
	}
	if j.Judged != 16 || j.Pending != 0 || j.Count() != 3 || j.Duplicates[0].Confidence != 0.86 || j.Duplicates[0].Choice != "yes" {
		t.Errorf("judged %d pending %d count %d first %+v", j.Judged, j.Pending, j.Count(), j.Duplicates[0])
	}
	if l := j.Duplicates[0].Line; l != "f1 and f2 may ask the same thing" {
		t.Errorf("line %q", l)
	}
	if l := j.OffTarget[0].Line; l != "f3 may ask about something other than AI visibility trackers" {
		t.Errorf("line %q", l)
	}
	if l := j.Branded[0].Line; !strings.Contains(l, "f5 may name the brand") {
		t.Errorf("line %q", l)
	}
	var stored int
	a.DB.QueryRow(`SELECT COUNT(*) FROM prompt_judgments WHERE set_id='five' AND source='jev'`).Scan(&stored)
	if stored != 16 {
		t.Errorf("stored rows %d", stored)
	}

	// Asking again asks nothing.
	if j2, err := a.JudgePrompts(ctx, set); err != nil || j2.Asked != 0 || f.total() != 16 || j2.Count() != 3 {
		t.Errorf("second call asked %d (%v), flags %d", j2.Asked, err, j2.Count())
	}

	// A lower threshold reads the same rows differently, asking nothing: the info reading shows.
	a.Cfg.TypeSafe.MinConfidence = 0.6
	j3, err := a.PromptJudgments(ctx, set)
	if err != nil || f.total() != 16 {
		t.Fatalf("reading asked Jev (%v)", err)
	}
	if ids(j3.Intent) != "f4" || !strings.Contains(j3.Intent[0].Line, `consider intent = "info"`) || j3.Intent[0].Confidence != 0.65 {
		t.Errorf("at 0.6 the intent flag shows: %+v", j3.Intent)
	}
	// A higher one drops the pair and keeps the two strong flags.
	a.Cfg.TypeSafe.MinConfidence = 0.9
	j4, _ := a.PromptJudgments(ctx, set)
	if ids(j4.Duplicates) != "" || ids(j4.OffTarget) != "f3" || ids(j4.Branded) != "f5" || f.total() != 16 {
		t.Errorf("at 0.9: dup %q off %q branded %q", ids(j4.Duplicates), ids(j4.OffTarget), ids(j4.Branded))
	}
	a.Cfg.TypeSafe.MinConfidence = 0

	// Rewording one prompt re-asks only the questions that saw it: its pair, its own three.
	reworded := set
	reworded.Prompts = append([]sets.Prompt(nil), set.Prompts...)
	reworded.Prompts[1].Text = "Which AI visibility tracker suits a small startup?"
	j5, err := a.JudgePrompts(ctx, reworded)
	if err != nil {
		t.Fatal(err)
	}
	if j5.Asked != 4 || f.count(JudgeDuplicate) != 2 || f.count(JudgeOffTarget) != 6 || f.count(JudgeIntent) != 6 || f.count(JudgeBranded) != 6 {
		t.Errorf("rewording f2 asked %d: %v", j5.Asked, f.byKind)
	}
	if ids(j5.Duplicates) != "f1+f2" || j5.Judged != 16 || j5.Pending != 0 {
		t.Errorf("after rewording: dup %q judged %d pending %d", ids(j5.Duplicates), j5.Judged, j5.Pending)
	}
	// And the old wording's rows are still there, unmatched, so going back costs nothing.
	if j6, err := a.JudgePrompts(ctx, set); err != nil || j6.Asked != 0 {
		t.Errorf("the old wording asked %d (%v)", j6.Asked, err)
	}
	// Spend is in the ledger under the pass's name and the set.
	var events int
	a.DB.QueryRow(`SELECT COUNT(*) FROM spend_events WHERE kind='provider-attempt/prompt-review/five'`).Scan(&events)
	if events == 0 {
		t.Error("no ledger events for the pass")
	}
}

func TestJudgePromptsSkipsBrandedForHeadToHeads(t *testing.T) {
	a, _, f := judgeApp(t)
	h2h := sets.Set{ID: sets.HeadToHeads, Prompts: []sets.Prompt{{ID: "h1", Text: "Is Cheap Seats better than Profound for tracking AI answers?"}}}
	j, err := a.JudgePrompts(context.Background(), h2h)
	if err != nil {
		t.Fatal(err)
	}
	if f.count(JudgeBranded) != 0 || f.count(JudgeIntent) != 1 || f.count(JudgeOffTarget) != 1 || j.Asked != 2 {
		t.Errorf("head-to-heads: %v", f.byKind)
	}
}

func TestJudgeRunJudgesTheRunsSetAndIsRegistered(t *testing.T) {
	a, id := providersApp(t)
	f := &fakeJevJudge{}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	defer srv.Close()
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, srv.URL)
	p, ok := PassByCommand("judge")
	if !ok || p.Name != "prompt-review" || p.Order != 30 || p.Provider != config.TypeSafeProvider || !p.On(a.Cfg) {
		t.Fatalf("pass: %+v", p)
	}
	// Set s has two prompts sharing no meaningful word, and the brand has no category: intent and
	// branded per prompt, no pair, and no off-target question (against the description it would
	// flag everything).
	n, err := a.RunPass(context.Background(), p, id)
	if err != nil || n != 4 || f.count(JudgeDuplicate) != 0 || f.count(JudgeOffTarget) != 0 || f.count(JudgeBranded) != 2 {
		t.Fatalf("pass asked %d (%v): %v", n, err, f.byKind)
	}
	if n, err := a.RunPass(context.Background(), p, id); err != nil || n != 0 {
		t.Errorf("second pass asked %d (%v)", n, err)
	}
	// The review carries the judgments, without asking.
	rev, err := a.Review(context.Background(), "s", 30)
	if err != nil || rev.Jev == nil || rev.Jev.Judged != 4 || f.total() != 4 {
		t.Fatalf("review: %v jev %+v requests %d", err, rev.Jev, f.total())
	}
	// Without [typesafe], the review has no Jev section at all.
	a.Cfg.TypeSafe = nil
	if rev, err := a.Review(context.Background(), "s", 30); err != nil || rev.Jev != nil {
		t.Errorf("without [typesafe]: %v %+v", err, rev.Jev)
	}
}

func TestJudgePromptsNeedsAKeyOrProxy(t *testing.T) {
	a, set, _ := judgeApp(t)
	t.Setenv(config.TypeSafeBaseURLVar, "")
	t.Setenv(typesafe.KeyVar, "")
	if _, err := a.JudgePrompts(context.Background(), set); err == nil || !strings.Contains(err.Error(), typesafe.KeyVar) {
		t.Errorf("err %v", err)
	}
}

// Setting the category afterwards asks the off-target question for every prompt and nothing
// else; changing it re-asks only that; and the intent question, which takes the category as
// context, is re-asked when the context it saw changes.
func TestJudgePromptsAsksOffTargetOnlyWithACategory(t *testing.T) {
	a, set, f := judgeApp(t)
	ctx := context.Background()
	a.Cfg.Brand.Category = ""
	if j, err := a.JudgePrompts(ctx, set); err != nil || f.count(JudgeOffTarget) != 0 || j.Asked != 11 || len(j.OffTarget) != 0 {
		t.Fatalf("without a category: asked %d (%v) %v", j.Asked, err, f.byKind)
	}
	a.Cfg.Brand.Category = "AI visibility trackers"
	// off_target for five prompts, and intent again for five, since its context changed from the
	// description to the category.
	if j, err := a.JudgePrompts(ctx, set); err != nil || f.count(JudgeOffTarget) != 5 || f.count(JudgeIntent) != 10 || j.Asked != 10 || len(j.OffTarget) != 1 {
		t.Fatalf("with a category: asked %d (%v) %v, off %d", j.Asked, err, f.byKind, len(j.OffTarget))
	}
	a.Cfg.Brand.Category = "AI search visibility tools"
	if j, err := a.JudgePrompts(ctx, set); err != nil || f.count(JudgeOffTarget) != 10 || f.count(JudgeIntent) != 15 || f.count(JudgeBranded) != 5 || f.count(JudgeDuplicate) != 1 {
		t.Fatalf("changing the category: asked %d (%v) %v", j.Asked, err, f.byKind)
	}
}

// A question's key changes with its wording, so a rewrite of a question in judgeQuestions
// re-asks it rather than reading last month's answers to a different question.
func TestJudgeKeyHashesTheQuestionsWording(t *testing.T) {
	q := typesafe.Question{Type: "noul", Instructions: "is it?"}
	k := judgeKey(JudgeIntent, q, "a prompt")
	if k != judgeKey(JudgeIntent, q, "a  prompt") {
		t.Error("whitespace in the parts should not count")
	}
	if k == judgeKey(JudgeIntent, typesafe.Question{Type: "noul", Instructions: "is it really?"}, "a prompt") {
		t.Error("reworded instructions should change the key")
	}
	c1 := typesafe.Question{Type: "choice", Criteria: map[string]any{"selection": "a", "info": "b"}}
	c2 := typesafe.Question{Type: "choice", Criteria: map[string]any{"selection": "a", "info": "c"}}
	if judgeKey(JudgeIntent, c1, "p") == judgeKey(JudgeIntent, c2, "p") {
		t.Error("a changed criterion should change the key")
	}
}

func TestSharesWordIgnoresQuestionWords(t *testing.T) {
	if sharesWord("What's the best tool for X?", "What's the best tool for Y?") {
		t.Error("best, tool and what are not meaningful words")
	}
	if !sharesWord("Which AI visibility tracker should I use?", "What's the best AI visibility tracker?") {
		t.Error("visibility and tracker are")
	}
}
