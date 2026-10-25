// Package typesafe calls TypeSafe's System One API: Jev, a model that returns typed judgments
// (a yes/no probability, a choice among options, a score on described levels) rather than text.
// Cheap Seats uses it for small decisions over data it already has, such as whether two product
// names the grader wrote are the same product. It is optional: with no [typesafe] in the config,
// or no TYPESAFE_API_KEY, nothing here is called.
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/bounded"
	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/config"
)

const (
	KeyVar     = "TYPESAFE_API_KEY"
	DefaultURL = "https://api.typesafe.ai/v1/systemone"
)

var ErrNoKey = errors.New(KeyVar + " is not set")

// maxResponse is the largest successful response read. Typed answers to a few questions are a
// few KB; past this the reply is an error, not a retry.
var maxResponse int64 = 4 << 20

// Question is one judgment to ask about the state. Type is noul (yes/no), choice or score.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions,omitempty"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer is the typed answer to one question. Noul is the probability of yes; Choice the
// selected option; Score the probability-weighted level; Confidence summarises how concentrated
// the probabilities are (choice and score only).
type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// Chosen is the probability Jev gave the option it chose: for a noul, the probability of yes;
// for a choice or score, the chosen option's probability, or, when the response has none, its
// confidence. Decisions are gated on this, never on Confidence, which measures how concentrated
// the whole distribution is and shrinks with every extra option (a 0.77 on a two-option question
// reads 0.54).
func Chosen(a Answer) float64 {
	if a.Type == "noul" {
		return a.Noul
	}
	if p, ok := a.Probabilities[a.Choice]; ok {
		return p
	}
	return a.Confidence
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
	// What the call cost by the configured price for the model, for the caller's records.
	CostUSD float64 `json:"-"`
}

// Client asks Jev. Budget, when set, reserves and settles each call against the daily budget,
// which also needs a [pricing] entry for Model (input per million tokens; output is free).
type Client struct {
	Model  string
	URL    string
	Budget *budget.Controller
	Cfg    *config.Config
	HTTP   *http.Client
}

// Ask evaluates every question against the state in one call. State is a string, an object or
// an array of text; questions can reference its fields by name in backticks.
func (c *Client) Ask(ctx context.Context, operation string, state any, questions map[string]Question) (*Response, error) {
	if len(questions) == 0 {
		return nil, errors.New("typesafe: no questions")
	}
	model, url := c.Model, c.URL
	if model == "" {
		model = "jev-latest"
	}
	if url == "" {
		url = DefaultURL
		if base := config.ProxyBaseURL(config.TypeSafeBaseURLVar); base != "" {
			url = base + "/v1/systemone"
		}
	}
	// Through a key-adding proxy the key is optional.
	key := os.Getenv(KeyVar)
	if key == "" && url == DefaultURL {
		return nil, ErrNoKey
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	payload, err := json.Marshal(map[string]any{"model": model, "state": state, "questions": questions})
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		var lease *budget.Lease
		if c.Budget != nil {
			if p, ok := c.Cfg.Pricing[model]; !ok || p.Input <= 0 {
				return nil, fmt.Errorf("model %q has no positive configured pricing", model)
			}
			lease, err = c.Budget.Reserve(ctx, "provider-attempt/"+operation, model, c.Budget.Allowance())
			if err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			settle(lease, 0, true)
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		if key != "" {
			req.Header.Set("authorization", "Bearer "+key)
		}
		resp, err := hc.Do(req)
		if err != nil {
			settle(lease, 0, false)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = err
		} else {
			if resp.StatusCode == http.StatusOK {
				data, err := bounded.Read(resp.Body, maxResponse)
				resp.Body.Close()
				if err != nil {
					settle(lease, 0, false)
					return nil, fmt.Errorf("typesafe: reading response: %w", err)
				}
				var out Response
				if err := json.Unmarshal(data, &out); err != nil {
					settle(lease, 0, false)
					return nil, fmt.Errorf("typesafe: decoding response: %w", err)
				}
				if c.Cfg != nil {
					out.CostUSD = c.Cfg.Cost(model, config.Usage{Input: out.Usage.InputTokens, Output: out.Usage.OutputTokens})
				}
				if err := settle(lease, out.CostUSD, true); err != nil {
					return &out, err
				}
				return &out, nil
			}
			msg := bounded.Prefix(resp.Body)
			resp.Body.Close()
			settle(lease, 0, resp.StatusCode < 500)
			last = &APIError{Status: resp.StatusCode, Body: msg}
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != 529 {
				return nil, last
			}
			if s, err := strconv.Atoi(resp.Header.Get("retry-after")); err == nil && s > 0 && s < 120 {
				if err := sleep(ctx, time.Duration(s)*time.Second); err != nil {
					return nil, err
				}
				continue
			}
		}
		if err := sleep(ctx, time.Duration(1<<attempt)*2*time.Second); err != nil {
			return nil, err
		}
	}
	return nil, last
}

func settle(l *budget.Lease, amount float64, known bool) error {
	if l == nil {
		return nil
	}
	return l.Finish(amount, known)
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

type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("typesafe: HTTP %d: %s", e.Status, b)
}
