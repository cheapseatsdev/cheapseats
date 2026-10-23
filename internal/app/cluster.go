package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/opinion"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// Clustering objections and praise (docs/optional-providers.md). The grader writes objections
// and praise as free text, and the run page and the sentiment count them by the folded string, so
// "coverage unverified" and "coverage of ChatGPT and Claude unverified" are two rows. After a
// run, with [typesafe] configured, each wording not decided before is put to Jev once: which of
// the clusters already counted, if any, says the same thing? Decisions land in phrase_clusters
// with Jev's probability for the cluster it chose; PhraseClusters reads them back for every
// report at today's threshold, so no page calls Jev and switching it off leaves the counts as
// they were.

func init() {
	RegisterPass(Pass{
		Name: "cluster-phrases", Command: "cluster", Order: 15, Provider: config.TypeSafeProvider,
		Usage: "put the run's new objection and praise wordings to Jev ([typesafe])",
		On:    (*config.Config).ClusterPhrasesOn,
		Run:   (*App).ClusterPhrases,
		Print: func(a *App, ctx context.Context, id string, n int) {
			fmt.Printf("asked Jev about %d new wordings\n", n)
			decisions, err := a.PhraseDecisions(ctx)
			if err != nil {
				fmt.Println("cheapseats:", err)
			}
			for _, d := range decisions {
				if d.Cluster != "" {
					fmt.Printf("  %-9s %-40s -> %-32s %.2f%s\n", d.Kind, d.Phrase, d.Display, d.Confidence, map[bool]string{true: "", false: "  (below the threshold)"}[d.Folds])
				}
			}
		},
	})
}

// The kinds of phrase the grader writes and the pass clusters.
const (
	objectionKind = "objection"
	praiseKind    = "praise"
)

var phraseKinds = []string{objectionKind, praiseKind}

// phraseCache is the clustering map per kind, rebuilt after a decision is written or the
// process starts.
type phraseCache struct {
	mu sync.Mutex
	m  map[string]map[string]string
}

// PhraseClusters is the counting map for one kind: a folded phrase the grader wrote, to the
// wording of the cluster it counts under, as that wording was first written. It holds the stored
// decisions at or above the configured confidence, one hop deep, and a phrase that is its own
// cluster maps to its own first wording. An empty map leaves every count as it is.
func (a *App) PhraseClusters(kind string) map[string]string {
	a.phrases.mu.Lock()
	defer a.phrases.mu.Unlock()
	if m, ok := a.phrases.m[kind]; ok {
		return m
	}
	m := map[string]string{}
	if a.DB != nil {
		rows, err := a.DB.Query(`SELECT phrase, cluster, display, confidence FROM phrase_clusters WHERE kind=? ORDER BY phrase, confidence DESC`, kind)
		if err != nil {
			slog.Error("phrase clusters", "kind", kind, "err", err)
		} else {
			min := a.Cfg.MinConfidence()
			seen := map[string]bool{}
			for rows.Next() {
				var p, c, d string
				var conf float64
				if rows.Scan(&p, &c, &d, &conf) != nil || seen[p] {
					continue
				}
				seen[p] = true
				// A member below the threshold is its own cluster today, under its own wording,
				// which this run's first wording supplies.
				if c != "" && conf < min {
					continue
				}
				m[p] = d
			}
			rows.Close()
		}
	}
	// One hop: a phrase clustered onto a phrase that is itself clustered.
	for p, d := range m {
		if d2, ok := m[grade.Fold(d)]; ok && grade.Fold(d2) != p {
			m[p] = d2
		}
	}
	if a.phrases.m == nil {
		a.phrases.m = map[string]map[string]string{}
	}
	a.phrases.m[kind] = m
	return m
}

func (a *App) invalidatePhrases() {
	a.phrases.mu.Lock()
	a.phrases.m = nil
	a.phrases.mu.Unlock()
}

// PhraseOptions is the sentiment's counting maps, praise and objections, at today's threshold.
func (a *App) PhraseOptions() opinion.Options {
	return opinion.Options{Praise: a.PhraseClusters(praiseKind), Objections: a.PhraseClusters(objectionKind)}
}

// Opinion is the run's opinion report with praise and objections counted through the stored
// clusters.
func (a *App) Opinion(ctx context.Context, id string, results []run.Result) opinion.Report {
	return a.Runs.OpinionWith(ctx, id, results, a.PhraseOptions())
}

// phraseUse is one wording the run's grades used, with how many answers used it, how it was
// first written, and a sentence of context from the grade that first raised it.
type phraseUse struct {
	key, display, context string
	answers               int
}

// cluster is a phrase that other wordings can be clustered onto: a stored decision with no
// cluster of its own.
type cluster struct {
	key, display, example string
	members               int
}

