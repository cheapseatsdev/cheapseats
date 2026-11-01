package audit

import (
	"context"
	"database/sql"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// Target is a third-party page answers drew on, from a run's report.
type Target struct {
	URL      string `json:"url"`
	Cited    int    `json:"cited"`    // answers that linked it
	Surfaced int    `json:"surfaced"` // answers whose searches returned it
}

// CitedPage is what a cited page says: whether it names the brand, and which competitors.
type CitedPage struct {
	Target
	Status      int      `json:"status"`
	Skipped     string   `json:"skipped,omitempty"` // why it wasn't read: robots.txt, an error, not text
	Via         string   `json:"via,omitempty"`     // "exa" when the text was read through Exa, plain HTTP having got an empty shell or a challenge
	NamesBrand  bool     `json:"names_brand"`
	Competitors []string `json:"competitors"`
	// It names competitors but not the brand: the page to get into.
	Outreach bool `json:"outreach"`
}

// CitationCheck is one run's check of the pages its answers cited.
type CitationCheck struct {
	RunID string      `json:"run_id"`
	At    string      `json:"at"`
	Pages []CitedPage `json:"pages"`
}

// Outreach is how many pages name competitors but not the brand.
func (c CitationCheck) Outreach() int {
	n := 0
	for _, p := range c.Pages {
		if p.Outreach {
			n++
		}
	}
	return n
}

type CiteOptions struct {
	Fetcher *Fetcher
	// Where fetched text is kept a day (page_fetches), so a second check the same day fetches
	// nothing; nil fetches every time.
	DB  *sql.DB
	Now func() time.Time
	// Reads a page's text another way when plain HTTP got an app shell that is empty without
	// JavaScript, or a bot challenge; nil reads nothing more, and what plain HTTP got is the result.
	Reader Reader
}

// maxText is the most of a page's text kept for matching.
const maxText = 200_000

// CheckCitations fetches each page (as Cheap Seats, when its robots.txt allows Cheap Seats) and reports
// whether it names the brand (by name or alias) and which competitors.
func CheckCitations(ctx context.Context, targets []Target, brand []string, competitors []sets.Competitor, o CiteOptions) CitationCheck {
	if o.Fetcher == nil {
		o.Fetcher = NewFetcher(nil, 4)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	now := o.Now().UTC()
	day := now.Format("2006-01-02")
	out := CitationCheck{At: now.Format(time.RFC3339), Pages: make([]CitedPage, len(targets))}
	robots := &robotsCache{f: o.Fetcher, by: map[string]*robotsEntry{}}
	if o.DB != nil {
		o.DB.ExecContext(ctx, `DELETE FROM page_fetches WHERE day < ?`, now.AddDate(0, 0, -30).Format("2006-01-02"))
	}
	each(len(targets), func(i int) {
		t := targets[i]
		p := CitedPage{Target: t, Competitors: []string{}}
		status, skipped, text, via, _ := fetchText(ctx, o, robots, t.URL, day)
		p.Status, p.Skipped, p.Via = status, skipped, via
		if skipped == "" {
			p.NamesBrand = NamesAny(text, brand)
			for _, c := range competitors {
				if NamesAny(text, append([]string{c.Name}, c.Aliases...)) {
					p.Competitors = append(p.Competitors, c.Name)
				}
			}
			p.Outreach = !p.NamesBrand && len(p.Competitors) > 0
		}
		out.Pages[i] = p
	})
	return out
}

// What plain HTTP got instead of the page, as stored in page_fetches' error column, and what a
// Reader can read another way. A challenge is skipped, as it always was. A shell (a 200 HTML page
// that is empty without JavaScript) was never skipped: with no Reader it reads as the little text it
// has, naming nobody, exactly as before the fallback; the reason is kept in the row so a later
// check the same day knows to read it through the Reader without fetching it again.
const (
	challengeReason = "it served a bot challenge"
	shellReason     = "empty without JavaScript"
)

// fetchText is a page's visible text, from today's cache or fetched; skipped says why there's none.
// A shell or a challenge is read through o.Reader when there is one, and via says so ("exa").
// cut says the text may stop short of the page: the response passed MaxBody or ended early, the
// text passed maxText, or the page is a shell that no Reader rendered (its text is what plain HTTP
// got, not the page). The day cache doesn't record it, so text read from the cache reads as cut.
func fetchText(ctx context.Context, o CiteOptions, robots *robotsCache, u, day string) (status int, skipped, text, via string, cut bool) {
	cached := false
	if o.DB != nil {
		cached = o.DB.QueryRowContext(ctx, `SELECT status, error, text FROM page_fetches WHERE url=? AND day=?`, u, day).Scan(&status, &skipped, &text) == nil
		cut = cached
	}
	if !cached {
		status, skipped, text, cut = fetchNow(ctx, o.Fetcher, robots, u)
		if o.DB != nil && ctx.Err() == nil {
			o.DB.ExecContext(ctx, `INSERT OR REPLACE INTO page_fetches (url, day, status, error, text, fetched_at) VALUES (?,?,?,?,?,?)`,
				u, day, status, skipped, text, time.Now().UTC().Format(time.RFC3339))
		}
	}
	switch skipped {
	case shellReason, challengeReason:
		if o.Reader != nil {
			if read, _, ok := o.Reader(ctx, u); ok {
				// A Reader's text may already have been cut to maxText (the app's is, before it is
				// cached), which leaves it within a rune of maxText bytes: that reads as cut too.
				return status, "", ClipText(read), ViaExa, len(read) > maxText-utf8.UTFMax
			}
		}
		if skipped == shellReason {
			skipped = "" // what plain HTTP got: a page with a little text, as before the fallback
			cut = true   // but not the rendered page, so it can't show what the page lacks
		}
	}
	return status, skipped, text, "", cut
}

func fetchNow(ctx context.Context, f *Fetcher, robots *robotsCache, raw string) (int, string, string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return 0, "not a web page", "", false
	}
	if !robots.allowed(ctx, u) {
		return 0, "robots.txt asks Cheap Seats not to fetch it", "", false
	}
	r, err := f.Get(ctx, raw)
	if err != nil {
		return 0, "couldn't fetch it: " + err.Error(), "", false
	}
	if r.Status != 200 {
		return r.Status, "it answered " + strconv.Itoa(r.Status), "", false
	}
	if challenge(r) {
		return r.Status, challengeReason, "", false
	}
	ct := r.ContentType()
	if ct != "" && !strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "html") && !strings.Contains(ct, "xml") {
		return r.Status, "not a text page (" + ct + ")", "", false
	}
	text, clipped := clipText(Text(r.Body, ct))
	cut := r.Cut || clipped
	if html := strings.Contains(ct, "html") || ct == "" && looksHTML(r.Body); html && emptyShell(r.Body, runes(text)) {
		return r.Status, shellReason, text, cut
	}
	return r.Status, "", text, cut
}

