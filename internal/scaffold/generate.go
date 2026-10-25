package scaffold

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/suggest"
)

// File is one file of the config repo, its path relative to the repo's root.
type File struct {
	Path string
	Body string
}

// Starter is a prompt set init writes, before it becomes TOML.
type Starter struct {
	ID, Name, Description string
	Tier                  sets.Tier
	Note                  string // a TODO comment at the top of the file
	Prompts               []sets.Prompt
	Suggested             int // how many of Prompts, at the end, the analyst suggested
}

// Plan is everything init would write.
type Plan struct {
	Site        Site
	Category    string
	Guessed     bool // the category is a stand-in: no product noun in the description
	Competitors []string
	Sets        []Starter
	Suggested   int
	CostUSD     float64
}

// heads are nouns a description's category ends on: "an open-source observability platform".
var heads = map[string]bool{"platform": true, "tool": true, "software": true, "app": true, "application": true, "service": true, "solution": true, "framework": true, "library": true, "sdk": true, "api": true, "database": true, "engine": true, "cli": true, "editor": true, "ide": true, "plugin": true, "extension": true, "marketplace": true, "crm": true, "cms": true, "suite": true, "system": true, "toolkit": true, "assistant": true, "agent": true, "workspace": true, "manager": true, "tracker": true, "builder": true, "provider": true, "browser": true, "client": true, "server": true, "runtime": true, "language": true, "hosting": true, "infrastructure": true, "alternative": true, "analytics": true, "monitoring": true, "observability": true, "automation": true}

// leads end the walk back from a head noun at the start of a category: "is an", "the leading".
var leads = map[string]bool{"a": true, "an": true, "the": true, "is": true, "are": true, "our": true, "its": true, "best": true, "leading": true, "top": true, "ultimate": true, "modern": true, "powerful": true, "simple": true, "easy": true, "fast": true, "new": true, "first": true, "only": true, "world's": true, "#1": true, "free": true}

// objects end it at a word that makes the noun something the product does or uses rather than what
// it is: "build applications", "with AI agents". Such a phrase isn't the category.
var objects = map[string]bool{"and": true, "or": true, "of": true, "for": true, "to": true, "with": true, "that": true, "which": true, "it": true, "we": true, "in": true, "on": true, "by": true, "as": true, "from": true, "into": true, "using": true, "via": true, "build": true, "create": true, "make": true, "run": true, "ship": true, "use": true, "get": true, "start": true, "manage": true, "help": true, "helps": true, "lets": true, "power": true, "powers": true, "deploy": true, "automate": true, "your": true, "you": true, "all": true, "where": true, "who": true, "when": true, "so": true, "built": true, "designed": true, "made": true, "trusted": true, "loved": true, "used": true}

// Category is what the brand is, as buyers would search for it, from its description: a product
// noun ("platform", "tool", "app"...) and up to three words before it, the first one introduced
// as the brand ("is an", "the") or opening a clause. "tool" and false when there's none, or the
// phrase names the brand.
func Category(about string, names ...string) (string, bool) {
	words := strings.Fields(about)
	best, rank := "", 3
	for i, w := range words {
		bare := strings.TrimRight(w, ".,;:!?)")
		lw := strings.ToLower(bare)
		head := caseOf(words, i)
		if bare == w && i+1 < len(words) {
			if n := strings.ToLower(words[i+1]); !objects[n] && !leads[n] {
				continue // a modifier: "application" in "application monitoring software"
			}
		}
		switch {
		case heads[lw]:
		case strings.HasSuffix(lw, "s") && heads[lw[:len(lw)-1]]:
			head = head[:len(head)-1] // the category, singular
		default:
			continue
		}
		phrase := []string{head}
		r := 1 // stopped at punctuation or the length limit: likely an adjective list
		for j := i - 1; ; j-- {
			if j < 0 {
				r = 0 // the description opens with it
				break
			}
			p := words[j]
			lp := strings.ToLower(p)
			if len(phrase) == 4 {
				break
			}
			if leads[lp] {
				r = 0
				break
			}
			if objects[lp] {
				r = 2
				break
			}
			if strings.ContainsAny(p, ".,;:!?()\"–—") {
				break
			}
			phrase = append([]string{caseOf(words, j)}, phrase...)
		}
		if r < rank {
			best, rank = strings.Join(phrase, " "), r
		}
		if rank == 0 {
			break
		}
	}
	if rank == 2 || best == "" {
		return "tool", false
	}
	for _, n := range names {
		if sets.Names(best, n) {
			return "tool", false
		}
	}
	return best, true
}

