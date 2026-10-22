package audit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/metcalfc/popcorn/internal/version"
)

// MaxBody is the most of any response read: a page, robots.txt or a sitemap.
const MaxBody = 2 << 20

// UA is how Popcorn identifies itself: a Mozilla-style string, so a firewall treats it as it would
// a person's browser, that still says who it is.
func UA() string {
	return "Mozilla/5.0 (compatible; Popcorn-Audit/" + version.String() + "; +https://popcorn.zone)"
}

// Token is Popcorn's robots.txt product token.
const Token = "Popcorn-Audit"

// Fetcher makes polite requests: one user agent, a timeout, a size cap and a few at a time.
type Fetcher struct {
	Client *http.Client
	UA     string
	limit  chan struct{}
}

// NewFetcher allows n requests in flight at once (4 when n <= 0). A nil client is one that only
// reaches public addresses (guard.go); a client given is trusted as it is.
func NewFetcher(client *http.Client, n int) *Fetcher {
	if n <= 0 {
		n = 4
	}
	if client == nil {
		client = safeClient()
	}
	return &Fetcher{Client: client, UA: UA(), limit: make(chan struct{}, n)}
}

// Response is what came back, the body cut at MaxBody.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	URL    string // after redirects
	Cut    bool   // the body was longer than MaxBody
}

func (r *Response) ContentType() string {
	if r == nil {
		return ""
	}
	return strings.ToLower(r.Header.Get("content-type"))
}

// Get fetches a URL as Popcorn, or as the user agent given.
func (f *Fetcher) Get(ctx context.Context, url string, ua ...string) (*Response, error) {
	select {
	case f.limit <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-f.limit }()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("not an http or https URL: %s", url)
	}
	agent := f.UA
	if len(ua) > 0 && ua[0] != "" {
		agent = ua[0]
	}
	req.Header.Set("user-agent", agent)
	req.Header.Set("accept", "text/html,application/xhtml+xml,text/plain,application/xml;q=0.9,*/*;q=0.8")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: body, URL: resp.Request.URL.String()}
	if len(body) > MaxBody {
		out.Body, out.Cut = body[:MaxBody], true
	}
	return out, nil
}

// each runs f for every index, all at once: the Fetcher limits how many requests are in flight.
func each(n int, f func(i int)) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f(i)
		}()
	}
	wg.Wait()
}
