package typesafe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

func TestAskSendsStateAndQuestionsAndPricesTheAnswer(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("authorization")
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "jev-1.14.0",
			"answers": map[string]any{"q": map[string]any{"type": "choice", "choice": "c0", "confidence": 0.9, "probabilities": map[string]float64{"c0": 0.95, "none": 0.05}}},
			"usage":   map[string]any{"input_tokens": 1000000, "output_tokens": 20},
		})
	}))
	defer srv.Close()
	t.Setenv(KeyVar, "k")
	cfg := &config.Config{Pricing: map[string]config.Pricing{"jev-1.14.0": {Input: 0.042}}}
	c := &Client{Model: "jev-1.14.0", URL: srv.URL, Cfg: cfg}
	resp, err := c.Ask(context.Background(), "test", map[string]any{"a": "x"}, map[string]Question{"q": {Type: "choice", Instructions: "which?", Criteria: map[string]string{"c0": "first", "none": "none"}}})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer k" {
		t.Errorf("auth header %q", auth)
	}
	if got["model"] != "jev-1.14.0" || got["state"].(map[string]any)["a"] != "x" {
		t.Errorf("request %v", got)
	}
	if q := got["questions"].(map[string]any)["q"].(map[string]any); q["type"] != "choice" || q["criteria"] == nil {
		t.Errorf("question %v", q)
	}
	a := resp.Answers["q"]
	if a.Choice != "c0" || a.Confidence != 0.9 || a.Probabilities["c0"] != 0.95 {
		t.Errorf("answer %+v", a)
	}
	// A million input tokens at $0.042 per million; output is free.
	if resp.CostUSD < 0.0419 || resp.CostUSD > 0.0421 {
		t.Errorf("cost %f", resp.CostUSD)
	}
}

func TestAskNeedsAKeyUnlessAProxyAddsIt(t *testing.T) {
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("authorization"), r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": map[string]any{"q": map[string]any{"type": "noul", "noul": 0.7}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 1}})
	}))
	defer srv.Close()
	t.Setenv(KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, "")
	c := &Client{}
	if _, err := c.Ask(context.Background(), "t", "s", map[string]Question{"q": {Type: "noul", Instructions: "?"}}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key and no proxy: %v", err)
	}
	if config.Missing(config.TypeSafeProvider) == "" {
		t.Error("should be missing")
	}
	t.Setenv(config.TypeSafeBaseURLVar, srv.URL+"/")
	resp, err := c.Ask(context.Background(), "t", "s", map[string]Question{"q": {Type: "noul", Instructions: "?"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/systemone" || auth != "" {
		t.Errorf("proxy got path %q auth %q", path, auth)
	}
	if resp.Answers["q"].Noul != 0.7 {
		t.Errorf("noul %v", resp.Answers["q"])
	}
	if m := config.Missing(config.TypeSafeProvider); m != "" {
		t.Errorf("a proxy is enough: %s", m)
	}
	if config.Missing(config.ExaProvider) == "" {
		t.Error("the TypeSafe proxy must not stand in for EXA_API_KEY")
	}
}

func TestAskGivesUpOnAClientError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"detail":"bad"}`, 422)
	}))
	defer srv.Close()
	t.Setenv(KeyVar, "k")
	c := &Client{URL: srv.URL}
	_, err := c.Ask(context.Background(), "t", "s", map[string]Question{"q": {Type: "noul", Instructions: "?"}})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 422 || calls != 1 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}

func TestChosenIsTheChosenOptionsProbability(t *testing.T) {
	if p := Chosen(Answer{Type: "noul", Noul: 0.8}); p != 0.8 {
		t.Errorf("noul: %f", p)
	}
	if p := Chosen(Answer{Type: "choice", Choice: "c1", Confidence: 0.54, Probabilities: map[string]float64{"c0": 0.23, "c1": 0.77}}); p != 0.77 {
		t.Errorf("choice: the chosen option's probability, not the confidence statistic: %f", p)
	}
	if p := Chosen(Answer{Type: "choice", Choice: "c1", Confidence: 0.54}); p != 0.54 {
		t.Errorf("with no probabilities, the confidence: %f", p)
	}
}