// caseOf is words[i] as it reads mid-sentence: lowercase if it's capitalized only for opening a
// sentence, else as written (AI, Postgres, GitHub).
func caseOf(words []string, i int) string {
	w := strings.TrimRight(words[i], ".,;:!?)")
	opens := i == 0 || strings.ContainsAny(words[i-1][len(words[i-1])-1:], ".!?")
	if opens && (len(w) < 2 || w[1:] == strings.ToLower(w[1:])) {
		return strings.ToLower(w)
	}
	return w
}

// article is "a" or "an" for a phrase.
func article(s string) string {
	if s != "" && strings.ContainsRune("aeiou", rune(strings.ToLower(s)[0])) {
		return "an"
	}
	return "a"
}

// Build is the starter sets for a site, before any suggestions.
func Build(s Site, competitors []string) Plan {
	names := append([]string{s.Name}, s.Aliases...)
	cat, ok := Category(s.About, names...)
	p := Plan{Site: s, Category: cat, Guessed: !ok, Competitors: competitors}
	a := article(cat)
	brand := slug(s.Name)
	pr := func(id, text string, info bool) sets.Prompt {
		x := sets.Prompt{ID: id, Text: text}
		if info {
			x.Intent = sets.Info
		}
		return x
	}
	todoCat := ""
	if p.Guessed {
		todoCat = fmt.Sprintf(" No product noun in the description, so these say %q: replace it with what buyers call this kind of product.", cat)
	}

	p.Sets = append(p.Sets, Starter{
		ID: "discovery", Name: "Discovery", Tier: sets.Core,
		Description: fmt.Sprintf("Buyers looking for %s %s who haven't settled on one: what's best, for whom.", a, cat),
		Note:        "TODO: these say \"" + cat + "\", guessed from the brand's description. Use the words your buyers use, and say who they are (\"for a small team\" is a stand-in)." + todoCat,
		Prompts: []sets.Prompt{
			pr("best-for-small-team", fmt.Sprintf("What's the best %s for a small team?", cat), false),
			pr("best-for-startup", fmt.Sprintf("What's the best %s for a startup?", cat), false),
			pr("best-for-large-company", fmt.Sprintf("What's the best %s for a large company?", cat), false),
			pr("most-recommended", fmt.Sprintf("Which %s do people recommend right now, and why?", cat), false),
			pr("how-to-choose", fmt.Sprintf("How do I choose %s %s?", a, cat), true),
		},
	})
	p.Sets = append(p.Sets, Starter{
		ID: "use-cases", Name: "Use cases", Tier: sets.PLG,
		Description: fmt.Sprintf("Buyers with a particular job to do, asking which %s fits it.", cat),
		Note:        "TODO: replace these with the jobs your buyers hire the product for, in their words: \"What " + cat + " should I use to <job>?\"",
		Prompts: []sets.Prompt{
			pr("easiest-to-set-up", fmt.Sprintf("Which %s is easiest to set up and maintain?", cat), false),
			pr("works-with-existing-tools", fmt.Sprintf("Which %s works well with the tools a team already uses?", cat), false),
			pr("for-growing-team", fmt.Sprintf("What's a good %s for a team that's growing fast?", cat), false),
			pr("what-to-look-for", fmt.Sprintf("What should I look for in %s %s?", a, cat), true),
		},
	})
	alt := Starter{
		ID: "alternatives", Name: "Alternatives", Tier: sets.PLG,
		Description: fmt.Sprintf("Buyers using or weighing another %s, looking for something else.", cat),
		Note:        "TODO: one prompt per competitor buyers move away from. A closed comparison between competitors is research about that shortlist, not open discovery for the brand; group it separately when interpreting results. Comparisons with the brand go in head-to-heads.",
	}
	for i, c := range competitors {
		if i == 5 {
			break
		}
		alt.Prompts = append(alt.Prompts, pr("alternatives-to-"+slug(c), fmt.Sprintf("What are good alternatives to %s?", c), false))
	}
	if len(competitors) >= 2 {
		alt.Prompts = append(alt.Prompts, pr(slug(competitors[0])+"-vs-"+slug(competitors[1]), fmt.Sprintf("%s vs %s: which is better?", competitors[0], competitors[1]), false))
	}
	alt.Prompts = append(alt.Prompts,
		pr("alternatives-to-market-leader", fmt.Sprintf("What are the alternatives to the market leader in %s?", plural(cat)), false),
		pr("simpler-than-big-names", fmt.Sprintf("Is there %s %s that's less complicated than the big names?", a, cat), false),
	)
	p.Sets = append(p.Sets, alt)
	p.Sets = append(p.Sets, Starter{
		ID: "pricing", Name: "Pricing", Tier: sets.PLG,
		Description: fmt.Sprintf("Buyers weighing what %s %s costs and what they get for it.", a, cat),
		Prompts: []sets.Prompt{
			pr("typical-cost", fmt.Sprintf("How much does %s %s typically cost?", a, cat), true),
			pr("most-affordable", fmt.Sprintf("What's the most affordable %s that's still good?", cat), false),
			pr("free-good-enough", fmt.Sprintf("Is there a free %s that's good enough for a small team?", cat), false),
			pr("best-value", fmt.Sprintf("Which %s is the best value for money?", cat), false),
		},
	})
	h2h := Starter{
		ID: sets.HeadToHeads, Name: s.Name + " head to heads", Tier: sets.PLG,
		Description: fmt.Sprintf("The only set that names the brand: buyers who already know %s and are comparing it with the alternatives, or asking whether it fits.", s.Name),
	}
	for i, c := range competitors {
		if i == 5 {
			break
		}
		h2h.Prompts = append(h2h.Prompts, pr(brand+"-vs-"+slug(c), fmt.Sprintf("%s vs %s: which should I pick?", s.Name, c), false))
	}
	if len(competitors) == 0 {
		h2h.Note = "TODO: add a comparison with each competitor, e.g.\n# [[prompts]]\n# id = \"brand-vs-competitor\"\n# text = \"" + s.Name + " vs <competitor>: which should I pick?\""
	}
	h2h.Prompts = append(h2h.Prompts,
		pr("is-"+brand+"-good", fmt.Sprintf("Is %s a good %s?", s.Name, cat), false),
		pr(brand+"-downsides", fmt.Sprintf("What are the downsides of %s?", s.Name), true),
		pr("is-"+brand+"-worth-paying-for", fmt.Sprintf("Is %s worth paying for?", s.Name), false),
	)
	p.Sets = append(p.Sets, h2h)

	taken := map[string]bool{}
	for i := range p.Sets {
		for j := range p.Sets[i].Prompts {
			x := &p.Sets[i].Prompts[j]
			x.ID = unique(x.ID, taken)
		}
	}
	return p
}

