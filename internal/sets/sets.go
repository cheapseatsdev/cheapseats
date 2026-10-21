// Package sets loads prompt sets: one TOML file per category under prompt-sets/ in the
// popcorn-config repo, beside popcorn.toml, changed only by pull request there.
package sets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"encoding/json"
	"github.com/BurntSushi/toml"
)

type Tier string

const (
	Core Tier = "core" // the bet: daily, full samples
	PLG  Tier = "plg"  // kept measured: every few days, one sample
)

type Competitor struct {
	Name    string   `toml:"name" json:"name"`
	Aliases []string `toml:"aliases" json:"aliases,omitempty"`
}

// Prompt is one buyer question. Its id is what results, trends and fixes key on, so it stays the
// same for as long as the question means the same thing: fixing a typo or rewording keeps the id
// (the trend continues, marked as reworded), and a different question gets a new one. Ids are
// unique across every set, so a prompt moved between sets keeps its history, and never reused.
type Prompt struct {
	ID   string `toml:"id" json:"id"`
	Text string `toml:"text" json:"text"`
}

// UnmarshalJSON also reads the plain strings that run snapshots stored before prompts had ids.
func (p *Prompt) UnmarshalJSON(b []byte) error {
	var text string
	if json.Unmarshal(b, &text) == nil {
		*p = Prompt{Text: text}
		return nil
	}
	type plain Prompt
	return json.Unmarshal(b, (*plain)(p))
}

// SHA identifies a prompt's wording, so a reworded prompt shows as a revision of the same id.
// Whitespace doesn't count.
func (p Prompt) SHA() string {
	h := sha256.Sum256([]byte(strings.Join(strings.Fields(p.Text), " ")))
	return hex.EncodeToString(h[:])[:16]
}

type Set struct {
	ID          string       `toml:"id" json:"id"`
	Name        string       `toml:"name" json:"name"`
	Description string       `toml:"description" json:"description"`
	Tier        Tier         `toml:"tier" json:"tier"`
	Prompts     []Prompt     `toml:"prompts" json:"prompts"`
	Competitors []Competitor `toml:"competitors" json:"competitors"`
	// Hash of the whole set, for display: what changed is worked out prompt by prompt.
	Version string `toml:"-" json:"version"`
	File    string `toml:"-" json:"file"`
}

// Prompt is the set's prompt with this id.
func (s Set) Prompt(id string) (Prompt, bool) {
	for _, p := range s.Prompts {
		if p.ID == id {
			return p, true
		}
	}
	return Prompt{}, false
}

// HeadToHeads is the one set that names the brand. Every other set asks the way a buyer who
// hasn't heard of it would, so being named there has to be earned.
const HeadToHeads = "head-to-heads"

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Dir is POPCORN_PROMPT_SETS, or prompt-sets/ beside popcorn.toml.
func Dir(configDir string) string {
	if d := os.Getenv("POPCORN_PROMPT_SETS"); d != "" {
		return d
	}
	return filepath.Join(configDir, "prompt-sets")
}

// LoadAll reads every set, as Popcorn does once at startup. A set that doesn't parse, or a set or
// prompt id used twice, is an error rather than a set quietly left out, so a deploy with a broken
// config fails to start instead of running without part of it. Core sets come first.
func LoadAll(dir string) ([]Set, error) {
	out, errs := load(dir)
	return out, errors.Join(errs...)
}

func load(dir string) ([]Set, []error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, []error{err}
	}
	if len(files) == 0 {
		return nil, []error{fmt.Errorf("no prompt sets in %s", dir)}
	}
	sort.Strings(files)
	var out []Set
	var errs []error
	seen := map[string]string{}
	prompts := map[string]string{}
	for _, f := range files {
		s, err := Parse(f)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(f), err))
			continue
		}
		if prev, dup := seen[s.ID]; dup {
			errs = append(errs, fmt.Errorf("%s: id %q is already used by %s", filepath.Base(f), s.ID, prev))
			continue
		}
		seen[s.ID] = filepath.Base(f)
		ok := true
		for _, p := range s.Prompts {
			if prev, dup := prompts[p.ID]; dup && prev != s.ID {
				errs = append(errs, fmt.Errorf("%s: prompt id %q is already used in %s; prompt ids are unique across sets", filepath.Base(f), p.ID, prev))
				ok = false
			}
		}
		if !ok {
			continue
		}
		for _, p := range s.Prompts {
			prompts[p.ID] = s.ID
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Tier == Core && out[j].Tier != Core })
	return out, errs
}

// Check is LoadAll plus the rules for writing sets, for `popcorn check` in the config repo: the
// same question isn't asked twice, and only head-to-heads may name the brand.
func Check(dir string, brand []string) error {
	all, errs := load(dir)
	texts := map[string]string{}
	for _, s := range all {
		for _, p := range s.Prompts {
			if prev, dup := texts[p.SHA()]; dup && prev != p.ID {
				errs = append(errs, fmt.Errorf("%s: prompt %q asks the same thing as %q", s.File, p.ID, prev))
			}
			texts[p.SHA()] = p.ID
		}
		if s.ID == HeadToHeads {
			continue
		}
		for _, p := range s.Prompts {
			for _, b := range brand {
				if Names(p.Text, b) {
					errs = append(errs, fmt.Errorf("%s: prompt %s names %s; only %s may", s.File, p.ID, b, HeadToHeads))
					break
				}
			}
		}
	}
	return errors.Join(errs...)
}

// Names reports whether text names name as a whole word, ignoring case, so a short alias like
// "rl" doesn't match "url".
func Names(text, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	re := regexp.MustCompile(`(?i)(^|[^\pL\pN])` + regexp.QuoteMeta(name) + `($|[^\pL\pN])`)
	return re.MatchString(text)
}

func Parse(path string) (Set, error) {
	var s Set
	md, err := toml.DecodeFile(path, &s)
	if err != nil {
		if strings.Contains(err.Error(), "prompts") {
			return s, fmt.Errorf("%w (prompts are [[prompts]] tables with an id and text; popcorn migrate-config converts a list of strings)", err)
		}
		return s, err
	}
	if u := md.Undecoded(); len(u) > 0 {
		return s, fmt.Errorf("unknown keys: %v", u)
	}
	if !idRe.MatchString(s.ID) {
		return s, fmt.Errorf("id must be lowercase letters, digits and dashes, got %q", s.ID)
	}
	if s.Tier == "" {
		s.Tier = Core
	}
	if s.Tier != Core && s.Tier != PLG {
		return s, fmt.Errorf("tier must be core or plg, got %q", s.Tier)
	}
	if len(s.Prompts) == 0 {
		return s, fmt.Errorf("prompts is empty")
	}
	ids := map[string]bool{}
	for i := range s.Prompts {
		p := &s.Prompts[i]
		p.Text = strings.TrimSpace(p.Text)
		if !idRe.MatchString(p.ID) {
			return s, fmt.Errorf("prompt %d: id must be lowercase letters, digits and dashes, got %q", i+1, p.ID)
		}
		if p.Text == "" {
			return s, fmt.Errorf("prompt %s: text is empty", p.ID)
		}
		if ids[p.ID] {
			return s, fmt.Errorf("prompt id %q is used twice", p.ID)
		}
		ids[p.ID] = true
	}
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	s.Version = Fingerprint(s)
	s.File = filepath.Base(path)
	return s, nil
}

func Fingerprint(s Set) string {
	s.Version = ""
	s.File = ""
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:16]
}
