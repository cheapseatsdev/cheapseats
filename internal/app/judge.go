package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// Prompt judgments (docs/optional-providers.md, docs/prompt-review.md). The prompt review finds
// duplicates and off-target prompts from a month of answers, so a new set has none; intent is
// set by hand; the branded check is a whole-word match that misses "cheapseats.dev". With
// [typesafe] configured, each prompt's wording is put to Jev once: four kinds of question, one
// per request, each stored in prompt_judgments under a hash of what the question saw and how it
// was worded, so a reworded prompt (or a reworded question) is asked again and nothing else costs
// anything. The off-target question needs brand.category, the market in a noun phrase: asked
// against the product description in brand.about it flags every prompt that isn't about this one
// product, which is most of them, so without a category it isn't asked. cheapseats
// check prints the flags as suggestions; the prompt review shows them in a read-only section;
// after a run the pass judges the run's set, so new prompts are judged at their first run. No
// page ever asks.

const (
	JudgeDuplicate = "duplicate"
	JudgeOffTarget = "off_target"
	JudgeIntent    = "intent"
	JudgeBranded   = "branded"
)

// JudgeKinds are the four questions, in the order they're asked and listed.
var JudgeKinds = []struct{ Kind, Title, What string }{
	{JudgeDuplicate, "Duplicates", "Two prompts of the same intent that one good answer would serve."},
	{JudgeOffTarget, "Off target", "A prompt that asks about something other than the brand's category (brand.category; not asked without it)."},
	{JudgeIntent, "Intent", "A prompt that reads as the other intent from the one it's set to: the answer that serves the buyer names a product in the category (selection) or names none (info)."},
	{JudgeBranded, "Branded", "Outside head-to-heads, a prompt that names the brand, a product of its maker or its domain, in any spelling."},
}

func init() {
	RegisterPass(Pass{
		Name: "prompt-review", Command: "judge", Order: 30, Provider: config.TypeSafeProvider,
		Usage: "put the run's set's prompts to Jev: duplicates, off target, intent, branded ([typesafe])",
		On:    (*config.Config).PromptReviewOn,
		Run:   (*App).JudgeRun,
		Print: func(a *App, ctx context.Context, id string, n int) {
			r, err := a.Runs.Get(ctx, id)
			if err != nil {
				fmt.Println("cheapseats:", err)
				return
			}
			s, ok := a.Set(r.SetID)
			if !ok {
				fmt.Printf("set %s is no longer configured\n", r.SetID)
				return
			}
			j, err := a.PromptJudgments(ctx, s)
			if err != nil {
				fmt.Println("cheapseats:", err)
				return
			}
			fmt.Printf("asked Jev %d questions about the prompts of set %s; %d answered, %d flagged, %d not asked yet\n", n, s.ID, j.Judged, j.Count(), j.Pending)
			for _, f := range j.All() {
				fmt.Printf("  %-10s %s (%.2f)\n", f.Kind, f.Line, f.Confidence)
			}
		},
	})
}

// A PromptJudgment is one stored answer about one prompt, or a pair, read at today's threshold.
type PromptJudgment struct {
	Kind    string   `json:"kind"`
	Prompts []string `json:"prompts"` // one id, or two for a duplicate
	Texts   []string `json:"texts"`
	// Choice is yes or no (duplicate, off_target, branded) or selection or info (intent);
	// Confidence is Jev's probability for it: for a yes/no question, the probability of yes.
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
	// Line is the flag in a sentence ("p3 and p7 may ask the same thing"), Suggestion what to do.
	Line       string `json:"line"`
	Suggestion string `json:"suggestion"`
	DecidedAt  string `json:"decided_at"`
}

