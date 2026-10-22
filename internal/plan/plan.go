// Package plan says what a config change will do before it's merged: which trends it starts
// over, what each day's rankings will note as changed, and how many answers a day it asks for.
// It reads two checkouts of a config repo and needs neither a database nor an API key, so it
// runs in a pull request's CI.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/run"
	"github.com/metcalfc/popcorn/internal/sets"
)

type Config struct {
	Cfg  *config.Config
	Sets []sets.Set
}

// Markdown compares before with after and writes the plan for a pull request comment.
func Markdown(before, after Config) string {
	var b strings.Builder
	b.WriteString("## Popcorn plan\n\n")
	var warn []string

	// How answers are asked and graded: a change starts every trend over.
	if m := methodChanges(before.Cfg, after.Cfg); len(m) > 0 {
		warn = append(warn, "**Every set's trend starts over**, because "+strings.Join(m, ", ")+" changed. Runs after this are compared only with each other.")
	}

	old := map[string]sets.Set{}
	for _, s := range before.Sets {
		old[s.ID] = s
	}
	oldPrompt := map[string]sets.Prompt{} // across sets: a prompt can move
	oldSetOf := map[string]string{}
	for _, s := range before.Sets {
		for _, p := range s.Prompts {
			oldPrompt[p.ID], oldSetOf[p.ID] = p, s.ID
		}
	}
	newPrompt := map[string]bool{}
	for _, s := range after.Sets {
		for _, p := range s.Prompts {
			newPrompt[p.ID] = true
		}
	}
	removedBySHA := map[string]string{}
	for id, p := range oldPrompt {
		if !newPrompt[id] {
			removedBySHA[p.SHA()] = id
		}
	}

	var sections []string
	for _, s := range after.Sets {
		var lines []string
		prev, existed := old[s.ID]
		if !existed {
			lines = append(lines, fmt.Sprintf("- New set, %d prompts.", len(s.Prompts)))
		}
		var added, reworded, toInfo, fromInfo []string
		for _, p := range s.Prompts {
			if was, ok := oldPrompt[p.ID]; ok && was.Info() != p.Info() {
				if p.Info() {
					toInfo = append(toInfo, p.ID)
				} else {
					fromInfo = append(fromInfo, p.ID)
				}
			}
			was, ok := oldPrompt[p.ID]
			switch {
			case !ok:
				added = append(added, p.ID)
				if id, renamed := removedBySHA[p.SHA()]; renamed {
					warn = append(warn, fmt.Sprintf("`%s` asks exactly what removed prompt `%s` asked. A new id starts its history over; keep `%s` to carry it on.", p.ID, id, id))
				}
			case was.SHA() != p.SHA():
				reworded = append(reworded, p.ID)
				if overlap(was.Text, p.Text) < 0.5 {
					warn = append(warn, fmt.Sprintf("`%s` is reworded so much it may be a different question. If it asks something new, give it a new id; a reworded prompt carries its history on.", p.ID))
				}
			}
			if ok && oldSetOf[p.ID] != s.ID {
				lines = append(lines, fmt.Sprintf("- `%s` moved here from `%s`; its history comes with it.", p.ID, oldSetOf[p.ID]))
			}
		}
		if existed {
			if len(added) > 0 {
				lines = append(lines, "- Added: "+codes(added))
			}
			var removed []string
			for _, p := range prev.Prompts {
				if !newPrompt[p.ID] {
					removed = append(removed, p.ID)
				}
			}
			if len(removed) > 0 {
				lines = append(lines, "- Removed: "+codes(removed)+". Their history stays on the set page; the ids are retired.")
			}
			if len(reworded) > 0 {
				lines = append(lines, "- Reworded: "+codes(reworded)+". Their history carries on, marked.")
			}
			if len(toInfo) > 0 {
				lines = append(lines, "- Now info prompts: "+codes(toInfo)+". From the next run they're scored on being named or cited only, and leave the recommended and top-pick rates; the day it changes is marked.")
			}
			if len(fromInfo) > 0 {
				lines = append(lines, "- No longer info prompts: "+codes(fromInfo)+". From the next run they count toward recommended and top pick again; the day it changes is marked.")
			}
			if a, r := diffNames(competitors(s), competitors(prev)); len(a)+len(r) > 0 {
				lines = append(lines, "- Competitors: "+plusMinus(a, r))
			}
			if s.Tier != prev.Tier {
				lines = append(lines, fmt.Sprintf("- Tier %s → %s: changes how often it runs and how many samples it takes.", prev.Tier, s.Tier))
			}
		}
		if len(lines) > 0 {
			sections = append(sections, fmt.Sprintf("### %s (`%s`)\n%s", s.Name, s.ID, strings.Join(lines, "\n")))
		}
	}
	for _, s := range before.Sets {
		if !contains(after.Sets, s.ID) {
			sections = append(sections, fmt.Sprintf("### `%s` removed\n- It stops running. Its runs and history stay in the database.", s.ID))
		}
	}

	// Answerers: changes are noted on every set's trend.
	var ans []string
	oldAns := map[string]config.Answerer{}
	for _, a := range before.Cfg.Answerers {
		oldAns[a.ID] = a
	}
	for _, a := range after.Cfg.Answerers {
		was, ok := oldAns[a.ID]
		switch {
		case !ok:
			ans = append(ans, fmt.Sprintf("- New answerer `%s` (%s %s).", a.ID, a.Provider, a.Model))
		case was.Model != a.Model:
			ans = append(ans, fmt.Sprintf("- `%s`: model %s → %s. Expect a step in every trend.", a.ID, was.Model, a.Model))
		case was.Samples != a.Samples:
			ans = append(ans, fmt.Sprintf("- `%s`: samples %d search / %d recall → %d / %d.", a.ID, was.Samples.Search, was.Samples.Recall, a.Samples.Search, a.Samples.Recall))
		}
		if ok && was.Provider != a.Provider {
			ans = append(ans, fmt.Sprintf("- `%s`: provider %s → %s.", a.ID, was.Provider, a.Provider))
		}
	}
	for _, a := range before.Cfg.Answerers {
		if !hasAnswerer(after.Cfg, a.ID) {
			ans = append(ans, fmt.Sprintf("- Answerer `%s` removed.", a.ID))
		}
	}

	if x, y := siteList(before.Cfg.Brand), siteList(after.Cfg.Brand); x != y {
		ans = append(ans, "- The brand's sites changed: which pages count as its own, and where fixes go. Trends carry on.")
	}

	if len(warn) > 0 {
		b.WriteString("**Check before merging**\n\n")
		for _, w := range warn {
			b.WriteString("- " + w + "\n")
		}
		b.WriteString("\n")
	}
	if len(sections)+len(ans) == 0 && len(warn) == 0 {
		b.WriteString("No change to what Popcorn asks or how it's measured.\n\n")
	}
	for _, s := range sections {
		b.WriteString(s + "\n\n")
	}
	if len(ans) > 0 {
		b.WriteString("### Answerers and sites\n" + strings.Join(ans, "\n") + "\n\n")
	}
	b.WriteString("Trends carry on through prompt, competitor and answerer changes; each day notes what changed. Editing a set's name or description changes nothing.\n\n")
	b.WriteString(volume(after))
	return b.String()
}

