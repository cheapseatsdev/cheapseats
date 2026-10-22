// Package config reads popcorn.toml: the brand, who answers, who grades, what it costs, and the
// limits. Secrets never live here; they come from the environment.
package config

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/metcalfc/popcorn/internal/sets"
)

type Config struct {
	Brand     Brand              `toml:"brand"`
	Answerers []Answerer         `toml:"answerers"`
	Grader    Model              `toml:"grader"`
	Analyst   Model              `toml:"analyst"`
	Budget    Budget             `toml:"budget"`
	Sampling  Sampling           `toml:"sampling"`
	Schedule  Schedule           `toml:"schedule"`
	Tiers     Tiers              `toml:"tiers"`
	Owners    Owners             `toml:"owners"`
	Pricing   map[string]Pricing `toml:"pricing"`
	// Competitors every prompt set folds names onto, so a list shared by all sets is written
	// once. A set's own [[competitors]] entry for the same name wins.
	Competitors []sets.Competitor `toml:"competitors"`
	// Overrides for the classes the run page sorts sources into, as class -> hosts or host/path
	// prefixes: [sources] ugc = ["community.example.com"]. They win over the built-in lists; the
	// brand's sites are always "own" and competitors' domains always "competitor".
	Sources map[string][]string `toml:"sources"`
	// The directory popcorn.toml was read from: prompt sets live beside it.
	Dir string `toml:"-"`
	// The config repo's commit, if known (see Revision).
	Rev string `toml:"-"`
}

type Brand struct {
	Name string `toml:"name"`
	// Other names the brand goes by in answers (the CLI, the domain).
	Aliases []string `toml:"aliases"`
	// Hosts, or host/path prefixes, whose pages count as first-party. Sites says the same with
	// what each one is for; both can be used, and a domain counts as a site of kind "other".
	Domains []string `toml:"domains"`
	// The brand's websites: the marketing site, docs, blog and so on. The fix list puts each fix
	// on the site that suits it and sends it to that site's owner; the run page says which of
	// them the answers drew on.
	Sites []Site `toml:"sites" json:",omitempty"`
	// What the product is, in a sentence or two. The grader uses it to tell the brand apart from
	// other things with the same name.
	About string `toml:"about"`
	// Public claims the analyst may build fixes on. Everything here is sent to the model, so list
	// only what the brand says publicly today.
	Facts []string `toml:"facts"`
	// Who asks the questions, as a noun phrase with its article ("a software developer", "an
	// HR lead"). The answerers are told they're answering this person's question; empty means
	// anyone's.
	Audience string `toml:"audience" json:",omitempty"`
}

type Site struct {
	// A host (example.com, which also covers its subdomains unless they're listed themselves) or
	// a host/path prefix (github.com/example).
	URL string `toml:"url" json:"url"`
	// What it is: marketing | docs | blog | community | code | other.
	Kind string `toml:"kind" json:"kind"`
	// Who acts on fixes for pages on it; empty falls back to [owners].
	Owner string `toml:"owner" json:"owner,omitempty"`
}

var SiteKinds = []string{"marketing", "docs", "blog", "community", "code", "other"}

// SourceClasses are the classes [sources] may put a host in.
var SourceClasses = []string{"ugc", "review", "reference", "institutional", "editorial"}

type Answerer struct {
	// Permanent: results and rankings key on it. A new model gets a new id.
	ID string `toml:"id"`
	// What the UI calls it ("Claude Opus"); the id when empty. Free to change.
	Name string `toml:"name" json:",omitempty"`
	// anthropic | openai: the API, billed per token.
	// claude-code | codex: the local CLI, on its logged-in Claude or ChatGPT subscription.
	Provider string  `toml:"provider"`
	Model    string  `toml:"model"`
	Samples  Samples `toml:"samples"`
}

type Samples struct {
	Search int `toml:"search"`
	Recall int `toml:"recall"`
}

// Model is the grader or the analyst: a Claude model, through the API (anthropic, the default) or
// the local claude CLI on a subscription (claude-code), so a laptop setup needs no API key.
type Model struct {
	Model    string `toml:"model"`
	Provider string `toml:"provider"`
	// Grader only: after a run is graded, one more call groups its objections, praise and
	// reasons for losing into a few themes. Unset means on.
	Themes *bool `toml:"themes"`
}

