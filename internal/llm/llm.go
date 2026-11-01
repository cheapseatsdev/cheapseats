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
	"github.com/cheapseatsdev/cheapseats/internal/bounded"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/redact"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 10 * time.Minute}

// Base URLs, overridable for tests.
var (
	AnthropicURL = "https://api.anthropic.com/v1/messages"
	OpenAIURL    = "https://api.openai.com/v1/responses"
)

// maxResponse is the largest successful response read. A Messages or Responses reply with web
// search results runs to a few hundred KB; past this the reply is an error, not a retry.
var maxResponse int64 = 32 << 20

// APIError is a non-200 reply. Body is at most bounded.ErrorPrefix bytes of what was sent.
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
			if resp.StatusCode == http.StatusOK {
				data, err := bounded.Read(resp.Body, maxResponse)
				resp.Body.Close()
				if err != nil {
					lease.Finish(0, false)
					return nil, fmt.Errorf("reading response: %w", err)
				}
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
			msg := bounded.Prefix(resp.Body)
			resp.Body.Close()
			if err := lease.Finish(0, resp.StatusCode < 500); err != nil {
				return nil, err
			}
			last = &APIError{Status: resp.StatusCode, Body: msg}
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

// Anthropic posts a Messages API request, to config.AnthropicBaseURLVar when it's set. A proxy there
// adds the key, so without ANTHROPIC_API_KEY the request goes without one.
func Anthropic(ctx context.Context, body map[string]any) (map[string]any, error) {
	url, headers := AnthropicURL, map[string]string{"anthropic-version": "2023-06-01"}
	if base := config.AnthropicBaseURL(); base != "" {
		url = base + "/v1/messages"
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" && url == AnthropicURL {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY: %w", ErrNoKey)
	}
	if key != "" {
		headers["x-api-key"] = key
	}
	return post(ctx, url, headers, body)
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
// schema must pass CheckSchema and be registered with RegisterSchema.
func Structured(ctx context.Context, model, system, user string, maxTokens int, schema map[string]any, out any) (map[string]any, error) {
	if err := CheckSchema(schema); err != nil {
		return nil, err
	}
	if err := Registered(schema); err != nil {
		return nil, err
	}
	resp, err := Anthropic(ctx, structuredBody(model, system, user, maxTokens, schema))
	if err != nil {
		return nil, err
	}
	// Only a reply that ended on its own is a whole answer. refusal and max_tokens were the
	// two checked; model_context_window_exceeded and pause_turn are stops too, and a stop
	// reason added later is safer read as one than decoded. "" is a reply without the field.
	// The error is stored (fixes_error) and an alert is keyed on its text, so it says only what
	// stays the same when the failure repeats; the counts go to the log line.
	switch sr := Str(resp["stop_reason"]); sr {
	case "", "end_turn", "stop_sequence":
	default:
		slog.Warn("structured output stopped", "operation", operation(ctx), "reply", Reply(resp))
		return resp, fmt.Errorf("structured output stopped: %s (%s)", sr, ReplyShape(resp))
	}
	if err := json.Unmarshal([]byte(ReplyText(resp)), out); err != nil {
		slog.Warn("structured output undecodable", "operation", operation(ctx), "err", err, "reply", Reply(resp))
		return resp, fmt.Errorf("decoding structured output: %w (%s)", err, ReplyShape(resp))
	}
	return resp, nil
}

// Operation is the label WithOperation gave ctx ("analysis/<run id>"), or "".
func Operation(ctx context.Context) string { return operation(ctx) }

// ReplyShape describes a Messages reply by what stays the same when it repeats: its stop
// reason, content block types, and whether it had any text. Errors that are stored and alerted
// on use it; Reply adds the counts, for a log line.
func ReplyShape(resp map[string]any) string {
	if resp == nil {
		return "no reply"
	}
	sr := Str(resp["stop_reason"])
	if sr == "" {
		sr = "none"
	}
	var blocks []string
	for _, b := range List(resp["content"]) {
		blocks = append(blocks, Str(Map(b)["type"]))
	}
	text := "text present"
	if strings.TrimSpace(ReplyText(resp)) == "" {
		text = "no text"
	}
	return fmt.Sprintf("stop_reason %s, blocks [%s], %s", sr, strings.Join(blocks, " "), text)
}

// ReplyText is a Messages reply's text blocks joined, which Structured decodes. Thinking and
// any other blocks are left out.
func ReplyText(resp map[string]any) string {
	text := ""
	for _, b := range List(resp["content"]) {
		if Str(Map(b)["type"]) == "text" {
			text += Str(Map(b)["text"])
		}
	}
	return text
}

// Reply describes a Messages reply for an error or a log line: its stop reason, input and
// output tokens, content block types and the length of its text. An output that decodes but
// says nothing can then be told apart: a short reply that ended on its own, one whose output
// went to thinking, or a text block missing.
func Reply(resp map[string]any) string {
	if resp == nil {
		return "no reply"
	}
	u := Map(resp["usage"])
	sr := Str(resp["stop_reason"])
	if sr == "" {
		sr = "none"
	}
	var blocks []string
	for _, b := range List(resp["content"]) {
		blocks = append(blocks, Str(Map(b)["type"]))
	}
	return fmt.Sprintf("stop_reason %s, %d input tokens, %d output tokens, blocks [%s], %d characters of text",
		sr, Int(u["input_tokens"])+Int(u["cache_read_input_tokens"])+Int(u["cache_creation_input_tokens"]), Int(u["output_tokens"]),
		strings.Join(blocks, " "), len([]rune(ReplyText(resp))))
}

// structuredBody is a Messages request for JSON matching schema; ProbeSchema sends the same shape.
func structuredBody(model, system, user string, maxTokens int, schema map[string]any) map[string]any {
	return map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   []any{map[string]any{"role": "user", "content": user}},
		"output_config": map[string]any{
			"format": map[string]any{"type": "json_schema", "schema": schema},
		},
	}
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