func slug(name string) string { return config.Brand{Name: name}.Slug() }

// unique is id, or id-2, id-3... when it's taken; it takes it.
func unique(id string, taken map[string]bool) string {
	out := id
	for n := 2; taken[out]; n++ {
		out = fmt.Sprintf("%s-%d", id, n)
	}
	taken[out] = true
	return out
}

// plural is a rough plural, for one phrase: "platforms", "software".
func plural(s string) string {
	switch {
	case strings.HasSuffix(s, "software"), strings.HasSuffix(s, "infrastructure"), strings.HasSuffix(s, "hosting"), strings.HasSuffix(s, "s"):
		return s
	case strings.HasSuffix(s, "y") && !strings.HasSuffix(s, "ey"):
		return s[:len(s)-1] + "ies"
	}
	return s + "s"
}

// stageSet is the starter set each suggest stage goes to.
var stageSet = map[string]string{"awareness": "discovery", "reputation": "discovery", "comparison": "alternatives", "alternatives": "alternatives", "pricing": "pricing", "implementation": "use-cases"}

// suggestPrompts is suggest.Prompts; a test swaps it for a fake.
var suggestPrompts = suggest.Prompts

// Suggest asks the analyst once for n more prompts across the starter sets (not head-to-heads),
// and adds each to the set for its stage. cfg is the config being written.
func (p *Plan) Suggest(ctx context.Context, cfg *config.Config, n int) error {
	var all []sets.Set
	combined := sets.Set{ID: "starter", Name: p.Site.Name + " starter prompts",
		Description: fmt.Sprintf("Every question a buyer asks while choosing %s %s: which to pick, for what job, instead of what, at what cost.", article(p.Category), p.Category)}
	for _, s := range p.Sets {
		all = append(all, sets.Set{ID: s.ID, Prompts: s.Prompts})
		if s.ID != sets.HeadToHeads {
			combined.Prompts = append(combined.Prompts, s.Prompts...)
		}
	}
	for _, c := range p.Competitors {
		combined.Competitors = append(combined.Competitors, sets.Competitor{Name: c})
	}
	res, err := suggestPrompts(ctx, cfg, combined, all, n)
	p.CostUSD = res.CostUSD
	if err != nil {
		return err
	}
	taken := map[string]bool{}
	for _, s := range p.Sets {
		for _, x := range s.Prompts {
			taken[x.ID] = true
		}
	}
	for _, x := range res.Prompts {
		id := stageSet[x.Stage]
		if id == "" {
			id = "discovery"
		}
		for i := range p.Sets {
			if p.Sets[i].ID == id {
				sp := sets.Prompt{ID: sets.PromptID(x.Text, taken), Text: x.Text}
				if x.Intent == sets.Info {
					sp.Intent = sets.Info
				}
				p.Sets[i].Prompts = append(p.Sets[i].Prompts, sp)
				p.Sets[i].Suggested++
				p.Suggested++
			}
		}
	}
	return nil
}