// PromptJudgments are a set's flags from Jev at today's threshold, by kind, with how much of
// the set has been judged. Only flags are listed: a prompt Jev found fine isn't.
type PromptJudgments struct {
	SetID      string           `json:"set_id"`
	Duplicates []PromptJudgment `json:"duplicates"`
	OffTarget  []PromptJudgment `json:"off_target"`
	Intent     []PromptJudgment `json:"intent"`
	Branded    []PromptJudgment `json:"branded"`
	// Judged is how many of the set's questions have a stored answer, Pending how many haven't
	// been asked yet (check, review -jev or the next run asks them).
	Judged  int `json:"judged"`
	Pending int `json:"pending"`
	// Asked is how many requests the call that returned this made, in all and by kind.
	Asked       int            `json:"asked,omitempty"`
	AskedByKind map[string]int `json:"asked_by_kind,omitempty"`
	// The probability a flag needed, as configured when it was read.
	MinConfidence float64 `json:"min_confidence"`
}

// All is every flag, in JudgeKinds order.
func (j PromptJudgments) All() []PromptJudgment {
	out := append([]PromptJudgment{}, j.Duplicates...)
	out = append(out, j.OffTarget...)
	out = append(out, j.Intent...)
	return append(out, j.Branded...)
}

// Count is how many flags there are.
func (j PromptJudgments) Count() int {
	return len(j.Duplicates) + len(j.OffTarget) + len(j.Intent) + len(j.Branded)
}

// JudgeRun is the after-run pass: it judges the run's set as configured today, so a prompt is
// judged at its first run. A set no longer configured is left alone.
func (a *App) JudgeRun(ctx context.Context, id string) (int, error) {
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	s, ok := a.Set(r.SetID)
	if !ok {
		slog.Info("prompt review skipped", "run", id, "reason", "set "+r.SetID+" is no longer configured")
		return 0, nil
	}
	j, err := a.JudgePrompts(ctx, s)
	return j.Asked, err
}

// JudgePrompts asks Jev the set's questions that have no stored answer, one per request, stores
// each answer, and returns the set's flags at today's threshold. Asking again asks nothing; a
// reworded prompt is asked only its own questions. On an error part way, what was stored is
// kept and returned with the error.
func (a *App) JudgePrompts(ctx context.Context, s sets.Set) (PromptJudgments, error) {
	if m := config.Missing(config.TypeSafeProvider); m != "" {
		return PromptJudgments{SetID: s.ID}, fmt.Errorf("typesafe: %s", m)
	}
	qs := judgeQuestions(a.Cfg.Brand, s)
	rows, err := a.judgeRows(ctx)
	if err != nil {
		return PromptJudgments{SetID: s.ID}, err
	}
	client := a.TypeSafe()
	asked := 0
	byKind := map[string]int{}
	var askErr error
	for _, q := range qs {
		if _, ok := rows[[2]string{q.kind, q.key}]; ok {
			continue
		}
		// One question per request, with only what it needs in the state: Jev's probabilities
		// fall as the state fills with detail unrelated to the question.
		resp, err := client.Ask(ctx, "prompt-review/"+s.ID, q.state, map[string]typesafe.Question{q.kind: q.question})
		if err != nil {
			askErr = err
			break
		}
		ans, ok := resp.Answers[q.kind]
		if !ok {
			askErr = fmt.Errorf("typesafe: no answer to %s", q.kind)
			break
		}
		choice := ans.Choice
		if ans.Type == "noul" || q.question.Type == "noul" {
			choice = "no"
			if ans.Noul >= 0.5 {
				choice = "yes"
			}
		}
		ids, _ := json.Marshal(q.ids())
		row := judgeRow{choice: choice, confidence: typesafe.Chosen(ans), decidedAt: store.Now()}
		if _, err := a.DB.ExecContext(ctx, `INSERT OR REPLACE INTO prompt_judgments(kind, key, set_id, prompt_ids, choice, confidence, source, decided_at) VALUES (?,?,?,?,?,?,'jev',?)`,
			q.kind, q.key, s.ID, string(ids), row.choice, row.confidence, row.decidedAt); err != nil {
			askErr = err
			break
		}
		rows[[2]string{q.kind, q.key}] = row
		asked++
		byKind[q.kind]++
		slog.Info("prompt review", "set", s.ID, "kind", q.kind, "prompts", strings.Join(q.ids(), ","), "choice", choice, "p", fmt.Sprintf("%.2f", row.confidence), "model", resp.Model, "cost_usd", fmt.Sprintf("%.5f", resp.CostUSD))
	}
	j := a.readJudgments(s, qs, rows)
	j.Asked, j.AskedByKind = asked, byKind
	return j, askErr
}

