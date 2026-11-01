package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

// Verification records why a candidate is safe to hand off as work. An empty or
// legacy status is not verification, even when an old fix has an implementation prompt.
type Verification struct {
	Status  string              `json:"status"`
	Summary string              `json:"summary"`
	Sources []string            `json:"sources"`
	Checks  []VerificationCheck `json:"checks,omitempty"`
	Context string              `json:"context,omitempty"`
}

// VerificationContext digests what a verification was judged against, so Triage can tell when
// a saved action needs verifying again without changing or re-grading the answer benchmark. It
// is computed once the checks are known (target is the action's target):
//
//   - What the product is (about, audience, category) and the advisory brief. Editing any of
//     these can change a strategy or capability verdict without touching a quote, so every
//     saved action is verified again.
//   - Each whole fact a brand.facts check quoted from (quotedFacts), not just the quoted span.
//     Prefixing "no longer" or appending "except the Free plan" to that fact, or removing it,
//     changes the digest even when the quoted words survive, there or inside another fact.
//     Adding a fact, or editing one the action didn't quote, leaves it unchanged.
//   - Which of the target and the pages the checks quoted are on one of the brand's configured
//     sites or domains (onSite). Removing the site such a page is on, or adding one that turns a
//     third party's page into the brand's own, changes whether the work is owned or outreach.
//     Adding, removing or narrowing other sites leaves the digest unchanged.
//
// The rest of the brand is left out. The name, aliases and owners are not criteria the verifier
// is given, and Triage rebuilds the handoff, site and owner from the current config on every
// read. The set's description is not digested either, though the verifier's prompt shows it as
// the set's purpose: the whole-brand digest before this one left it out too, and editing it
// leaves verified actions as they are until the analysis is rebuilt (docs/next-steps.md).
func VerificationContext(cfg *config.Config, target string, v Verification) string {
	var facts, owned []string
	for _, c := range v.Checks {
		if c.Source == "brand.facts" {
			facts = append(facts, quotedFacts(cfg.Brand.Facts, c.Quote)...)
		}
	}
	for _, u := range append([]string{target}, v.Sources...) {
		if u := webURL(u); u != "" {
			if _, ok := cfg.Brand.SiteOf(u); ok {
				owned = append(owned, u)
			}
		}
	}
	b, _ := json.Marshal(struct {
		About, Audience, Category, Advisory string
		Facts, OnSite                       []string
	}{cfg.Brand.About, cfg.Brand.Audience, cfg.Brand.Category, cfg.Brand.Advisory.Text(), sortedSet(facts), sortedSet(owned)})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// quotedFacts is every whole fact a quote overlaps, wherever it occurs in the facts joined the
// way checkedProof searched them. A quote spanning two facts depends on both.
func quotedFacts(facts []string, quote string) []string {
	joined := strings.Join(facts, "\n")
	var out []string
	for from := 0; quote != "" && from <= len(joined); {
		i := strings.Index(joined[from:], quote)
		if i < 0 {
			break
		}
		start := from + i
		end := start + len(quote)
		pos := 0
		for _, f := range facts {
			if pos < end && start < pos+len(f) {
				out = append(out, f)
			}
			pos += len(f) + 1
		}
		from = start + 1
	}
	return out
}

func sortedSet(xs []string) []string {
	sort.Strings(xs)
	return slices.Compact(xs)
}

// legacyVerificationContext is the whole-brand digest stored before VerificationContext was
// narrowed. It still matches while the brand is unchanged, so upgrading doesn't demote actions
// verified under it; any brand edit since then still needs them verified again. That is
// stricter than the current digest, never looser.
func legacyVerificationContext(cfg *config.Config) string {
	b, _ := json.Marshal(struct {
		Brand    config.Brand
		Advisory string
	}{cfg.Brand, cfg.Brand.Advisory.Text()})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// currentContext reports whether a stored verification was made against today's brief, the
// facts it quoted and the sites its pages are on.
func currentContext(cfg *config.Config, f Fix) bool {
	c := f.Verification.Context
	return c != "" && (c == VerificationContext(cfg, f.Target, f.Verification) || c == legacyVerificationContext(cfg))
}

type VerificationCheck struct {
	Gate   string `json:"gate"`
	Source string `json:"source"`
	Quote  string `json:"quote"`
	Reason string `json:"reason"`
}

// Recorded checks the provenance shape of persisted verification without reading
// the web again. Semantic relevance is still the verifier's judgment; a saved
// status and context digest alone are not evidence that verification happened.
func (v Verification) Recorded() bool {
	if v.Status != "verified" || v.Context == "" || len(v.Checks) != len(gates) || len(v.Sources) == 0 {
		return false
	}
	sources := map[string]bool{}
	for _, s := range v.Sources {
		u, ok := proofURL(s)
		if !ok {
			return false
		}
		sources[u] = true
	}
	seen := map[string]bool{}
	for _, c := range v.Checks {
		if seen[c.Gate] || strings.TrimSpace(c.Source) == "" || strings.TrimSpace(c.Quote) == "" || strings.TrimSpace(c.Reason) == "" {
			return false
		}
		seen[c.Gate] = true
		switch c.Gate {
		case "strategy":
			if c.Source != "brand.advisory" {
				return false
			}
		case "completion":
			if c.Source != "candidate.done_when" {
				return false
			}
		case "existing_work", "remaining_change":
			u, ok := proofURL(c.Source)
			if !ok || !sources[u] {
				return false
			}
		case "capability", "eligibility":
			if c.Source == "brand.facts" || c.Source == "brand.advisory" {
				continue
			}
			u, ok := proofURL(c.Source)
			if !ok || !sources[u] {
				return false
			}
		default:
			return false
		}
	}
	return len(seen) == len(gates)
}

func proofURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	return webURL(raw), true
}

func recordedFix(f Fix) bool {
	if !f.Verification.Recorded() {
		return false
	}
	for _, c := range f.Verification.Checks {
		if c.Gate == "completion" && c.Quote != f.DoneWhen {
			return false
		}
		if (c.Gate == "existing_work" || c.Gate == "remaining_change") && f.Type != Product && webURL(c.Source) != webURL(f.Target) {
			return false
		}
	}
	return true
}

func goalAllowsProduct(cfg *config.Config, f Fix) bool {
	for _, c := range f.Verification.Checks {
		if c.Gate != "strategy" || c.Source != "brand.advisory" {
			continue
		}
		for _, goal := range cfg.Brand.Advisory.Goals {
			if strings.TrimSpace(c.Quote) != "" && strings.Contains(goal, c.Quote) {
				return true
			}
		}
	}
	return false
}

type Investigation struct {
	Title     string   `json:"title"`
	Why       string   `json:"why"`
	Check     string   `json:"check"`
	Decision  string   `json:"decision"`
	Evidence  string   `json:"evidence"`
	Prompts   []int    `json:"prompts"`
	PromptIDs []string `json:"prompt_ids,omitempty"`
	Target    string   `json:"target,omitempty"`
}

type Finding struct {
	Title     string   `json:"title"`
	Detail    string   `json:"detail"`
	Evidence  string   `json:"evidence"`
	Prompts   []int    `json:"prompts"`
	PromptIDs []string `json:"prompt_ids,omitempty"`
}

type Analysis struct {
	Insights       string          `json:"insights"`
	Fixes          []Fix           `json:"fixes"`
	Investigations []Investigation `json:"investigations"`
	Findings       []Finding       `json:"market_findings"`
	// VerificationFailed marks an analysis whose verification call failed: its candidates are
	// investigations, unverified. It is stored only for a run with no earlier analysis, together
	// with the error, so the error's surfaces can say that is what they show.
	VerificationFailed bool `json:"verification_failed,omitempty"`
}

// Encode retains the complete advisory result in the existing fixes JSON column.
// This envelope is separate from the answer/grading method and its historical data.
func Encode(a Analysis) string {
	b, _ := json.Marshal(struct {
		Version int `json:"version"`
		Analysis
	}{1, a})
	return string(b)
}

// DecodeAnalysis accepts historical arrays without treating their lack of checks
// as verification. A claimed status in an old array cannot upgrade its provenance.
func DecodeAnalysis(s string) (Analysis, error) {
	a := Analysis{Fixes: []Fix{}, Investigations: []Investigation{}, Findings: []Finding{}}
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return a, nil
	}
	if strings.HasPrefix(s, "[") {
		if err := json.Unmarshal([]byte(s), &a.Fixes); err != nil {
			return a, fmt.Errorf("legacy fixes: %w", err)
		}
		for i := range a.Fixes {
			a.Fixes[i].Verification = Verification{Status: "unverified", Summary: "Historical suggestion; current work and eligibility were not verified.", Sources: []string{}}
			a.Fixes[i].ClaudePrompt = ""
		}
		return a, nil
	}
	var envelope struct {
		Version int `json:"version"`
		Analysis
	}
	if err := json.Unmarshal([]byte(s), &envelope); err != nil {
		return a, fmt.Errorf("analysis: %w", err)
	}
	if envelope.Version != 1 {
		return a, fmt.Errorf("unsupported analysis version %d", envelope.Version)
	}
	for i := range envelope.Fixes {
		if envelope.Fixes[i].Verification.Status == "verified" && !recordedFix(envelope.Fixes[i]) {
			envelope.Fixes[i].Verification.Status = "unverified"
			envelope.Fixes[i].Verification.Summary = "The stored verification lacks complete source checks or product context; its readiness is unknown."
		}
		if envelope.Fixes[i].Verification.Status != "verified" {
			envelope.Fixes[i].ClaudePrompt = ""
		}
	}
	return envelope.Analysis, nil
}

// Decode is the historical fix-only view. Callers needing errors or the other
// categories use DecodeAnalysis; malformed old data still reads as an empty list.
func Decode(s string) []Fix {
	a, err := DecodeAnalysis(s)
	if err != nil {
		return nil
	}
	return a.Fixes
}
