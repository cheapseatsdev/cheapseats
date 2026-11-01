package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/bounded"
	"github.com/cheapseatsdev/cheapseats/internal/errclass"
)

// boundedServe answers every request with status and body, counting the calls.
func boundedServe(t *testing.T, status int, body string) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := AnthropicURL
	AnthropicURL = srv.URL
	t.Cleanup(func() { AnthropicURL = old })
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	return &calls
}

func TestAnOversizedReplyIsAnErrorAndNotRetried(t *testing.T) {
	old := maxResponse
	maxResponse = 1000
	t.Cleanup(func() { maxResponse = old })
	calls := boundedServe(t, http.StatusOK, `{"content":"`+strings.Repeat("x", 2000)+`"}`)
	_, err := Anthropic(context.Background(), map[string]any{"model": "m"})
	if !errors.Is(err, bounded.ErrTooLarge) {
		t.Fatalf("err %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("an oversized reply was retried: %d calls", calls.Load())
	}
}

func TestAnErrorReplyKeepsOnlyItsStart(t *testing.T) {
	boundedServe(t, http.StatusBadRequest, strings.Repeat("e", 3*bounded.ErrorPrefix))
	_, err := Anthropic(context.Background(), map[string]any{"model": "m"})
	var ae *errclass.Error
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Category != "http_400" {
		t.Fatalf("err %v", err)
	}
	if len(ae.Detail()) != bounded.ErrorPrefix {
		t.Errorf("error body kept %d bytes; want %d", len(ae.Detail()), bounded.ErrorPrefix)
	}
}