// ThemesOn reports whether runs end with the theme pass ([grader] themes, on unless false).
func (c *Config) ThemesOn() bool { return c.Grader.Themes == nil || *c.Grader.Themes }

// Sampling is adaptive sampling: before a run, extra answers go to the cells (prompt, answerer,
// track) whose recent named rate is least certain. Off unless adaptive is true.
type Sampling struct {
	Adaptive bool `toml:"adaptive"`
	// The most extra answers one run may add, across every cell.
	MaxExtra int `toml:"max_extra"`
}

// Via is the provider the model is called through: anthropic unless it's claude-code.
func (m Model) Via() string {
	if m.Provider == ClaudeCode {
		return ClaudeCode
	}
	return Anthropic
}

// Ref names the model and how it's called, for a run's snapshot: the model alone through the API,
// as runs always recorded it, and "claude-code:<model>" through the CLI. ModelFromRef reads it back.
func (m Model) Ref() string {
	if m.Via() == ClaudeCode {
		return ClaudeCode + ":" + m.Model
	}
	return m.Model
}

func ModelFromRef(ref string) Model {
	if p, model, ok := strings.Cut(ref, ":"); ok && p == ClaudeCode {
		return Model{Model: model, Provider: ClaudeCode}
	}
	return Model{Model: ref}
}

type Budget struct {
	DailyUSD       float64 `toml:"daily_usd"`
	Concurrency    int     `toml:"concurrency"`
	ReservationUSD float64 `toml:"reservation_usd"`
}

type Schedule struct {
	// UTC hour for the daily runs; nil means no schedule (the default, so dev doesn't spend).
	DailyHourUTC *int `toml:"daily_hour_utc"`
}

type Tiers struct {
	// Core sets run every every_days days at schedule.daily_hour_utc (default 1: daily).
	Core struct {
		EveryDays int `toml:"every_days"`
	} `toml:"core"`
	PLG struct {
		EveryDays  int `toml:"every_days"`
		MaxSamples int `toml:"max_samples"`
	} `toml:"plg"`
}

type Owners struct {
	Default string            `toml:"default"`
	ByGap   map[string]string `toml:"by_gap"`
}

// Pricing is USD per million tokens, and per 1,000 searches.
type Pricing struct {
	Input       float64 `toml:"input"`
	Output      float64 `toml:"output"`
	CacheRead   float64 `toml:"cache_read"`
	CacheWrite  float64 `toml:"cache_write"`
	SearchPer1K float64 `toml:"search_per_1k"`
}

// Usage is what one model call consumed.
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Searches   int64 `json:"searches"`
}

func (u *Usage) Add(o Usage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
	u.Searches += o.Searches
}

// Cost prices usage for a model; an unpriced model costs zero and is logged once at load.
func (c *Config) Cost(model string, u Usage) float64 {
	p, ok := c.Pricing[model]
	if !ok {
		return 0
	}
	m := 1e6
	return float64(u.Input)*p.Input/m + float64(u.Output)*p.Output/m +
		float64(u.CacheRead)*p.CacheRead/m + float64(u.CacheWrite)*p.CacheWrite/m +
		float64(u.Searches)*p.SearchPer1K/1000
}

