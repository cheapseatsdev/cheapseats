package crawl

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/bots"
)

// Event is a hit worth counting: an AI crawler's fetch of a page, a visit an assistant referred, or
// another person's visit to a page.
type Event struct {
	Day   string // UTC
	Host  string
	Path  string
	Class bots.Class
	Bot   string // the crawler's token, or the referring assistant; empty for a visit
	Error bool   // answered 4xx or 5xx
	Hash  string // identifies the request, so a log imported twice counts once
}

// ErrorPath is the one path every request answered 4xx is counted under, per host, day, class and
// bot. Anyone can send a crawler's user agent for any path, and a 404 for each made-up one would
// otherwise be a row of its own: client errors are counted, but not by page. A 5xx stays on its
// page, since a server error there is a finding about that page.
const ErrorPath = "/(errors)"

// assets are file types that aren't pages: a crawler fetching them says nothing about a page.
var assets = map[string]bool{".css": true, ".js": true, ".mjs": true, ".map": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".avif": true, ".svg": true, ".ico": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp4": true, ".webm": true, ".mp3": true, ".json": true, ".wasm": true}

// visitPages are the file types a person's visit can be to: a page's path ends in one of these or
// has none. A browser fetching robots.txt, a feed or a download isn't visiting a page.
var visitPages = map[string]bool{"": true, ".html": true, ".htm": true, ".shtml": true, ".php": true, ".asp": true, ".aspx": true, ".jsp": true, ".cfm": true}

// Classify says what a hit was: an AI crawler (by its user agent), a person an assistant sent (by
// the Referer or a utm_source naming one), another person's visit to a page (by a browser's user
// agent), or none of these. Host is used when the log line has none.
func Classify(h Hit, host string) (Event, bool) {
	th, p, q := split(h.Target)
	if h.Host != "" {
		host = h.Host
	} else if th != "" {
		host = th
	}
	host = NormHost(host)
	if assets[strings.ToLower(path.Ext(p))] {
		return Event{}, false
	}
	e := Event{Day: h.Time.UTC().Format("2006-01-02"), Host: host, Path: NormPath(p), Error: h.Status >= 400}
	if h.Status >= 400 && h.Status < 500 {
		e.Path = ErrorPath
	}
	if b, ok := bots.Match(h.UA); ok {
		e.Class, e.Bot = b.Class, b.Token
	} else if name, ok := referrer(h.Referer, q); ok {
		e.Class, e.Bot = bots.Referral, name
	} else if bots.Browser(h.UA) && visitPages[strings.ToLower(path.Ext(p))] && (h.Status < 300 || h.Status == 304 || h.Status >= 400) {
		// A redirect isn't a visit: the page it leads to is counted when the browser gets there.
		e.Class = bots.Visit
	} else {
		return Event{}, false
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{h.Time.UTC().Format(time.RFC3339Nano), h.IP, host, h.Target, h.UA}, "\x00")))
	e.Hash = hex.EncodeToString(sum[:16])
	return e, true
}

// referrer is the assistant a visit came from: the Referer's host, or utm_source.
func referrer(ref string, q url.Values) (string, bool) {
	if ref != "" {
		if u, err := url.Parse(ref); err == nil {
			if n, ok := bots.ReferrerOf(u.Hostname()); ok {
				return n, true
			}
		}
	}
	return bots.ReferrerOf(q.Get("utm_source"))
}

// NormHost is a host lowercased, without its port or a leading www.
func NormHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if x, _, err := net.SplitHostPort(h); err == nil {
		h = x
	}
	return strings.TrimPrefix(h, "www.")
}

// NormPath is a path without its trailing slash, "/" for the root.
func NormPath(p string) string {
	p = strings.TrimSuffix(p, "/")
	if p == "" || p[0] != '/' {
		p = "/" + p
	}
	return p
}

// Key is a URL's page, as counts are kept: its host and path.
func Key(raw string) (host, p string) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ""
	}
	return NormHost(u.Host), NormPath(u.Path)
}

// Stats is what an import found.
type Stats struct {
	Lines     int                `json:"lines"`
	Unread    int                `json:"unread"`     // in no format Parse knows, or too long to be a request
	Counted   int                `json:"counted"`    // new AI crawler fetches, referred visits and other visits
	Duplicate int                `json:"duplicate"`  // counted by an earlier import
	TooOld    int                `json:"too_old"`    // from more than keepDays ago: not counted
	Future    int                `json:"future"`     // dated after tomorrow (UTC): not counted
	OverLimit int                `json:"over_limit"` // past maxEvents new ones in this import: not counted; import the log again for the rest
	ByClass   map[bots.Class]int `json:"by_class"`
	NoHost    bool               `json:"no_host"` // lines had no host, and none was given
}

