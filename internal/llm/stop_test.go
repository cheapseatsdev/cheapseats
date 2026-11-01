package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

// fakeReply serves one Messages reply for every request, the way the API answers a
// structured output request.
func fakeReply(t *testing.T, reply map[string]any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	old := AnthropicURL
	AnthropicURL = srv.URL
	t.Cleanup(func() { AnthropicURL = old })
	t.Setenv("ANTHROPIC_API_KEY", "test")
	t.Setenv(config.AnthropicBaseURLVar, "")
}

// Only refusal and max_tokens were treated as stops, so a reply cut off another way was decoded
// as if whole. Every stop but the model ending its turn is an error that says what came back.
func TestStructuredRejectsEveryEarlyStop(t *testing.T) {
	for _, sr := range []string{"refusal", "max_tokens", "model_context_window_exceeded", "pause_turn", "a_stop_added_later"} {
		fakeReply(t, map[string]any{
			"stop_reason": sr,
			"content":     []any{map[string]any{"type": "text", "text": `{"ok":true}`}},
			"usage":       map[string]any{"input_tokens": 120000, "output_tokens": 900},
		})
		var out map[string]any
		_, err := Structured(context.Background(), "m", "s", "u", 10, schemaTestRegistered, &out)
		if err == nil || !strings.Contains(err.Error(), "stopped: "+sr+" (stop_reason "+sr+", blocks [text], text present)") || strings.Contains(err.Error(), "output tokens") {
			t.Errorf("%s: want a stop error with the reply's stable shape, got %v", sr, err)
		}
	}
}

// Claude Opus 5.5 always thinks, so a reply carries a thinking block (empty by default) beside
// the JSON. The thinking block is not the answer, and Reply says it was there and what the
// output tokens went to.
func TestStructuredDecodesTheTextBesideThinking(t *testing.T) {
	reply := map[string]any{
		"stop_reason": "end_turn",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": "sig"},
			map[string]any{"type": "text", "text": `{"insights":"","fixes":[]}`},
		},
		"usage": map[string]any{"input_tokens": 100, "cache_read_input_tokens": 20, "output_tokens": 1234},
	}
	fakeReply(t, reply)
	var out struct {
		Insights string `json:"insights"`
	}
	resp, err := Structured(context.Background(), "m", "s", "u", 10, schemaTestRegistered, &out)
	if err != nil || out.Insights != "" {
		t.Fatalf("decoding: %v %+v", err, out)
	}
	want := "stop_reason end_turn, 120 input tokens, 1234 output tokens, blocks [thinking text], 26 characters of text"
	if got := Reply(resp); got != want {
		t.Errorf("Reply = %q, want %q", got, want)
	}
	if got := ReplyText(resp); got != `{"insights":"","fixes":[]}` {
		t.Errorf("ReplyText = %q", got)
	}
}

// A reply with no text block (all of it thinking) fails to decode, and the error says so.
func TestStructuredWithoutTextSaysWhatCameBack(t *testing.T) {
	fakeReply(t, map[string]any{
		"stop_reason": "end_turn",
		"content":     []any{map[string]any{"type": "thinking", "thinking": ""}},
		"usage":       map[string]any{"input_tokens": 5, "output_tokens": 7},
	})
	var out map[string]any
	_, err := Structured(context.Background(), "m", "s", "u", 10, schemaTestRegistered, &out)
	if err == nil || !strings.Contains(err.Error(), "(stop_reason end_turn, blocks [thinking], no text)") {
		t.Fatalf("want a decoding error describing the reply, got %v", err)
	}
}

// The stored error is what the ops watch alerts on when it changes, so the same failure with
// other token counts must give the same error; the counts are in the log line, with the run.
func TestStructuredErrorsAreStableAndTheCountsAreLogged(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	ctx := WithOperation(context.Background(), "analysis/r-stable")
	var errs []string
	for _, n := range []int{900, 4321} {
		fakeReply(t, map[string]any{
			"stop_reason": "model_context_window_exceeded",
			"content":     []any{map[string]any{"type": "text", "text": strings.Repeat("x", n)}},
			"usage":       map[string]any{"input_tokens": 100000 + n, "output_tokens": n},
		})
		var out map[string]any
		_, err := Structured(ctx, "m", "s", "u", 10, schemaTestRegistered, &out)
		if err == nil {
			t.Fatal("want an error")
		}
		errs = append(errs, err.Error())
	}
	if errs[0] != errs[1] {
		t.Errorf("the same stop gave different errors:\n%s\n%s", errs[0], errs[1])
	}
	log := buf.String()
	for _, want := range []string{"operation=analysis/r-stable", "4321 output tokens", "104321 input tokens", "4321 characters of text"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}