func Load(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("reading %s (set POPCORN_CONFIG to your config repo's popcorn.toml): %w", path, err)
	}
	// A misspelled or misplaced key would otherwise be dropped without a word (a schedule hour
	// in the wrong table just turns the schedule off).
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("%s: unknown keys: %v", path, u)
	}
	c.Dir = filepath.Dir(path)
	c.Rev = Revision(c.Dir)
	if c.Brand.Name == "" {
		return nil, fmt.Errorf("%s: brand.name is required", path)
	}
	for i, s := range c.Brand.Sites {
		if cleanSite(s.URL) == "" {
			return nil, fmt.Errorf("%s: brand.sites[%d]: url is required", path, i)
		}
		if !slices.Contains(SiteKinds, s.Kind) {
			return nil, fmt.Errorf("%s: brand.sites[%d] (%s): kind must be one of %s", path, i, s.URL, strings.Join(SiteKinds, ", "))
		}
	}
	names := map[string]bool{strings.ToLower(c.Brand.Name): true}
	for _, a := range c.Brand.Aliases {
		names[strings.ToLower(a)] = true
	}
	for i, comp := range c.Competitors {
		n := strings.ToLower(strings.TrimSpace(comp.Name))
		if n == "" {
			return nil, fmt.Errorf("%s: competitors[%d]: name is required", path, i)
		}
		if names[n] {
			return nil, fmt.Errorf("%s: competitor %q is listed twice or is the brand", path, comp.Name)
		}
		names[n] = true
		if err := comp.CheckDomains(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, d := range comp.Domains {
			if c.Brand.FirstParty(d) {
				return nil, fmt.Errorf("%s: competitor %q: domain %s is one of the brand's sites", path, comp.Name, d)
			}
		}
	}
	placed := map[string]string{}
	for class, hosts := range c.Sources {
		if !slices.Contains(SourceClasses, class) {
			return nil, fmt.Errorf("%s: sources.%s: class must be one of %s", path, class, strings.Join(SourceClasses, ", "))
		}
		for _, h := range hosts {
			k := cleanSite(h)
			if k == "" || strings.ContainsAny(k, " \t") {
				return nil, fmt.Errorf("%s: sources.%s: %q is not a host or host/path", path, class, h)
			}
			if prev, dup := placed[k]; dup && prev != class {
				return nil, fmt.Errorf("%s: sources: %s is in both %s and %s", path, h, prev, class)
			}
			placed[k] = class
		}
	}
	seen := map[string]bool{}
	for _, a := range c.Answerers {
		if a.ID == "" || seen[a.ID] {
			return nil, fmt.Errorf("%s: answerer ids must be set and unique (%q)", path, a.ID)
		}
		seen[a.ID] = true
		if a.Model == "" || a.Samples.Search < 0 || a.Samples.Recall < 0 || a.Samples.Search+a.Samples.Recall == 0 {
			return nil, fmt.Errorf("answerer %s requires model and positive sample count", a.ID)
		}
		switch a.Provider {
		case Anthropic, OpenAI, ClaudeCode, Codex:
		default:
			return nil, fmt.Errorf("%s: answerer %s: provider must be anthropic, openai, claude-code or codex", path, a.ID)
		}
	}
	for name, m := range map[string]Model{"grader": c.Grader, "analyst": c.Analyst} {
		switch m.Provider {
		case "", Anthropic, ClaudeCode:
		default:
			return nil, fmt.Errorf("%s: [%s] provider must be anthropic or claude-code", path, name)
		}
	}
	if c.Analyst.Themes != nil {
		return nil, fmt.Errorf("%s: themes belongs in [grader], not [analyst]", path)
	}
	if c.Sampling.MaxExtra < 0 || c.Sampling.MaxExtra > 1000 {
		return nil, fmt.Errorf("%s: [sampling] max_extra must be 0..1000", path)
	}
	if c.Sampling.Adaptive && c.Sampling.MaxExtra == 0 {
		return nil, fmt.Errorf("%s: [sampling] adaptive needs max_extra, the most extra answers a run may add", path)
	}
	if c.Budget.DailyUSD < 0 || math.IsNaN(c.Budget.DailyUSD) || math.IsInf(c.Budget.DailyUSD, 0) {
		return nil, fmt.Errorf("invalid daily admission budget")
	}
	for m, p := range c.Pricing {
		for _, v := range []float64{p.Input, p.Output, p.CacheRead, p.CacheWrite, p.SearchPer1K} {
			if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("invalid price for %s", m)
			}
		}
	}
	if c.Schedule.DailyHourUTC != nil && (*c.Schedule.DailyHourUTC < 0 || *c.Schedule.DailyHourUTC > 23) {
		return nil, fmt.Errorf("daily UTC hour must be 0..23")
	}
	if c.Budget.Concurrency <= 0 {
		c.Budget.Concurrency = 4
	}
	if c.Tiers.Core.EveryDays <= 0 {
		c.Tiers.Core.EveryDays = 1
	}
	if c.Tiers.PLG.EveryDays <= 0 {
		c.Tiers.PLG.EveryDays = 7
	}
	if c.Tiers.PLG.MaxSamples <= 0 {
		c.Tiers.PLG.MaxSamples = 1
	}
	return &c, nil
}

const (
	Anthropic  = "anthropic"
	OpenAI     = "openai"
	ClaudeCode = "claude-code"
	Codex      = "codex"
)

// Subscription reports whether a provider answers through a local CLI's subscription rather
// than per-token API billing.
func Subscription(provider string) bool { return provider == ClaudeCode || provider == Codex }

// Available reports whether this machine can call the provider: its API key is set, or its CLI
// is on the PATH.
func Available(provider string) bool { return Missing(provider) == "" }