// volume is how many answers (and as many grading calls) a run of each set asks for.
func volume(c Config) string {
	var b strings.Builder
	b.WriteString("| set | tier | prompts | answers a run |\n|---|---|---|---|\n")
	total := 0
	for _, s := range c.Sets {
		n := 0
		for _, a := range c.Cfg.Answerers {
			for _, k := range []int{a.Samples.Search, a.Samples.Recall} {
				if s.Tier == sets.PLG && k > c.Cfg.Tiers.PLG.MaxSamples {
					k = c.Cfg.Tiers.PLG.MaxSamples
				}
				n += k * len(s.Prompts)
			}
		}
		if s.Tier == sets.Core {
			total += n
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d |\n", s.ID, s.Tier, len(s.Prompts), n)
	}
	every := "daily"
	if n := c.Cfg.Tiers.Core.EveryDays; n > 1 {
		every = fmt.Sprintf("every %d days", n)
	}
	fmt.Fprintf(&b, "\nCore sets run %s: %d answers each time, each graded once. Subscription answerers cost nothing per answer; API ones are billed per token against `budget.daily_usd`.\n", every, total)
	return b.String()
}

// methodChanges names what differs in how answers are asked and graded (see run.Method).
func methodChanges(before, after *config.Config) []string {
	x, y := run.NewSnapshot(before, sets.Set{}), run.NewSnapshot(after, sets.Set{})
	if run.Method(x) == run.Method(y) {
		return nil
	}
	var out []string
	bb, ab := before.Brand, after.Brand
	if bb.Name != ab.Name {
		out = append(out, "the brand name")
	}
	if strings.Join(bb.Aliases, "\x00") != strings.Join(ab.Aliases, "\x00") {
		out = append(out, "the brand's aliases")
	}
	if bb.About != ab.About {
		out = append(out, "`brand.about`")
	}
	if bb.Audience != ab.Audience {
		out = append(out, "`brand.audience`")
	}
	if before.Grader.Model != after.Grader.Model {
		out = append(out, "the grader model")
	}
	if before.Grader.Via() != after.Grader.Via() {
		out = append(out, "how the grader is called (`[grader] provider`)")
	}
	if len(out) == 0 {
		out = append(out, "how answers are asked or graded")
	}
	return out
}

func siteList(b config.Brand) string {
	var parts []string
	for _, s := range b.AllSites() {
		parts = append(parts, s.URL+" "+s.Kind+" "+s.Owner)
	}
	return strings.Join(parts, "\n")
}

// overlap is the share of words two texts have in common, to spot a reword that's really a new
// question.
func overlap(a, b string) float64 {
	words := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, w := range strings.Fields(strings.ToLower(s)) {
			m[strings.Trim(w, ".,?!:;\"'()")] = true
		}
		return m
	}
	x, y := words(a), words(b)
	inter, union := 0, len(y)
	for w := range x {
		if y[w] {
			inter++
		} else {
			union++
		}
	}
	if union == 0 {
		return 1
	}
	return float64(inter) / float64(union)
}

func competitors(s sets.Set) []string {
	var out []string
	for _, c := range s.Competitors {
		out = append(out, c.Name)
	}
	return out
}

func diffNames(now, before []string) (added, removed []string) {
	in := map[string]bool{}
	for _, x := range before {
		in[x] = true
	}
	for _, x := range now {
		if !in[x] {
			added = append(added, x)
		}
		delete(in, x)
	}
	for x := range in {
		removed = append(removed, x)
	}
	sort.Strings(added)
	sort.Strings(removed)
	return
}

func plusMinus(added, removed []string) string {
	var parts []string
	for _, a := range added {
		parts = append(parts, "+"+a)
	}
	for _, r := range removed {
		parts = append(parts, "−"+r)
	}
	return strings.Join(parts, ", ")
}

func codes(ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = "`" + id + "`"
	}
	return strings.Join(out, ", ")
}

func contains(all []sets.Set, id string) bool {
	for _, s := range all {
		if s.ID == id {
			return true
		}
	}
	return false
}

func hasAnswerer(c *config.Config, id string) bool {
	for _, a := range c.Answerers {
		if a.ID == id {
			return true
		}
	}
	return false
}