// PromptJudgments reads the set's stored flags at today's threshold and asks nothing: what the
// review page, the API and MCP show.
func (a *App) PromptJudgments(ctx context.Context, s sets.Set) (PromptJudgments, error) {
	rows, err := a.judgeRows(ctx)
	if err != nil {
		return PromptJudgments{SetID: s.ID}, err
	}
	return a.readJudgments(s, judgeQuestions(a.Cfg.Brand, s), rows), nil
}

type judgeRow struct {
	choice     string
	confidence float64
	decidedAt  string
}

// judgeRows is the whole table, by kind and key: a few rows per prompt, and a prompt moved
// between sets keeps its answers.
func (a *App) judgeRows(ctx context.Context) (map[[2]string]judgeRow, error) {
	out := map[[2]string]judgeRow{}
	if a.DB == nil {
		return out, nil
	}
	rows, err := a.DB.QueryContext(ctx, `SELECT kind, key, choice, confidence, decided_at FROM prompt_judgments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, key string
		var r judgeRow
		if err := rows.Scan(&kind, &key, &r.choice, &r.confidence, &r.decidedAt); err != nil {
			return nil, err
		}
		out[[2]string{kind, key}] = r
	}
	return out, rows.Err()
}

// readJudgments turns the stored answers to the set's current questions into flags at the
// configured threshold. A row for a question the set no longer asks (a reworded prompt) is
// simply not matched.
func (a *App) readJudgments(s sets.Set, qs []judgeQuestion, rows map[[2]string]judgeRow) PromptJudgments {
	min := a.Cfg.MinConfidence()
	j := PromptJudgments{SetID: s.ID, Duplicates: []PromptJudgment{}, OffTarget: []PromptJudgment{}, Intent: []PromptJudgment{}, Branded: []PromptJudgment{}, MinConfidence: min}
	category := a.Cfg.Brand.Category
	for _, q := range qs {
		r, ok := rows[[2]string{q.kind, q.key}]
		if !ok {
			j.Pending++
			continue
		}
		j.Judged++
		if r.confidence < min {
			continue
		}
		f := PromptJudgment{Kind: q.kind, Prompts: q.ids(), Choice: r.choice, Confidence: r.confidence, DecidedAt: r.decidedAt}
		for _, p := range q.prompts {
			f.Texts = append(f.Texts, p.Text)
		}
		ids := strings.Join(q.ids(), " and ")
		switch q.kind {
		case JudgeDuplicate:
			f.Line = ids + " may ask the same thing"
			f.Suggestion = "Merge them to save answers: keep one, retire the other."
		case JudgeOffTarget:
			f.Line = fmt.Sprintf("%s may ask about something other than %s", ids, category)
			f.Suggestion = "Add context to the prompt (the category, who is asking), or retire it."
		case JudgeIntent:
			// A flag only when Jev reads the prompt as the other intent from the one it's set to.
			if (r.choice == sets.Info) == q.prompts[0].Info() {
				continue
			}
			if r.choice == sets.Info {
				f.Line = ids + ` reads as a question a good answer serves without naming a product; consider intent = "info"`
				f.Suggestion = `Set intent = "info", so it's scored on being named rather than recommended.`
			} else {
				f.Line = ids + ` reads as a question a good answer names a product for; consider dropping intent = "info"`
				f.Suggestion = `Drop intent = "info", so it's scored on being recommended.`
			}
		case JudgeBranded:
			f.Line = ids + " may name the brand; only " + sets.HeadToHeads + " may"
			f.Suggestion = "Reword it as a buyer who hasn't heard of the brand would ask."
		}
		switch q.kind {
		case JudgeDuplicate:
			j.Duplicates = append(j.Duplicates, f)
		case JudgeOffTarget:
			j.OffTarget = append(j.OffTarget, f)
		case JudgeIntent:
			j.Intent = append(j.Intent, f)
		case JudgeBranded:
			j.Branded = append(j.Branded, f)
		}
	}
	return j
}

