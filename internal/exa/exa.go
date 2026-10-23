// Package exa calls Exa's search API, a neutral web index that Cheap Seats can ask, after a run,
// which of the brand's own pages answer each buyer question. It is optional: with no [exa] in
// the config, or no EXA_API_KEY, nothing here is called.
package exa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/config"
)

const (
	KeyVar     = "EXA_API_KEY"
	DefaultURL = "https://api.exa.ai/search"
	// Model is the name a search is priced and budgeted under: [pricing.exa-search] search_per_1k.
	Model = "exa-search"
)

var ErrNoKey = errors.New(KeyVar + " is not set")

type Result struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	PublishedDate string `json:"publishedDate,omitempty"`
}

type Response struct {
	RequestID string   `json:"requestId"`
	Results   []Result `json:"results"`
	Cost      struct {
		Total float64 `json:"total"`
	} `json:"costDollars"`
	// What the search cost: Exa's own estimate when it gives one, else the configured price.
	CostUSD float64 `json:"-"`
}

// Client searches Exa. Budget, when set, reserves and settles each search against the daily
// budget under Model, which needs a [pricing.exa-search] entry with search_per_1k.
type Client struct {
	URL    string
	Budget *budget.Controller
	Cfg    *config.Config
	HTTP   *http.Client
}

// Search runs one query, limited to the given domains or domain/path prefixes when any are
// given, and returns up to n results with no page contents (the cheapest call).
func (c *Client) Search(ctx context.Context, operation, query string, domains []string, n int) (*Response, error) {
	if n <= 0 {
		n = 3
	}
	url := c.URL
	if url == "" {
		url = DefaultURL
		if base := config.ProxyBaseURL(config.ExaBaseURLVar); base != "" {
			url = base + "/search"
		}
	}
	// Through a key-adding proxy the key is optional.
	key := os.Getenv(KeyVar)
	if key == "" && url == DefaultURL {
		return nil, ErrNoKey
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	body := map[string]any{"query": query, "type": "auto", "numResults": n}
	if len(domains) > 0 {
		body["includeDomains"] = domains
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		var lease *budget.Lease
		if c.Budget != nil {
			if p, ok := c.Cfg.Pricing[Model]; !ok || p.SearchPer1K <= 0 {
				return nil, fmt.Errorf("model %q has no positive configured pricing", Model)
			}
			lease, err = c.Budget.Reserve(ctx, "provider-attempt/"+operation, Model, c.Budget.Allowance())
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
			req.Header.Set("x-api-key", key)
		}
		resp, err := hc.Do(req)
		if err != nil {
			settle(lease, 0, false)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = err
		} else {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var out Response
				if err := json.Unmarshal(data, &out); err != nil {
					settle(lease, 0, false)
					return nil, fmt.Errorf("exa: decoding response: %w", err)
				}
				out.CostUSD = out.Cost.Total
				if out.CostUSD <= 0 && c.Cfg != nil {
					out.CostUSD = c.Cfg.Cost(Model, config.Usage{Searches: 1})
				}
				if err := settle(lease, out.CostUSD, true); err != nil {
					return &out, err
				}
				return &out, nil
			}
			settle(lease, 0, resp.StatusCode < 500)
			last = &APIError{Status: resp.StatusCode, Body: string(data)}
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
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
	return fmt.Sprintf("exa: HTTP %d: %s", e.Status, b)
}
