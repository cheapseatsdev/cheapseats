package audit

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReadCurrent supplies current page evidence to the adviser through the same guarded
// fetcher, robots policy and optional rendered reader as citation checks.
// An unreadable page is unknown, never evidence that content does not exist. cut says the text
// may stop short of the page (a response or its text passed the read limits), so what it lacks
// may be on the page.
//
// It always fetches fresh, ignoring o.DB: the day cache (page_fetches) doesn't record whether a
// reading was cut, and recording it would be a stored-format change (docs/analysis.md, "Why
// verification reads pages fresh"). A Reader's own cache keeps text cut at maxText, so its cut
// is still seen.
func ReadCurrent(ctx context.Context, u string, o CiteOptions) (text string, cut bool, err error) {
	o.DB = nil
	if o.Fetcher == nil {
		o.Fetcher = NewFetcher(nil, 4)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	day := o.Now().UTC().Format("2006-01-02")
	robots := &robotsCache{f: o.Fetcher, by: map[string]*robotsEntry{}}
	status, skipped, text, via, cut := fetchText(ctx, o, robots, u, day)
	if skipped != "" {
		return "", false, fmt.Errorf("current page unavailable: %s", skipped)
	}
	if status != 200 && via != ViaExa || len(strings.Fields(text)) < 20 {
		return "", false, fmt.Errorf("current page has insufficient readable evidence (HTTP %d)", status)
	}
	return text, cut, nil
}
