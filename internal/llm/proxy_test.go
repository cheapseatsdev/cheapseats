package llm

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/metcalfc/popcorn/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

// exe.dev's integrations proxy the API and add the key, so the VM holds none.
func TestAnthropicThroughAKeyAddingProxy(t *testing.T) {
	var path, key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, key = r.URL.Path, r.Header.Get("x-api-key")
		json.NewEncoder(w).Encode(map[string]any{"content": []any{}})
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(config.AnthropicBaseURLVar, "")
	if _, err := Anthropic(context.Background(), map[string]any{}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key and no proxy: %v", err)
	}
	if config.Missing(config.Anthropic) == "" {
		t.Error("no key and no proxy should be missing")
	}

	t.Setenv(config.AnthropicBaseURLVar, srv.URL+"/")
	if _, err := Anthropic(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/messages" || key != "" {
		t.Errorf("proxy got path %q key %q", path, key)
	}
	if m := config.Missing(config.Anthropic); m != "" {
		t.Errorf("a proxy is enough for the Anthropic API: %s", m)
	}
	if config.Missing(config.OpenAI) == "" {
		t.Error("the Anthropic proxy must not stand in for OPENAI_API_KEY")
	}

	t.Setenv("ANTHROPIC_API_KEY", "own-key")
	Anthropic(context.Background(), map[string]any{})
	if key != "own-key" {
		t.Errorf("a key that is set should still be sent: %q", key)
	}
}