// gitignore keeps secrets out of the config repo: cheapseats.toml says keys may live in a .env
// beside it, and the next step commits the directory.
const gitignore = `# Secrets: API keys and tokens never go in the config repo.
.env
.env.*
!.env.example
`

// Files is the config repo: a .gitignore, cheapseats.toml and one file per starter set.
func (p Plan) Files() []File {
	out := []File{{Path: ".gitignore", Body: gitignore}, {Path: "cheapseats.toml", Body: p.config()}}
	for _, s := range p.Sets {
		out = append(out, File{Path: filepath.Join("prompt-sets", s.ID+".toml"), Body: s.toml()})
	}
	return out
}

func (s Starter) toml() string {
	var b strings.Builder
	fmt.Fprintf(&b, "id = %s\nname = %s\ntier = %s\ndescription = %s\n", q(s.ID), q(s.Name), q(string(s.Tier)), q(s.Description))
	b.WriteString("\n# Every prompt's id is permanent: results and trends key on it. Reword freely (the id stays);\n# a different question gets a new id.\n")
	if s.Note != "" {
		b.WriteString("#\n")
		for _, line := range strings.Split(s.Note, "\n") {
			b.WriteString(comment(strings.TrimPrefix(line, "# ")))
		}
	}
	for i, x := range s.Prompts {
		if s.Suggested > 0 && i == len(s.Prompts)-s.Suggested {
			b.WriteString("\n# Suggested by the analyst (cheapseats init -suggest). Check each is a question your buyers ask.\n")
		}
		fmt.Fprintf(&b, "\n[[prompts]]\nid = %s\ntext = %s\n", q(x.ID), q(x.Text))
		if x.Intent != "" {
			fmt.Fprintf(&b, "intent = %s\n", q(x.Intent))
		}
	}
	return b.String()
}

// comment is text as # comment lines of at most 100 characters.
func comment(text string) string {
	var b strings.Builder
	line := "#"
	for _, w := range strings.Fields(Clean(text)) {
		if len(line)+1+len(w) > 100 && line != "#" {
			b.WriteString(line + "\n")
			line = "#"
		}
		line += " " + w
	}
	b.WriteString(line + "\n")
	return b.String()
}