// ClusterPhrases puts the run's new objection and praise wordings to Jev and stores its
// decisions. It returns how many wordings were asked about. Wordings decided before are skipped,
// and a wording with no plausible cluster is stored as its own without a question, so re-running
// it costs nothing.
func (a *App) ClusterPhrases(ctx context.Context, id string) (int, error) {
	if m := config.Missing(config.TypeSafeProvider); m != "" {
		return 0, fmt.Errorf("typesafe: %s", m)
	}
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	results, err := a.Runs.Results(ctx, id)
	if err != nil {
		return 0, err
	}
	category := categoryOf(a.RunBrand(r))
	client := a.TypeSafe()
	asked, cost := 0, 0.0
	model := ""
	for _, kind := range phraseKinds {
		n, c, m, err := a.clusterKind(ctx, client, id, kind, category, results)
		asked += n
		cost += c
		if m != "" {
			model = m
		}
		if err != nil {
			return asked, err
		}
	}
	if asked > 0 {
		slog.Info("cluster phrases", "run", id, "asked", asked, "model", model, "cost_usd", fmt.Sprintf("%.4f", cost))
	}
	a.invalidatePhrases()
	return asked, nil
}

// clusterKind clusters one kind's wordings and returns how many were asked about, what the
// requests cost, and the model that answered.
func (a *App) clusterKind(ctx context.Context, client *typesafe.Client, id, kind, category string, results []run.Result) (int, float64, string, error) {
	// The run's wordings, counted once per answer naming the brand, as the reports count them.
	uses := map[string]*phraseUse{}
	for _, x := range results {
		g := x.Grade
		if !x.Graded() || g.Verdict == grade.Absent || g.Verdict == "" {
			continue
		}
		items := g.Objections
		if kind == praiseKind {
			items = g.Praise
		}
		seen := map[string]bool{}
		for _, it := range items {
			k := grade.Fold(it)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			u := uses[k]
			if u == nil {
				u = &phraseUse{key: k, display: strings.Join(strings.Fields(it), " "), context: phraseContext(g, k)}
				uses[k] = u
			}
			u.answers++
		}
	}
	if len(uses) == 0 {
		return 0, 0, "", nil
	}
	// What's decided already: every stored wording, and the clusters they can join.
	decided := map[string]bool{}
	var clusters []*cluster
	byKey := map[string]*cluster{}
	rows, err := a.DB.QueryContext(ctx, `SELECT phrase, cluster, display FROM phrase_clusters WHERE kind=? ORDER BY decided_at, phrase`, kind)
	if err != nil {
		return 0, 0, "", err
	}
	for rows.Next() {
		var p, c, d string
		if err := rows.Scan(&p, &c, &d); err != nil {
			rows.Close()
			return 0, 0, "", err
		}
		decided[p] = true
		root := c
		if root == "" {
			root = p
		}
		cl := byKey[root]
		if cl == nil {
			cl = &cluster{key: root, display: d}
			byKey[root] = cl
			clusters = append(clusters, cl)
		}
		cl.members++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, "", err
	}
	for _, cl := range clusters {
		if u := uses[cl.key]; u != nil {
			cl.example = u.context
		}
	}
	// Most answers first, shorter first at a tie: the established wording becomes the cluster and
	// the later wordings are asked about it.
	var order []*phraseUse
	for _, u := range uses {
		if !decided[u.key] {
			order = append(order, u)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].answers != order[j].answers {
			return order[i].answers > order[j].answers
		}
		if len(order[i].key) != len(order[j].key) {
			return len(order[i].key) < len(order[j].key)
		}
		return order[i].key < order[j].key
	})
	// A cluster's size orders the candidates: the wordings folded into it so far, and this run's
	// answers using its own wording.
	size := func(cl *cluster) int {
		n := cl.members
		if u := uses[cl.key]; u != nil {
			n += u.answers
		}
		return n
	}
	own := func(u *phraseUse, conf float64, source string) error {
		if _, err := a.DB.ExecContext(ctx, `INSERT OR REPLACE INTO phrase_clusters(kind, phrase, cluster, display, confidence, source, decided_at) VALUES (?,?,'',?,?,?,?)`,
			kind, u.key, u.display, conf, source, store.Now()); err != nil {
			return err
		}
		cl := &cluster{key: u.key, display: u.display, example: u.context, members: 1}
		byKey[u.key] = cl
		clusters = append(clusters, cl)
		return nil
	}
	asked, cost, model := 0, 0.0, ""
	for _, u := range order {
		if ctx.Err() != nil {
			return asked, cost, model, ctx.Err()
		}
		var cands []*cluster
		for _, cl := range clusters {
			if cl.key != u.key && plausiblePhrases(u.key, cl.key) {
				cands = append(cands, cl)
			}
		}
		if len(cands) == 0 {
			if err := own(u, 1, "rule"); err != nil {
				return asked, cost, model, err
			}
			continue
		}
		sort.SliceStable(cands, func(i, j int) bool { return size(cands[i]) > size(cands[j]) })
		if len(cands) > 8 {
			cands = cands[:8]
		}
		// One wording per request, with only its candidates in the state: Jev's probabilities
		// fall as the state fills with detail unrelated to the question (see fold.go).
		var candState []any
		criteria := map[string]any{}
		for i, cl := range cands {
			c := map[string]any{"phrase": cl.display}
			if cl.example != "" {
				c["example"] = cl.example
			}
			candState = append(candState, c)
			criteria[fmt.Sprintf("c%d", i)] = fmt.Sprintf("the same %s as `candidates[%d].phrase` (%s)", kind, i, cl.display)
		}
		criteria["none"] = fmt.Sprintf("a different %s from every candidate", kind)
		state := map[string]any{"category": category, "kind": kind, "phrase": u.display, "candidates": candState}
		if u.context != "" {
			state["context"] = u.context
		}
		reader := "a reader deciding whether to act on it"
		if kind == praiseKind {
			reader = "a reader weighing it"
		}
		questions := map[string]typesafe.Question{"q": {
			Type:         "choice",
			Instructions: fmt.Sprintf("`phrase` is one %s an AI assistant's answer raised about a product in `category`; `context` is the grader's sentence about that answer. Which of `candidates`, if any, says the same thing? Two %ss are the same when %s would treat them the same way: a narrower or more specific wording of the same point is the same. A different feature, or a different point about the same feature, is different.", kind, kind, reader),
			Criteria:     criteria,
		}}
		resp, err := client.Ask(ctx, "cluster-phrases/"+id, state, questions)
		if err != nil {
			return asked, cost, model, err
		}
		cost += resp.CostUSD
		model = resp.Model
		ans, ok := resp.Answers["q"]
		if !ok {
			continue
		}
		asked++
		p := typesafe.Chosen(ans)
		var chosen *cluster
		var ci int
		if _, err := fmt.Sscanf(ans.Choice, "c%d", &ci); err == nil && ci >= 0 && ci < len(cands) {
			chosen = cands[ci]
		}
		if chosen == nil {
			if err := own(u, p, "jev"); err != nil {
				return asked, cost, model, err
			}
			continue
		}
		if _, err := a.DB.ExecContext(ctx, `INSERT OR REPLACE INTO phrase_clusters(kind, phrase, cluster, display, confidence, source, decided_at) VALUES (?,?,?,?,?,'jev',?)`,
			kind, u.key, chosen.key, chosen.display, p, store.Now()); err != nil {
			return asked, cost, model, err
		}
		chosen.members++
	}
	return asked, cost, model, nil
}

