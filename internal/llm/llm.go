// Package llm is a thin HTTP client for the Anthropic Messages API and the OpenAI Responses API,
// with retries on rate limits and overloads. Bodies and responses stay as JSON maps so the
// answerers can read exactly the blocks they need.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/metcalfc/popcorn/internal/redact"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"time"
)

var client = &http.Client{Timeout: 10 * time.Minute}

// Base URLs, overridable for tests.
var (
	AnthropicURL = "https://api.anthropic.com/v1/messages"
	OpenAIURL    = "https://api.openai.com/v1/responses"
)

type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, Truncate(redact.Text(e.Body), 400))
}

func retryable(status int) bool {
	return status == 408 || status == 409 || status == 429 || status == 529 || status >= 500
}

func post(ctx context.Context, url string, headers map[string]string, body any) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		lease, meter, model, err := admit(ctx, body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			lease.Finish(0, true)
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ferr := lease.Finish(0, false); ferr != nil {
				return nil, ferr
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = err
		} else {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var out map[string]any
				if err := json.Unmarshal(data, &out); err != nil {
					lease.Finish(0, false)
					return nil, fmt.Errorf("decoding response: %w", err)
				}
				if meter != nil {
					known := knownUsage(out, url == OpenAIURL)
					if err := lease.Finish(meter.Cfg.Cost(model, Usage(out, url == OpenAIURL)), known); err != nil {
						return out, err
					}
				}
				return out, nil
			}
			if err := lease.Finish(0, resp.StatusCode < 500); err != nil {
				return nil, err
			}
			last = &APIError{Status: resp.StatusCode, Body: string(data)}
			if !retryable(resp.StatusCode) {
				return nil, last
			}
			if s, err := strconv.Atoi(resp.Header.Get("retry-after")); err == nil && s > 0 && s < 120 {
				if err := sleep(ctx, time.Duration(s)*time.Second); err != nil {
					return nil, err
				}
				continue
			}
		}
		backoff := time.Duration(1<<attempt)*2*time.Second + time.Duration(rand.IntN(1000))*time.Millisecond
		if err := sleep(ctx, backoff); err != nil {
			return nil, err
		}
	}
	return nil, last
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var ErrNoKey = errors.New("API key not set")

// Anthropic posts a Messages API request.
func Anthropic(ctx context.Context, body map[string]any) (map[string]any, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY: %w", ErrNoKey)
	}
	return post(ctx, AnthropicURL, map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}, body)
}

// OpenAI posts a Responses API request.
func OpenAI(ctx context.Context, body map[string]any) (map[string]any, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY: %w", ErrNoKey)
	}
	return post(ctx, OpenAIURL, map[string]string{"authorization": "Bearer " + key}, body)
}

// Structured asks Claude for JSON matching schema and decodes it into out. Returns the usage.
func Structured(ctx context.Context, model, system, user string, maxTokens int, schema map[string]any, out any) (map[string]any, error) {
	resp, err := Anthropic(ctx, map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   []any{map[string]any{"role": "user", "content": user}},
		"output_config": map[string]any{
			"format": map[string]any{"type": "json_schema", "schema": schema},
		},
	})
	if err != nil {
		return nil, err
	}
	if sr := Str(resp["stop_reason"]); sr == "refusal" || sr == "max_tokens" {
		return resp, fmt.Errorf("structured output stopped: %s", sr)
	}
	text := ""
	for _, b := range List(resp["content"]) {
		if Str(Map(b)["type"]) == "text" {
			text += Str(Map(b)["text"])
		}
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return resp, fmt.Errorf("decoding structured output: %w", err)
	}
	return resp, nil
}

// Helpers for reading untyped JSON without a panic.

func Map(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func List(v any) []any {
	l, _ := v.([]any)
	return l
}

func Str(v any) string {
	s, _ := v.(string)
	return s
}

func Int(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
