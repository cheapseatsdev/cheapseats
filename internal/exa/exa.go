// Package exa calls Exa's web API, a neutral web index that Cheap Seats can ask, after a run,
// which of the brand's own pages answer each buyer question, what the web as a whole returns for
// it, and what resembles the pages that shaped the answers. It is optional: with no [exa] in the
// config, or no EXA_API_KEY, nothing here is called.
package exa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/bounded"
	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/config"
)

const (
	KeyVar     = "EXA_API_KEY"
	DefaultURL = "https://api.exa.ai"
	// Model is the name a search (and a find-similar, which Exa prices the same) is priced and
	// budgeted under: [pricing.exa-search] search_per_1k.
	Model = "exa-search"
	// ContentsModel is the name a page's text is priced and budgeted under:
	// [pricing.exa-contents] page_per_1k.
	ContentsModel = "exa-contents"
)

var ErrNoKey = errors.New(KeyVar + " is not set")

// maxResponse is the largest successful response read. A contents reply carries whole pages'
// text, so it is the largest Exa sends; past this the reply is an error, not a retry.
var maxResponse int64 = 32 << 20

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
	// What the call cost: Exa's own estimate when it gives one, else the configured price.
	CostUSD float64 `json:"-"`
}

// Page is a page's text, from Contents.
type Page struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// ContentsResponse is what Contents returns: the pages Exa could read, and a status per URL asked.
type ContentsResponse struct {
	RequestID string `json:"requestId"`
	Results   []Page `json:"results"`
	Statuses  []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Tag            string `json:"tag"`
			HTTPStatusCode int    `json:"httpStatusCode"`
		} `json:"error,omitempty"`
	} `json:"statuses"`
	Cost struct {
		Total float64 `json:"total"`
	} `json:"costDollars"`
	CostUSD float64 `json:"-"`
}

// Client calls Exa. Budget, when set, reserves and settles each call against the daily budget
// under Model or ContentsModel, which need their [pricing] entries.
type Client struct {
	// URL is the API's base URL (DefaultURL, or a key-adding proxy); endpoints are paths under it.
	URL    string
	Budget *budget.Controller
	Cfg    *config.Config
	HTTP   *http.Client
}

// Query is one search: the text, the domains (or domain/path prefixes) to keep to or leave out,
// and how many results to return. Page contents are never asked for (they cost extra).
type Query struct {
	Text    string
	Include []string
	Exclude []string
	N       int
}

// Search runs one query, limited to the given domains or domain/path prefixes when any are
// given, and returns up to n results with no page contents (the cheapest call).
func (c *Client) Search(ctx context.Context, operation, query string, domains []string, n int) (*Response, error) {
	return c.Run(ctx, operation, Query{Text: query, Include: domains, N: n})
}

// Run is Search with every option.
func (c *Client) Run(ctx context.Context, operation string, q Query) (*Response, error) {
	if q.N <= 0 {
		q.N = 3
	}
	body := map[string]any{"query": q.Text, "type": "auto", "numResults": q.N}
	if len(q.Include) > 0 {
		body["includeDomains"] = q.Include
	}
	if len(q.Exclude) > 0 {
		body["excludeDomains"] = q.Exclude
	}
	var out Response
	lease, err := c.post(ctx, operation, "/search", Model, body, &out)
	if err != nil {
		return nil, err
	}
	out.CostUSD = c.price(out.Cost.Total, Model, config.Usage{Searches: 1})
	return &out, settle(lease, out.CostUSD, true)
}

// FindSimilar returns up to n pages that resemble the page at url, never from the page's own
// host, and never from the excluded domains. It is priced as a search.
func (c *Client) FindSimilar(ctx context.Context, operation, url string, exclude []string, n int) (*Response, error) {
	if n <= 0 {
		n = 5
	}
	body := map[string]any{"url": url, "numResults": n, "excludeSourceDomain": true}
	if len(exclude) > 0 {
		body["excludeDomains"] = exclude
	}
	var out Response
	lease, err := c.post(ctx, operation, "/findSimilar", Model, body, &out)
	if err != nil {
		return nil, err
	}
	out.CostUSD = c.price(out.Cost.Total, Model, config.Usage{Searches: 1})
	return &out, settle(lease, out.CostUSD, true)
}

// Contents returns the text of each page Exa can read, priced per page under ContentsModel.
// Up to 100 URLs per call.
func (c *Client) Contents(ctx context.Context, operation string, urls []string) (*ContentsResponse, error) {
	if len(urls) == 0 {
		return nil, errors.New("exa: no urls")
	}
	body := map[string]any{"urls": urls, "text": true}
	var out ContentsResponse
	lease, err := c.post(ctx, operation, "/contents", ContentsModel, body, &out)
	if err != nil {
		return nil, err
	}
	out.CostUSD = c.price(out.Cost.Total, ContentsModel, config.Usage{Pages: int64(len(out.Results))})
	return &out, settle(lease, out.CostUSD, true)
}

// price is Exa's own estimate when it gives one, else the configured price for the usage.
func (c *Client) price(estimate float64, model string, u config.Usage) float64 {
	if estimate > 0 || c.Cfg == nil {
		return estimate
	}
	return c.Cfg.Cost(model, u)
}

func (c *Client) endpoint(path string) string {
	base := c.URL
	if base == "" {
		base = DefaultURL
		if p := config.ProxyBaseURL(config.ExaBaseURLVar); p != "" {
			base = p
		}
	}
	// A URL that already names the search endpoint (the shape the first release used) still works.
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/search")
	return base + path
}

// post sends one request, reserving against the budget under model and retrying rate limits
// and server errors. On success it returns the open lease for the caller to settle, since the
// cost is known only after the response is read into its own type.
func (c *Client) post(ctx context.Context, operation, path, model string, body any, out any) (*budget.Lease, error) {
	url := c.endpoint(path)
	// Through a key-adding proxy the key is optional.
	key := os.Getenv(KeyVar)
	if key == "" && strings.HasPrefix(url, DefaultURL) {
		return nil, ErrNoKey
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		var lease *budget.Lease
		if c.Budget != nil {
			if !priced(c.Cfg, model) {
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
			if resp.StatusCode == http.StatusOK {
				data, err := bounded.Read(resp.Body, maxResponse)
				resp.Body.Close()
				if err != nil {
					settle(lease, 0, false)
					return nil, fmt.Errorf("exa: reading response: %w", err)
				}
				if err := json.Unmarshal(data, out); err != nil {
					settle(lease, 0, false)
					return nil, fmt.Errorf("exa: decoding response: %w", err)
				}
				return lease, nil
			}
			msg := bounded.Prefix(resp.Body)
			resp.Body.Close()
			settle(lease, 0, resp.StatusCode < 500)
			last = &APIError{Status: resp.StatusCode, Body: msg}
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

func priced(cfg *config.Config, model string) bool {
	if cfg == nil {
		return false
	}
	p, ok := cfg.Pricing[model]
	if !ok {
		return false
	}
	switch model {
	case ContentsModel:
		return p.PagePer1K > 0
	}
	return p.SearchPer1K > 0
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