// phraseStop are the words that carry no meaning between two wordings of one point, beyond the
// product-name stop list: complaints are built from these.
var phraseStop = map[string]bool{"and": true, "not": true, "lack": true, "lacks": true, "lacking": true, "limited": true, "only": true, "for": true, "with": true, "without": true, "its": true, "has": true, "have": true, "does": true, "doesn": true, "very": true, "some": true, "more": true, "less": true, "than": true, "about": true, "from": true, "into": true, "that": true, "this": true, "there": true, "are": true, "was": true, "but": true, "yet": true, "still": true, "may": true, "might": true, "could": true}

// phraseTokens are a wording's meaningful words: tokens, less the words complaints are built from.
func phraseTokens(s string) map[string]bool {
	out := tokens(s)
	for t := range out {
		if phraseStop[t] {
			delete(out, t)
		}
	}
	return out
}

// plausiblePhrases is the cheap pre-filter that decides which pairs are worth a question: the
// wordings share a meaningful word.
func plausiblePhrases(a, b string) bool {
	tb := phraseTokens(b)
	for t := range phraseTokens(a) {
		if tb[t] {
			return true
		}
	}
	return false
}

// phraseContext is the grader's sentence about the answer that bears on a folded phrase: the
// sentence of its reasoning sharing a word with the phrase, else its first sentence, else the
// evidence.
func phraseContext(g *grade.Grade, key string) string {
	want := phraseTokens(key)
	for _, s := range strings.FieldsFunc(g.Reasoning, func(r rune) bool { return r == '.' || r == '!' || r == '?' || r == '\n' }) {
		for t := range tokens(s) {
			if want[t] {
				return trimContext(s)
			}
		}
	}
	if s := firstSentence(g.Reasoning); s != "" {
		return trimContext(s)
	}
	return trimContext(g.Evidence)
}

func trimContext(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// PhraseDecision is one stored clustering decision, for the CLI.
type PhraseDecision struct {
	Kind       string  `json:"kind"`
	Phrase     string  `json:"phrase"`
	Cluster    string  `json:"cluster,omitempty"`
	Display    string  `json:"display"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
	DecidedAt  string  `json:"decided_at"`
	// Whether the decision folds the phrase into its cluster today, at the configured confidence.
	Folds bool `json:"folds"`
}

// PhraseDecisions lists the stored decisions, newest first.
func (a *App) PhraseDecisions(ctx context.Context) ([]PhraseDecision, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT kind, phrase, cluster, display, confidence, source, decided_at FROM phrase_clusters ORDER BY decided_at DESC, kind, phrase`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PhraseDecision
	for rows.Next() {
		var d PhraseDecision
		if err := rows.Scan(&d.Kind, &d.Phrase, &d.Cluster, &d.Display, &d.Confidence, &d.Source, &d.DecidedAt); err != nil {
			return nil, err
		}
		d.Folds = d.Cluster != "" && d.Confidence >= a.Cfg.MinConfidence()
		out = append(out, d)
	}
	return out, rows.Err()
}