// ClipText makes text valid UTF-8 (a Latin-1 page's bytes become U+FFFD, which matching reads as
// a word break) and cuts it to maxText bytes, without splitting the last rune.
func ClipText(text string) string {
	text, _ = clipText(text)
	return text
}

// clipText is ClipText, and whether it cut anything.
func clipText(text string) (string, bool) {
	text = strings.ToValidUTF8(text, "\uFFFD")
	if len(text) <= maxText {
		return text, false
	}
	cut := maxText
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

// robotsCache fetches each host's robots.txt once per check.
type robotsCache struct {
	f  *Fetcher
	mu sync.Mutex
	by map[string]*robotsEntry
}

type robotsEntry struct {
	once sync.Once
	r    *Robots // nil when there's none, or it couldn't be read: allowed
	deny bool    // a server error there: crawlers fetch nothing
}

func (c *robotsCache) allowed(ctx context.Context, u *url.URL) bool {
	key := u.Scheme + "://" + u.Host
	c.mu.Lock()
	e := c.by[key]
	if e == nil {
		e = &robotsEntry{}
		c.by[key] = e
	}
	c.mu.Unlock()
	e.once.Do(func() {
		r, err := c.f.Get(ctx, key+"/robots.txt")
		switch {
		case err != nil:
		case r.Status >= 500:
			e.deny = true
		case r.Status == 200:
			e.r = ParseRobots(string(r.Body))
		}
	})
	if e.deny {
		return false
	}
	path := u.EscapedPath()
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return allowsUs(e.r, path)
}

// allowsUs is whether robots.txt lets Cheap Seats fetch path, by its token.
func allowsUs(r *Robots, path string) bool {
	ok, _ := r.Allowed(Token, path)
	return ok
}

// NamesAny reports whether text names any of the names, ignoring case, as a whole word: "Gauge"
// in "a gauge of interest" counts, "rl" in "world" doesn't. Stricter than the grader's check
// (grade.namesBrand, a bare substring, which only ever overrules the model one way): here a false
// match would hide an outreach target.
func NamesAny(text string, names []string) bool {
	l := strings.ToLower(text)
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		for from := 0; ; {
			i := strings.Index(l[from:], n)
			if i < 0 {
				break
			}
			i += from
			before, _ := utf8.DecodeLastRuneInString(l[:i])
			after, _ := utf8.DecodeRuneInString(l[i+len(n):])
			if !wordRune(before) && !wordRune(after) {
				return true
			}
			from = i + 1
		}
	}
	return false
}

func wordRune(r rune) bool {
	return r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r))
}