// A judgeQuestion is one question about one prompt, or a pair, with the state it sees and the
// key that state and the question's wording hash to.
type judgeQuestion struct {
	kind     string
	key      string
	prompts  []sets.Prompt
	state    map[string]any
	question typesafe.Question
}

func (q judgeQuestion) ids() []string {
	var out []string
	for _, p := range q.prompts {
		out = append(out, p.ID)
	}
	return out
}

// judgeQuestions is every question the set's prompts raise, in JudgeKinds order and set order:
// duplicate over each pair of the same intent that shares a meaningful word, off_target and
// intent per prompt, branded per prompt outside head-to-heads. Each key hashes exactly what the
// question sees (the texts, and for off_target and branded the brand facts) and the question's
// own wording, so a reworded prompt, a changed category, or a change to a question here re-asks
// only what it changes. off_target is asked only with brand.category set; the others take the
// category as context and fall back to the description.
func judgeQuestions(brand config.Brand, s sets.Set) []judgeQuestion {
	category := categoryOf(brand)
	var out []judgeQuestion
	for i, p := range s.Prompts {
		for _, o := range s.Prompts[i+1:] {
			if p.Intent != o.Intent || !sharesWord(p.Text, o.Text) {
				continue
			}
			a, b := foldSpace(p.Text), foldSpace(o.Text)
			if a > b {
				a, b = b, a
			}
			q := typesafe.Question{Type: "noul", Instructions: "`a` and `b` are two questions buyers might ask an AI assistant about `category`. One good answer would serve both: they ask for the same thing in other words, so a page that answers one answers the other. Two questions about the same subject that ask for different things (the price against the features, a comparison against a how-to, a product for a different kind of user) are different questions."}
			out = append(out, judgeQuestion{
				kind: JudgeDuplicate, key: judgeKey(JudgeDuplicate, q, a, b), prompts: []sets.Prompt{p, o},
				state:    map[string]any{"category": category, "a": p.Text, "b": o.Text},
				question: q,
			})
		}
	}
	if brand.Category != "" {
		for _, p := range s.Prompts {
			q := typesafe.Question{Type: "noul", Instructions: "`prompt` is one of the questions asked of AI assistants to measure whether a product in `category` gets recommended; `audience` is who the questions are written as. `prompt` asks about something other than `category`: a good answer to it would have no reason to name a product in `category`. A question that asks for products, approaches or advice in `category` without naming the category is about `category`, and so is a question about a problem that products in `category` exist to solve, or about choosing between named products in it."}
			out = append(out, judgeQuestion{
				kind: JudgeOffTarget, key: judgeKey(JudgeOffTarget, q, p.Text, brand.Category, brand.Audience), prompts: []sets.Prompt{p},
				state:    map[string]any{"category": brand.Category, "audience": brand.Audience, "prompt": p.Text},
				question: q,
			})
		}
	}
	// Intent is about what the grader scores, not the grammar: a how-do-I question from a buyer
	// with a job that products in the category do is a selection, since the answer that serves
	// the buyer names one.
	for _, p := range s.Prompts {
		q := typesafe.Question{Type: "choice", Instructions: "`prompt` is a question a buyer asks an AI assistant, and `category` is a kind of product. Which kind of answer serves the buyer?",
			Criteria: map[string]any{
				"selection": "an answer that names a product or tool in `category`: the buyer asks which to use, for the best ones, for alternatives to one, for a comparison, or how to do a job that products in `category` exist to do (how do I measure X, is there a way to Y, can I run Z), so the useful answer points at a product even when none was asked for",
				sets.Info:   "an answer that names no product in `category`: the buyer asks what something is, how it works, whether a practice is worth doing, about a named product's own details, or how to do something no product in `category` does",
			}}
		out = append(out, judgeQuestion{
			kind: JudgeIntent, key: judgeKey(JudgeIntent, q, p.Text, category), prompts: []sets.Prompt{p},
			state:    map[string]any{"category": category, "prompt": p.Text},
			question: q,
		})
	}
	if s.ID != sets.HeadToHeads {
		var sites []string
		for _, st := range brand.Sites {
			sites = append(sites, st.URL)
		}
		sites = append(sites, brand.Domains...)
		facts := []string{brand.Name, strings.Join(brand.Aliases, "\n"), brand.About, strings.Join(sites, "\n")}
		for _, p := range s.Prompts {
			q := typesafe.Question{Type: "noul", Instructions: "`prompt` names the brand `brand.name`, one of its `brand.aliases`, a product of its maker, or one of its web domains in `brand.sites`, in any spelling: with or without spaces, hyphens, punctuation or capitals, with a domain suffix, or as part of a web address. A question that only describes what the brand does (`brand.about`) without naming it does not name it."}
			out = append(out, judgeQuestion{
				kind: JudgeBranded, key: judgeKey(JudgeBranded, q, append([]string{p.Text}, facts...)...), prompts: []sets.Prompt{p},
				state:    map[string]any{"brand": map[string]any{"name": brand.Name, "aliases": brand.Aliases, "about": brand.About, "sites": sites}, "prompt": p.Text},
				question: q,
			})
		}
	}
	return out
}

