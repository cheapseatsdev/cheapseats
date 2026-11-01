package audit

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Landing is what a fix's target page on a brand site says, read through the citation check's
// fetcher and day cache: whether it names the brand, and which of the fix's claim terms it has,
// whole-word (NamesAny). It is evidence beside the fix's "done when", never a grade: it counts
// toward no outcome.
type Landing struct {
	URL string `json:"url"`
	Day string `json:"day"` // the day the page was read (its page_fetches row)
	// Why it couldn't be read (robots.txt, an error, not text); when set, nothing else is said.
	Skipped    string   `json:"skipped,omitempty"`
	NamesBrand bool     `json:"names_brand"`
	Has        []string `json:"has"`     // the claim's terms the page has
	Missing    []string `json:"missing"` // and those it doesn't
}

// Landed is whether the page was read, names the brand and has every claim term.
func (l Landing) Landed() bool { return l.Skipped == "" && l.NamesBrand && len(l.Missing) == 0 }

// CheckLanding reads a page as the citation check does (robots.txt, the 200 KB cap, and the
// page_fetches day cache when o.DB is set, so a second read the same day fetches nothing) and
// reports whether it names the brand and has each term.
func CheckLanding(ctx context.Context, u string, brand, terms []string, o CiteOptions) Landing {
	if o.Fetcher == nil {
		o.Fetcher = NewFetcher(nil, 4)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	day := o.Now().UTC().Format("2006-01-02")
	robots := &robotsCache{f: o.Fetcher, by: map[string]*robotsEntry{}}
	_, skipped, text, _, _ := fetchText(ctx, o, robots, u, day)
	return reading(u, day, skipped, text, brand, terms)
}

// CachedLanding is the newest reading of a page in the day cache (page_fetches keeps 30 days),
// or nil when it has none; fresh says whether that reading is from day.
func CachedLanding(ctx context.Context, db *sql.DB, u, day string, brand, terms []string) (l *Landing, fresh bool) {
	var at, skipped, text string
	if db.QueryRowContext(ctx, `SELECT day, error, text FROM page_fetches WHERE url=? ORDER BY day DESC LIMIT 1`, u).Scan(&at, &skipped, &text) != nil {
		return nil, false
	}
	if skipped == shellReason {
		skipped = "" // the little text it has, as fetchText reads it without a Reader
	}
	r := reading(u, at, skipped, text, brand, terms)
	return &r, at == day
}

func reading(u, day, skipped, text string, brand, terms []string) Landing {
	l := Landing{URL: u, Day: day, Skipped: skipped, Has: []string{}, Missing: []string{}}
	if skipped != "" {
		return l
	}
	l.NamesBrand = NamesAny(text, brand)
	for _, t := range terms {
		if NamesAny(text, []string{t}) {
			l.Has = append(l.Has, t)
		} else {
			l.Missing = append(l.Missing, t)
		}
	}
	return l
}

var quoted = regexp.MustCompile(`"([^"]+)"|“([^”]+)”`)

// claimStop is the common words left out of a claim's terms.
var claimStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`about after also always among and any are because been before being both but can could does
		doing each every for from has have into its just like make makes more most much must not now only other our out over own
		same should some such than that the their them then there these they this those through too under until very was were
		what when where whether which while who whom why will with within without would you your yours`) {
		claimStop[w] = true
	}
}

// ClaimTerms is what a page must contain to carry a fix's claim: the claim's quoted phrases and
// its numbers (any word with a digit: "$49", "99.9%", "30-day"), when it has any; otherwise its
// words of four letters or more that aren't common ones. Lower case, each once, at most 12.
func ClaimTerms(claim string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !seen[t] && len(out) < 12 {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, m := range quoted.FindAllStringSubmatch(claim, -1) {
		add(m[1] + m[2])
	}
	edge := func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("$%€£", r)
	}
	for _, w := range strings.Fields(quoted.ReplaceAllString(claim, " ")) {
		if w = strings.TrimFunc(w, edge); strings.IndexFunc(w, unicode.IsDigit) >= 0 {
			add(w)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, w := range strings.FieldsFunc(claim, func(r rune) bool { return !unicode.IsLetter(r) && r != '-' }) {
		if w = strings.Trim(strings.ToLower(w), "-"); len([]rune(w)) >= 4 && !claimStop[w] {
			add(w)
		}
	}
	return out
}
