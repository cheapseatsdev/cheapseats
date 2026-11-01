package audit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"

	"github.com/cheapseatsdev/cheapseats/internal/redact"
	"github.com/cheapseatsdev/cheapseats/internal/version"
)

// MaxBody is the most of any response read: a page, robots.txt or a sitemap.
const MaxBody = 2 << 20

// UA is how Cheap Seats identifies itself: a Mozilla-style string, so a firewall treats it as it would
// a person's browser, that still says who it is.
func UA() string {
	return "Mozilla/5.0 (compatible; CheapSeats-Audit/" + version.String() + "; +https://cheapseats.dev)"
}

// Token is Cheap Seats' robots.txt product token.
const Token = "CheapSeats-Audit"

// fetchSlots caps the requests in flight across every Fetcher in the process: each audit and each
// citation check makes its own Fetcher of four, and several can run at once (for different runs,
// from the API, MCP and the CLI), so without it the total had no bound.
var fetchSlots = make(chan struct{}, 16)

// eachWorkers is the most goroutines each runs at once: enough to keep the fetch slots busy while
// some of them read the day's cache or wait on robots.txt.
const eachWorkers = 8

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
	Cut    bool   // the body was longer than MaxBody, or ended before the length it declared
}

func (r *Response) ContentType() string {
	if r == nil {
		return ""
	}
	return strings.ToLower(r.Header.Get("content-type"))
}

// Get fetches a URL as Cheap Seats, or as the user agent given.
func (f *Fetcher) Get(ctx context.Context, url string, ua ...string) (*Response, error) {
	select {
	case f.limit <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-f.limit }()
	select {
	case fetchSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-fetchSlots }()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("not an http or https URL: %s", redact.URL(url))
	}
	agent := f.UA
	if len(ua) > 0 && ua[0] != "" {
		agent = ua[0]
	}
	req.Header.Set("user-agent", agent)
	req.Header.Set("accept", "text/html,application/xhtml+xml,text/plain,application/xml;q=0.9,*/*;q=0.8")
	resp, err := f.Client.Do(req)
	if err != nil {
		// A fetch error names the URL (never the body), and a URL's query or user info can carry
		// a token: redact.URL drops them, so the error is safe wherever it is shown or stored.
		var ue *neturl.Error
		if errors.As(err, &ue) {
			ue.URL = redact.URL(ue.URL)
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: body, URL: resp.Request.URL.String()}
	// The connection closed before the body's declared length (or its last chunk): what came
	// is kept, as before, and marked cut, since the page goes on past it.
	out.Cut = err != nil
	if len(body) > MaxBody {
		out.Body, out.Cut = body[:MaxBody], true
	}
	return out, nil
}

// each runs f for every index, on at most eachWorkers goroutines; the Fetcher and fetchSlots limit
// how many requests are in flight.
func each(n int, f func(i int)) {
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(n, eachWorkers); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				f(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
}
