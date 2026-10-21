// Package probe runs one query through the web search tool the answerers use and returns the
// result exactly as the model receives it: the links in order, before it writes anything.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/metcalfc/popcorn/internal/answer"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/llm"
)

type Input struct {
	Query string `json:"query"`
	// Only return results from these domains. Can't be combined with BlockedDomains.
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	// Never return results from these domains.
	BlockedDomains []string `json:"blocked_domains,omitempty"`
}

type Result struct {
	Query string `json:"query"`
	// What the model passed to the search tool. Normally the query, verbatim.
	ToolInput map[string]any `json:"tool_input"`
	// The tool result exactly as the API returned it to the model.
	Output     json.RawMessage `json:"output"`
	Error      string          `json:"error,omitempty"`
	Links      []answer.Link   `json:"links"`
	Model      string          `json:"model"`
	CostUSD    float64         `json:"cost_usd"`
	DurationMS int64           `json:"duration_ms"`
	RanAt      string          `json:"ran_at"`
}

// The instruction only exists to get exactly one search with the query verbatim; the model's own
// reply is thrown away.
const system = "Call the web search tool exactly once with the user's message as the query, unchanged. Do not rephrase it. After the result comes back, reply with only the word done."

// Model is the first Claude answerer's model (API or Claude Code), so the probe searches the way
// a run's answers do. The probe itself always goes through the API.
func Model(cfg *config.Config) (string, error) {
	for _, a := range cfg.Answerers {
		if a.Provider == config.Anthropic || a.Provider == config.ClaudeCode {
			return a.Model, nil
		}
	}
	return "", errors.New("the search probe needs a Claude answerer (anthropic or claude-code)")
}

func Run(ctx context.Context, cfg *config.Config, in Input) (Result, error) {
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return Result{}, errors.New("query is empty")
	}
	if len(in.AllowedDomains) > 0 && len(in.BlockedDomains) > 0 {
		return Result{}, errors.New("use allowed domains or blocked domains, not both")
	}
	model, err := Model(cfg)
	if err != nil {
		return Result{}, err
	}
	tool := map[string]any{"type": "web_search_20260318", "name": "web_search", "max_uses": 1}
	if len(in.AllowedDomains) > 0 {
		tool["allowed_domains"] = in.AllowedDomains
	}
	if len(in.BlockedDomains) > 0 {
		tool["blocked_domains"] = in.BlockedDomains
	}
	start := time.Now()
	resp, err := llm.Anthropic(ctx, map[string]any{
		"model":      model,
		"max_tokens": 256,
		"system":     system,
		"messages":   []any{map[string]any{"role": "user", "content": q}},
		"tools":      []any{tool},
	})
	if err != nil {
		return Result{}, err
	}
	res := Result{Query: q, Model: llm.Str(resp["model"]), DurationMS: time.Since(start).Milliseconds(), RanAt: start.UTC().Format(time.RFC3339)}
	if res.Model == "" {
		res.Model = model
	}
	res.CostUSD = cfg.Cost(model, answer.ParseAnthropic(resp).Usage)
	var callID string
	for _, raw := range llm.List(resp["content"]) {
		b := llm.Map(raw)
		switch llm.Str(b["type"]) {
		case "server_tool_use":
			if llm.Str(b["name"]) == "web_search" && callID == "" {
				callID = llm.Str(b["id"])
				res.ToolInput = llm.Map(b["input"])
			}
		case "web_search_tool_result":
			if llm.Str(b["tool_use_id"]) != callID || res.Output != nil {
				continue
			}
			res.Output, _ = json.Marshal(b["content"])
			if m, ok := b["content"].(map[string]any); ok { // an error result is an object, not a list
				res.Error = llm.Str(m["error_code"])
			}
			for _, r := range llm.List(b["content"]) {
				rm := llm.Map(r)
				res.Links = append(res.Links, answer.Link{URL: llm.Str(rm["url"]), Title: llm.Str(rm["title"])})
			}
		}
	}
	if callID == "" {
		return res, fmt.Errorf("the model didn't call web search")
	}
	return res, nil
}

// Domains splits a comma- or space-separated list from a form field.
func Domains(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
}