// judgeKey hashes what a question sees and how it is worded (its type, instructions and
// criteria, as JSON, which sorts the criteria), so a change to a question here re-asks it.
// Whitespace in the parts doesn't count, as in sets.Prompt.SHA.
func judgeKey(kind string, q typesafe.Question, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	wording, _ := json.Marshal(q)
	h.Write([]byte{0})
	h.Write(wording)
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(foldSpace(p)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func foldSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// promptStop are the words two buyer questions share without asking the same thing, on top of
// fold.go's stop list: question words, verbs of asking and choosing, and the words that make a
// question ("best", "good", "options").
var promptStop = map[string]bool{
	"what": true, "whats": true, "which": true, "who": true, "whom": true, "how": true, "why": true, "when": true, "where": true,
	"does": true, "doing": true, "did": true, "should": true, "could": true, "would": true, "can": true, "will": true,
	"the": true, "and": true, "for": true, "are": true, "was": true, "were": true, "with": true, "that": true, "this": true,
	"these": true, "those": true, "there": true, "than": true, "then": true, "from": true, "into": true, "about": true,
	"any": true, "all": true, "some": true, "most": true, "more": true, "much": true, "many": true, "one": true, "ones": true,
	"you": true, "your": true, "our": true, "its": true, "their": true, "them": true, "they": true, "have": true, "has": true,
	"had": true, "use": true, "using": true, "used": true, "need": true, "want": true, "get": true, "getting": true,
	"make": true, "like": true, "out": true, "over": true, "also": true, "just": true, "between": true, "versus": true,
	"best": true, "good": true, "top": true, "great": true, "right": true, "options": true, "option": true, "way": true, "ways": true,
	"recommend": true, "recommended": true, "recommendation": true, "recommendations": true, "worth": true, "something": true,
	"anyone": true, "someone": true, "really": true, "actually": true, "still": true, "other": true, "another": true,
}

// sharesWord is the cheap pre-filter for a duplicate question: the two prompts share a word
// that isn't a stop word.
func sharesWord(a, b string) bool {
	ta := promptWords(a)
	for w := range promptWords(b) {
		if ta[w] {
			return true
		}
	}
	return false
}

func promptWords(s string) map[string]bool {
	out := tokens(s)
	for w := range out {
		if promptStop[w] {
			delete(out, w)
		}
	}
	return out
}
