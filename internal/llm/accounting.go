package llm

import (
	"context"
	"fmt"
	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/config"
)

type accountingKey struct{}
type operationKey struct{}

func WithOperation(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, operationKey{}, label)
}

func WithAccounting(ctx context.Context, b *budget.Controller) context.Context {
	return context.WithValue(ctx, accountingKey{}, b)
}
func admit(ctx context.Context, body any) (*budget.Lease, *budget.Controller, string, error) {
	c, _ := ctx.Value(accountingKey{}).(*budget.Controller)
	if c == nil {
		return nil, nil, "", nil
	}
	m := Str(Map(body)["model"])
	p, ok := c.Cfg.Pricing[m]
	if !ok || p.Input <= 0 || p.Output <= 0 {
		return nil, c, m, fmt.Errorf("model %q has no positive configured pricing", m)
	}
	l, err := c.Reserve(ctx, "provider-attempt/"+operation(ctx), m, c.Allowance())
	return l, c, m, err
}

// Usage is what a response is billed for, read from an Anthropic Messages or (openai) OpenAI
// Responses response. Budget settlement and the answer's recorded cost both use it, so they agree.
// OpenAI bills search actions as tool calls; opening or searching within a page isn't one.
func Usage(resp map[string]any, openai bool) config.Usage {
	u := Map(resp["usage"])
	v := config.Usage{Input: Int(u["input_tokens"]), Output: Int(u["output_tokens"]), CacheRead: Int(u["cache_read_input_tokens"]), CacheWrite: Int(u["cache_creation_input_tokens"]), Searches: Int(Map(u["server_tool_use"])["web_search_requests"])}
	if openai {
		v.CacheRead = Int(Map(u["input_tokens_details"])["cached_tokens"])
		v.Input -= v.CacheRead
		v.CacheWrite = 0
		v.Searches = 0
		for _, x := range List(resp["output"]) {
			o := Map(x)
			if Str(o["type"]) != "web_search_call" {
				continue
			}
			// Without the action (older responses), count it: an overcount is the safe side.
			if t := Str(Map(o["action"])["type"]); t == "" || t == "search" {
				v.Searches++
			}
		}
	}
	return v
}

func operation(ctx context.Context) string { s, _ := ctx.Value(operationKey{}).(string); return s }
func knownUsage(resp map[string]any, openai bool) bool {
	u, ok := resp["usage"].(map[string]any)
	if !ok {
		return false
	}
	for _, k := range []string{"input_tokens", "output_tokens"} {
		v, ok := u[k].(float64)
		if !ok || v < 0 {
			return false
		}
	}
	v := Usage(resp, openai)
	return v.Input >= 0 && v.Output >= 0 && v.CacheRead >= 0 && v.CacheWrite >= 0 && v.Searches >= 0
}