// q is a TOML basic string.
func q(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func (p Plan) config() string {
	s := p.Site
	var b strings.Builder
	fmt.Fprintf(&b, `# cheapseats configuration for %s, started by cheapseats init from %s.
# Review every TODO, then: cheapseats check. Secrets (API keys, tokens) come from the environment or
# .env, never from here. Prompt sets live in prompt-sets/ beside this file.
#
# Every model is called through the local claude CLI on your Claude subscription: no API key and
# no API spend. To run in a container, switch the providers to anthropic (and openai) and add
# [pricing]; see examples/cheapseats-config/cheapseats.toml in the cheapseats repo.

[brand]
name = %s
aliases = [`, Clean(s.Name), Clean(s.URL), q(s.Name))
	for i, a := range s.Aliases {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(q(a))
	}
	b.WriteString("]\n")
	b.WriteString("# What the product is, so the grader can tell it apart from other things with the same name.\n")
	if s.About == "" {
		b.WriteString("# TODO: the site has no description; say what it is in a sentence or two.\n")
	} else {
		b.WriteString("# TODO: check this, from the home page's description, is accurate and plain.\n")
	}
	fmt.Fprintf(&b, "about = %s\n", q(s.About))
	b.WriteString(`# The market, as a noun phrase: what the prompts are about, where about says what this one
# product is. The prompt review's off-target check is asked against it ([typesafe]).
# TODO: the category: e.g. AI search visibility tools
# category = ""
# Who asks the questions, as a noun phrase with its article. The answerers are told they're
# answering this person's question. Changing about or audience restarts every trend, so settle them
# before the first run.
# TODO: who asks: e.g. a product marketing lead
# audience = "a product marketing lead"
# Public claims the analyst may build fixes on. Everything here is sent to the model.
# TODO: add what the brand says publicly today.
facts = []

# The brand's websites, found on the home page, llms.txt and sitemap. Pages on them count as its
# own; the fix list puts each fix on the site that suits it. A host covers its subdomains unless
# they're listed themselves; a host/path covers that prefix. kind: marketing | docs | blog |
# community | code | other.
# TODO: add any that were missed (a community forum, a docs site on another domain).
`)
	for _, x := range s.Sites {
		fmt.Fprintf(&b, "[[brand.sites]]\nurl = %s\nkind = %s\n# owner = \"\"\n\n", q(x.URL), q(x.Kind))
	}
	b.WriteString(`# Every assistant that answers. Results key on id: never reuse one for a different model.
# claude-code runs the local claude CLI on its logged-in subscription.
[[answerers]]
id = "claude-code-opus"
name = "Claude Opus"
provider = "claude-code"
model = "claude-opus-5-5"
samples = { search = 2, recall = 1 }

# Optional: ChatGPT's answers through the local codex CLI on a ChatGPT subscription. A run skips it
# when codex isn't installed.
# [[answerers]]
# id = "codex-sol"
# name = "ChatGPT Sol"
# provider = "codex"
# model = "gpt-6.1-sol"
# samples = { search = 2, recall = 1 }

# Competitors the grader folds onto one name in every prompt set, for share of voice. domains are
# their own sites. Adding one never restarts a trend.
`)
	if len(p.Competitors) == 0 {
		b.WriteString("# TODO: list the products your buyers compare you with.\n# [[competitors]]\n# name = \"Example Co\"\n# aliases = [\"example.com\"]\n# domains = [\"example.com\"]\n\n")
	} else {
		b.WriteString("# TODO: add each competitor's aliases and domains.\n")
		for _, c := range p.Competitors {
			fmt.Fprintf(&b, "[[competitors]]\nname = %s\n# aliases = []\n# domains = []\n\n", q(c))
		}
	}
	b.WriteString(`# The grader and the analyst on the claude CLI too. Switching provider is a new method for every
# trend.
[grader]
model = "claude-haiku-5-5"
provider = "claude-code"

[analyst]
model = "claude-opus-5-5"
provider = "claude-code"

[budget]
# Admission budget across API calls; subscription calls cost nothing here.
daily_usd = 10.0
reservation_usd = 0.60
concurrency = 4

[schedule]
# UTC hour for the daily runs. Off until you've reviewed a manual run.
# daily_hour_utc = 13

# How often the schedule runs each tier, in days: core sets every other day, the rest weekly.
[tiers.core]
every_days = 2

[tiers.plg]
every_days = 7
max_samples = 1

[owners]
# Who picks up each kind of fix. Leave blank until you name owners.
default = ""
`)
	return b.String()
}

// Check writes the files to a temporary directory and loads them as cheapseats check would: the
// config, every set, and the rule that only head-to-heads names the brand.
func Check(files []File) (*config.Config, error) {
	dir, err := os.MkdirTemp("", "cheapseats-init-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := Write(dir, files); err != nil {
		return nil, err
	}
	cfg, err := config.Load(filepath.Join(dir, "cheapseats.toml"))
	if err != nil {
		return nil, err
	}
	if err := sets.Check(filepath.Join(dir, "prompt-sets"), append([]string{cfg.Brand.Name}, cfg.Brand.Aliases...)); err != nil {
		return nil, err
	}
	cfg.Dir, cfg.Rev = "", ""
	return cfg, nil
}

// Write puts the files under dir, refusing to overwrite any that exist: nothing is written if one
// does.
func Write(dir string, files []File) error {
	var exist []string
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, f.Path)); err == nil {
			exist = append(exist, filepath.Join(dir, f.Path))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if len(exist) > 0 {
		return fmt.Errorf("refusing to overwrite %s", strings.Join(exist, ", "))
	}
	for _, f := range files {
		path := filepath.Join(dir, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		w, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = w.WriteString(f.Body)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
