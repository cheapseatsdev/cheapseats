package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
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

// VerificationContext invalidates advisory work when the current product brief
// changes, without changing or re-grading the answer benchmark.
func VerificationContext(cfg *config.Config) string {
	b, _ := json.Marshal(struct {
		Brand    config.Brand
		Advisory string
	}{cfg.Brand, cfg.Brand.Advisory.Text()})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
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