// Missing says what the provider needs that isn't here, or "" when nothing is missing.
func Missing(provider string) string {
	switch provider {
	case ClaudeCode, Codex:
		bin := CLI(provider)
		if _, err := exec.LookPath(bin); err != nil {
			return bin + " is not on the PATH"
		}
		return ""
	}
	if provider != OpenAI && AnthropicBaseURL() != "" {
		return "" // a proxy that adds the key
	}
	if os.Getenv(KeyVar(provider)) == "" {
		return KeyVar(provider) + " is not set"
	}
	return ""
}

// AnthropicBaseURLVar names a base URL for Popcorn's own Anthropic API calls, such as a proxy that
// adds the key (exe.dev's integrations). With it set, ANTHROPIC_API_KEY is optional. It isn't
// ANTHROPIC_BASE_URL because the claude CLI reads that, and subscription answers must not go through
// a proxy that bills an API key.
const AnthropicBaseURLVar = "POPCORN_ANTHROPIC_BASE_URL"

// AnthropicBaseURL is AnthropicBaseURLVar without a trailing slash, or "".
func AnthropicBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(AnthropicBaseURLVar)), "/")
}

// AnswererName is what the UI calls an answerer: its name, or its id if it has none or is no
// longer configured.
func (c *Config) AnswererName(id string) string {
	for _, a := range c.Answerers {
		if a.ID == id && a.Name != "" {
			return a.Name
		}
	}
	return id
}

// CLI is the command a subscription provider runs through, or "" for an API provider.
func CLI(provider string) string {
	return map[string]string{ClaudeCode: "claude", Codex: "codex"}[provider]
}

func KeyVar(provider string) string {
	if provider == OpenAI {
		return "OPENAI_API_KEY"
	}
	return "ANTHROPIC_API_KEY"
}

// Slug is the brand name in lowercase letters, digits and dashes, for names that have to tell
// one deployment from another: the login cookie, the MCP server, export files.
func (b Brand) Slug() string {
	var out []rune
	dash := false
	for _, r := range strings.ToLower(b.Name) {
		if unicode.IsLetter(r) && r < 128 || unicode.IsDigit(r) {
			if dash && len(out) > 0 {
				out = append(out, '-')
			}
			out, dash = append(out, r), false
		} else {
			dash = true
		}
	}
	if len(out) == 0 {
		return "brand"
	}
	return string(out)
}

// AllSites is Sites, then each of Domains not already among them as kind "other".
func (b Brand) AllSites() []Site {
	out := append([]Site(nil), b.Sites...)
	for _, d := range b.Domains {
		dup := false
		for _, s := range b.Sites {
			dup = dup || strings.EqualFold(cleanSite(s.URL), cleanSite(d))
		}
		if !dup {
			out = append(out, Site{URL: d, Kind: "other"})
		}
	}
	return out
}

// SiteOf is the brand's site a URL is on: the most specific match, so docs.example.com wins over
// example.com for a docs page.
func (b Brand) SiteOf(url string) (Site, bool) {
	best, bestLen := Site{}, -1
	for _, s := range b.AllSites() {
		if n := MatchSite(s.URL, url); n > bestLen {
			best, bestLen = s, n
		}
	}
	return best, bestLen >= 0
}

// MatchSite reports how specifically a site (a host, which covers its subdomains, or a host/path
// prefix) matches a URL: the length of the site as written without scheme or www., or -1 when the
// URL isn't on it. The longest match is the most specific.
func MatchSite(site, url string) int {
	d := cleanSite(site)
	if d == "" {
		return -1
	}
	u := cleanSite(url)
	match := u == d || strings.HasPrefix(u, d+"/") || strings.HasPrefix(u, d+"?") || strings.HasPrefix(u, d+"#")
	// Subdomains of a bare host count too (docs.example.com under example.com).
	if !match && !strings.Contains(d, "/") {
		host := u
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		match = strings.HasSuffix(host, "."+d)
	}
	if !match {
		return -1
	}
	return len(d)
}

// FirstParty reports whether a URL is on one of the brand's sites.
func (b Brand) FirstParty(url string) bool {
	_, ok := b.SiteOf(url)
	return ok
}

// cleanSite is a URL or site without its scheme, a leading www. or a trailing slash, lowercased.
func cleanSite(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.TrimSuffix(strings.TrimPrefix(u, "www."), "/")
}