// Import counts a log's AI crawler fetches, referred visits and other visits into crawl_daily, each request
// once however often the log is imported. Host is the site for lines that don't say (common and
// combined logs).
func Import(ctx context.Context, db *sql.DB, r io.Reader, host string) (Stats, error) {
	st := Stats{ByClass: map[bots.Class]int{}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()
	seen, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO crawl_seen (hash, day) VALUES (?, ?)`)
	if err != nil {
		return st, err
	}
	add, err := tx.PrepareContext(ctx, `INSERT INTO crawl_daily (day, host, path, class, bot, hits, errors) VALUES (?,?,?,?,?,1,?)
		ON CONFLICT (day, host, path, class, bot) DO UPDATE SET hits = hits + 1, errors = errors + excluded.errors`)
	if err != nil {
		return st, err
	}
	// Hashes are kept as long as a log line counts: older lines are left out, so a log from
	// before then imported again can't count twice. The counts are kept as long, too. A line
	// dated after tomorrow is a wrong clock or a forged line, and is left out as well.
	now := time.Now().UTC()
	horizon := now.AddDate(0, 0, -keepDays).Format("2006-01-02")
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	for _, table := range []string{"crawl_seen", "crawl_daily"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE day < ?`, horizon); err != nil {
			return st, err
		}
	}
	count := func(e Event) error {
		if e.Host == "" {
			st.NoHost = true
		}
		if st.Counted >= maxEvents {
			st.OverLimit++
			return nil
		}
		res, err := seen.ExecContext(ctx, e.Hash, e.Day)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			st.Duplicate++
			return nil
		}
		errs := 0
		if e.Error {
			errs = 1
		}
		if _, err := add.ExecContext(ctx, e.Day, e.Host, e.Path, string(e.Class), e.Bot, errs); err != nil {
			return err
		}
		st.Counted++
		st.ByClass[e.Class]++
		return nil
	}
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, long, err := readLine(br)
		if err != nil && err != io.EOF {
			return st, err
		}
		if strings.TrimSpace(line) != "" || long {
			st.Lines++
			if h, ok := Parse(line); long || !ok {
				st.Unread++ // a line too long to be a request, or in no format Parse knows
			} else if e, ok := Classify(h, host); ok && e.Day < horizon {
				st.TooOld++
			} else if ok && e.Day > tomorrow {
				st.Future++
			} else if ok {
				if err := count(e); err != nil {
					return st, err
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	if st.Lines > 0 && st.Unread == st.Lines {
		return st, fmt.Errorf("no line is in a format Cheap Seats reads: %s", Formats)
	}
	return st, tx.Commit()
}

// keepDays is how far back an import counts lines, and keeps their hashes and the daily counts.
const keepDays = 400

// maxEvents is the most new requests one import counts. Duplicates don't count toward it, so
// importing the same log again carries on where this one stopped.
var maxEvents = 1_000_000

// maxLine is the longest log line read; a longer one is skipped, not the whole import.
const maxLine = 1 << 20

// readLine reads one line, without its newline. Long reports a line over maxLine, whose text is
// dropped (the rest of it is read and discarded).
func readLine(br *bufio.Reader) (line string, long bool, err error) {
	var b []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if !long {
			b = append(b, chunk...)
			if len(b) > maxLine {
				long, b = true, nil
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return strings.TrimRight(string(b), "\r\n"), long, err
	}
}

// Use is how answers drew on one of the brand's pages, from the latest runs.
type Use struct{ Cited, Surfaced int }

// PageRow is one page: its fetches by class, visits and referred visits in the window, and how the
// latest runs used it.
type PageRow struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	Assistant int    `json:"assistant"`
	Search    int    `json:"search"`
	Training  int    `json:"training"`
	Engine    int    `json:"engine"`
	Referrals int    `json:"referrals"`
	// Visits is people's visits, the AI referrals among them. Zero everywhere when the window has
	// no visits counted but referrals (imported by a release that didn't count visits, or
	// removed): referrals alone aren't every visit.
	Visits int    `json:"visits"`
	Errors int    `json:"errors"` // crawler fetches answered 4xx or 5xx
	Last   string `json:"last_fetched,omitempty"`
	Use
	// crawled-not-cited: AI crawlers fetched it, but no answer in the latest runs cited it.
	// cited-not-crawled: answers cite it, but no assistant or search crawler fetched it in the
	// window (a host in the logs only).
	Flag string `json:"flag,omitempty"`
}

// BotRow is one crawler's or assistant's total in the window.
type BotRow struct {
	Class bots.Class `json:"class"`
	Bot   string     `json:"bot"`
	Hits  int        `json:"hits"`
	Pages int        `json:"pages"`
}

type Report struct {
	Since  string    `json:"since"`
	First  string    `json:"first_day,omitempty"` // the logs' first and last days in the window
	Last   string    `json:"last_day,omitempty"`
	Hosts  []string  `json:"hosts"`
	Bots   []BotRow  `json:"bots"`
	Pages  []PageRow `json:"pages"`
	Totals PageRow   `json:"totals"`
}

// Build reports the last days of counts, page by page, joined to how the latest runs used the
// brand's pages (cited, keyed by Key(url) as host+path).
func Build(ctx context.Context, db *sql.DB, days int, cited map[[2]string]Use) (Report, error) {
	since := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	rep := Report{Since: since, Hosts: []string{}, Bots: []BotRow{}, Pages: []PageRow{}}
	rows, err := db.QueryContext(ctx, `SELECT day, host, path, class, bot, hits, errors FROM crawl_daily WHERE day >= ?`, since)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	pages := map[[2]string]*PageRow{}
	type bk struct{ class, bot string }
	byBot := map[bk]*BotRow{}
	botPages := map[bk]map[[2]string]bool{}
	hosts := map[string]bool{}
	visits := false
	for rows.Next() {
		var day, host, p, class, bot string
		var hits, errs int
		if err := rows.Scan(&day, &host, &p, &class, &bot, &hits, &errs); err != nil {
			return rep, err
		}
		hosts[host] = true
		if rep.First == "" || day < rep.First {
			rep.First = day
		}
		if day > rep.Last {
			rep.Last = day
		}
		k := [2]string{host, p}
		row := pages[k]
		if row == nil {
			row = &PageRow{Host: host, Path: p}
			pages[k] = row
		}
		switch bots.Class(class) {
		case bots.Assistant:
			row.Assistant += hits
		case bots.Search:
			row.Search += hits
		case bots.Training:
			row.Training += hits
		case bots.Engine:
			row.Engine += hits
		case bots.Referral:
			row.Referrals += hits
			row.Visits += hits
		case bots.Visit:
			row.Visits += hits
			visits = true
		}
		if c := bots.Class(class); c != bots.Referral && c != bots.Visit {
			row.Errors += errs
			if day > row.Last {
				row.Last = day
			}
		}
		if bots.Class(class) == bots.Visit {
			continue // people, not a crawler: no row in Bots
		}
		b := bk{class, bot}
		if byBot[b] == nil {
			byBot[b] = &BotRow{Class: bots.Class(class), Bot: bot}
			botPages[b] = map[[2]string]bool{}
		}
		byBot[b].Hits += hits
		botPages[b][k] = true
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}
	for k := range cited {
		if !hosts[k[0]] {
			continue // no logs for that site: nothing to say about its crawling
		}
		if pages[k] == nil {
			pages[k] = &PageRow{Host: k[0], Path: k[1]}
		}
	}
	for k, row := range pages {
		row.Use = cited[k]
		if !visits {
			row.Visits = 0
		}
		ai := row.Assistant + row.Search + row.Training
		switch {
		case row.Path == ErrorPath: // not a page: nothing to cite
		case ai > 0 && row.Cited == 0:
			row.Flag = "crawled-not-cited"
		case row.Cited > 0 && row.Assistant+row.Search == 0:
			row.Flag = "cited-not-crawled"
		}
		rep.Pages = append(rep.Pages, *row)
		rep.Totals.Assistant += row.Assistant
		rep.Totals.Search += row.Search
		rep.Totals.Training += row.Training
		rep.Totals.Engine += row.Engine
		rep.Totals.Referrals += row.Referrals
		rep.Totals.Visits += row.Visits
		rep.Totals.Errors += row.Errors
	}
	sort.Slice(rep.Pages, func(i, j int) bool {
		x, y := rep.Pages[i], rep.Pages[j]
		if x.Cited != y.Cited {
			return x.Cited > y.Cited
		}
		if a, b := x.Assistant+x.Search+x.Training+x.Engine, y.Assistant+y.Search+y.Training+y.Engine; a != b {
			return a > b
		}
		if x.Referrals != y.Referrals {
			return x.Referrals > y.Referrals
		}
		if x.Visits != y.Visits {
			return x.Visits > y.Visits
		}
		return x.Host+x.Path < y.Host+y.Path
	})
	for h := range hosts {
		rep.Hosts = append(rep.Hosts, h)
	}
	sort.Strings(rep.Hosts)
	for b, row := range byBot {
		row.Pages = len(botPages[b])
		rep.Bots = append(rep.Bots, *row)
	}
	order := map[bots.Class]int{bots.Assistant: 0, bots.Search: 1, bots.Training: 2, bots.Engine: 3, bots.Referral: 4}
	sort.Slice(rep.Bots, func(i, j int) bool {
		x, y := rep.Bots[i], rep.Bots[j]
		if x.Class != y.Class {
			return order[x.Class] < order[y.Class]
		}
		if x.Hits != y.Hits {
			return x.Hits > y.Hits
		}
		return x.Bot < y.Bot
	})
	return rep, nil
}
