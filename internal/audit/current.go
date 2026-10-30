package audit

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReadCurrent supplies current page evidence to the adviser through the same guarded
// fetcher, robots policy, daily cache and optional rendered reader as citation checks.
// An unreadable page is unknown, never evidence that content does not exist.
func ReadCurrent(ctx context.Context, u string, o CiteOptions) (string, error) {
	if o.Fetcher == nil {
		o.Fetcher = NewFetcher(nil, 4)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	day := o.Now().UTC().Format("2006-01-02")
	robots := &robotsCache{f: o.Fetcher, by: map[string]*robotsEntry{}}
	status, skipped, text, via := fetchText(ctx, o, robots, u, day)
	if skipped != "" {
		return "", fmt.Errorf("current page unavailable: %s", skipped)
	}
	if status != 200 && via != ViaExa || len(strings.Fields(text)) < 20 {
		return "", fmt.Errorf("current page has insufficient readable evidence (HTTP %d)", status)
	}
	return text, nil
}
