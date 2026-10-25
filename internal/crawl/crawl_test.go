package crawl

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/bots"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// Today's date in each line's format, so the counts fall inside a report's window.
var (
	today = time.Now().UTC().Truncate(time.Second)
	clf   = today.Format("02/Jan/2006:15:04:05 -0700")
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		name, line string
		want       Hit
	}{
		{"combined", `203.0.113.7 - - [` + clf + `] "GET /docs/start?x=1 HTTP/1.1" 200 5120 "-" "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; ChatGPT-User/1.0; +https://openai.com/bot"`,
			Hit{Time: today, IP: "203.0.113.7", Target: "/docs/start?x=1", Status: 200, UA: "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; ChatGPT-User/1.0; +https://openai.com/bot"}},
		{"combined with a user and escaped quotes", `198.51.100.2 - frank [` + clf + `] "GET / HTTP/2.0" 404 0 "https://chatgpt.com/" "say \"hi\""`,
			Hit{Time: today, IP: "198.51.100.2", Target: "/", Status: 404, Referer: "https://chatgpt.com/", UA: `say "hi"`}},
		{"vhost combined", `cheapseats.dev:443 203.0.113.7 - - [` + clf + `] "GET /pricing HTTP/1.1" 200 10 "-" "GPTBot/1.2"`,
			Hit{Time: today, Host: "cheapseats.dev:443", IP: "203.0.113.7", Target: "/pricing", Status: 200, UA: "GPTBot/1.2"}},
		{"common", `203.0.113.7 - - [` + clf + `] "GET /x HTTP/1.0" 200 10`,
			Hit{Time: today, IP: "203.0.113.7", Target: "/x", Status: 200}},
		{"caddy", `{"level":"info","ts":` + strconv.FormatInt(today.Unix(), 10) + `.5,"logger":"http.log.access","msg":"handled request","request":{"remote_ip":"10.0.0.1","client_ip":"203.0.113.9","proto":"HTTP/2.0","method":"GET","host":"docs.cheapseats.dev","uri":"/guide","headers":{"User-Agent":["Claude-SearchBot/1.0"],"Referer":["https://claude.ai/"]}},"status":200}`,
			Hit{Time: today.Add(500 * time.Millisecond), IP: "203.0.113.9", Host: "docs.cheapseats.dev", Target: "/guide", Status: 200, UA: "Claude-SearchBot/1.0", Referer: "https://claude.ai/"}},
		{"cloudflare, nanoseconds", `{"ClientIP":"203.0.113.5","ClientRequestHost":"cheapseats.dev","ClientRequestURI":"/blog/x?utm_source=chatgpt.com","ClientRequestUserAgent":"Mozilla/5.0 (Macintosh)","ClientRequestReferer":"","EdgeResponseStatus":200,"EdgeStartTimestamp":` + strconv.FormatInt(today.UnixNano(), 10) + `}`,
			Hit{Time: today, IP: "203.0.113.5", Host: "cheapseats.dev", Target: "/blog/x?utm_source=chatgpt.com", Status: 200, UA: "Mozilla/5.0 (Macintosh)"}},
		{"cloudflare, RFC 3339", `{"ClientIP":"203.0.113.5","ClientRequestHost":"cheapseats.dev","ClientRequestURI":"/","ClientRequestUserAgent":"PerplexityBot/1.0","EdgeResponseStatus":403,"EdgeStartTimestamp":"` + today.Format(time.RFC3339) + `"}`,
			Hit{Time: today, IP: "203.0.113.5", Host: "cheapseats.dev", Target: "/", Status: 403, UA: "PerplexityBot/1.0"}},
	} {
		got, ok := Parse(c.line)
		if !ok {
			t.Errorf("%s: not parsed", c.name)
			continue
		}
		if !got.Time.Equal(c.want.Time) {
			t.Errorf("%s: time %v, want %v", c.name, got.Time, c.want.Time)
		}
		got.Time = c.want.Time
		if got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
	for _, bad := range []string{"", "garbage", `{"level":"info"}`, `1.2.3.4 - - [not a time] "GET / HTTP/1.1" 200 1`} {
		if _, ok := Parse(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		hit        Hit
		class      bots.Class
		bot, host  string
		path       string
		ok         bool
		errorCount bool
	}{
		{Hit{Target: "/docs/", UA: "Mozilla/5.0; compatible; OAI-SearchBot/1.0"}, bots.Search, "OAI-SearchBot", "cheapseats.dev", "/docs", true, false},
		{Hit{Target: "/made-up", UA: "ClaudeBot/1.0", Status: 404}, bots.Training, "ClaudeBot", "cheapseats.dev", ErrorPath, true, true},
		{Hit{Host: "WWW.CheapSeats.Dev:443", Target: "/a?b=1", UA: "Mozilla/5.0 (compatible; bingbot/2.0)"}, bots.Engine, "Bingbot", "cheapseats.dev", "/a", true, false},
		{Hit{Target: "/p", UA: "Mozilla/5.0", Referer: "https://www.perplexity.ai/search?q=x"}, bots.Referral, "Perplexity", "cheapseats.dev", "/p", true, false},
		{Hit{Target: "/p?utm_source=chatgpt.com", UA: "Mozilla/5.0"}, bots.Referral, "ChatGPT", "cheapseats.dev", "/p", true, false},
		{Hit{Target: "https://other.example/x", UA: "Perplexity-User/1.0"}, bots.Assistant, "Perplexity-User", "other.example", "/x", true, false},
		{Hit{Target: "/app.js", UA: "GPTBot/1.0"}, "", "", "", "", false, false},                            // an asset, not a page
		{Hit{Target: "/", UA: "Mozilla/5.0", Referer: "https://google.com/"}, "", "", "", "", false, false}, // a person from elsewhere
	} {
		e, ok := Classify(c.hit, "cheapseats.dev")
		if ok != c.ok || ok && (e.Class != c.class || e.Bot != c.bot || e.Host != c.host || e.Path != c.path || e.Error != c.errorCount || e.Hash == "") {
			t.Errorf("%+v: %+v %v", c.hit, e, ok)
		}
	}
}

func TestImportAndReport(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	log := strings.Join([]string{
		`203.0.113.7 - - [` + clf + `] "GET /docs/start HTTP/1.1" 200 5120 "-" "ChatGPT-User/1.0"`,
		`203.0.113.8 - - [` + clf + `] "GET /docs/start HTTP/1.1" 200 5120 "-" "GPTBot/1.0"`,
		`203.0.113.8 - - [` + clf + `] "GET /blog/old HTTP/1.1" 200 5120 "-" "GPTBot/1.0"`,
		`203.0.113.9 - - [` + clf + `] "GET /docs/start HTTP/1.1" 200 5120 "https://chatgpt.com/" "Mozilla/5.0 (Macintosh)"`,
		`203.0.113.9 - - [` + clf + `] "GET /style.css HTTP/1.1" 200 5120 "-" "GPTBot/1.0"`,
		`203.0.113.9 - - [` + clf + `] "GET / HTTP/1.1" 200 5120 "-" "Mozilla/5.0 (Macintosh)"`,
		`not a log line`,
	}, "\n")
	st, err := Import(ctx, db.DB, strings.NewReader(log), "cheapseats.dev")
	if err != nil {
		t.Fatal(err)
	}
	if st.Lines != 7 || st.Unread != 1 || st.Counted != 4 || st.ByClass[bots.Assistant] != 1 || st.ByClass[bots.Training] != 2 || st.ByClass[bots.Referral] != 1 {
		t.Errorf("first import: %+v", st)
	}
	// Again, as when a rotated log is imported with the one before it: nothing counts twice.
	st, err = Import(ctx, db.DB, strings.NewReader(log), "cheapseats.dev")
	if err != nil || st.Counted != 0 || st.Duplicate != 4 {
		t.Errorf("second import: %+v %v", st, err)
	}
	if _, err := Import(ctx, db.DB, strings.NewReader("junk\nmore junk\n"), ""); err == nil {
		t.Error("a file in no known format should be an error")
	}

	cited := map[[2]string]Use{
		{"cheapseats.dev", "/docs/start"}: {Cited: 3, Surfaced: 5},
		{"cheapseats.dev", "/compare"}:    {Cited: 2},
		{"docs.other.example", "/never"}:  {Cited: 9}, // a host with no logs: left out
	}
	rep, err := Build(ctx, db.DB, 30, cited)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]PageRow{}
	for _, p := range rep.Pages {
		byPath[p.Host+p.Path] = p
	}
	if len(rep.Pages) != 3 || rep.Pages[0].Path != "/docs/start" {
		t.Fatalf("pages: %+v", rep.Pages)
	}
	if p := byPath["cheapseats.dev/docs/start"]; p.Assistant != 1 || p.Training != 1 || p.Referrals != 1 || p.Cited != 3 || p.Flag != "" || p.Last == "" {
		t.Errorf("docs/start: %+v", p)
	}
	if p := byPath["cheapseats.dev/blog/old"]; p.Flag != "crawled-not-cited" {
		t.Errorf("blog/old: %+v", p)
	}
	if p := byPath["cheapseats.dev/compare"]; p.Flag != "cited-not-crawled" {
		t.Errorf("compare: %+v", p)
	}
	if rep.Totals.Training != 2 || len(rep.Hosts) != 1 || len(rep.Bots) != 3 || rep.Bots[0].Bot != "ChatGPT-User" || rep.Bots[1].Bot != "GPTBot" || rep.Bots[1].Pages != 2 {
		t.Errorf("totals %+v, hosts %v, bots %+v", rep.Totals, rep.Hosts, rep.Bots)
	}
}

// One absurd line doesn't sink the import, and lines older than the hashes are kept aren't counted
// (they could count twice).
func TestImportSkipsLongAndOldLines(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := today.AddDate(0, 0, -keepDays-5).Format("02/Jan/2006:15:04:05 -0700")
	log := strings.Join([]string{
		`203.0.113.7 - - [` + clf + `] "GET /a HTTP/1.1" 200 1 "-" "GPTBot/1.0"`,
		`203.0.113.7 - - [` + clf + `] "GET /` + strings.Repeat("x", 3*maxLine) + ` HTTP/1.1" 200 1 "-" "GPTBot/1.0"`,
		`203.0.113.7 - - [` + old + `] "GET /old HTTP/1.1" 200 1 "-" "GPTBot/1.0"`,
		`203.0.113.7 - - [` + clf + `] "GET /b HTTP/1.1" 200 1 "-" "GPTBot/1.0"`, // last, with no newline
	}, "\r\n")
	st, err := Import(context.Background(), db.DB, strings.NewReader(log), "cheapseats.dev")
	if err != nil || st.Lines != 4 || st.Unread != 1 || st.TooOld != 1 || st.Counted != 2 {
		t.Fatalf("%+v %v", st, err)
	}
	db.Exec(`INSERT INTO crawl_seen (hash, day) VALUES ('stale', ?)`, today.AddDate(0, 0, -keepDays-1).Format("2006-01-02"))
	if _, err := Import(context.Background(), db.DB, strings.NewReader(""), ""); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM crawl_seen WHERE hash='stale'`).Scan(&n)
	if n != 0 {
		t.Error("hashes past the horizon weren't pruned")
	}
}

// Anyone can send a crawler's user agent: made-up paths, future dates and old counts don't grow
// the database, and one import stops at maxEvents.
func TestImportBoundsWhatAForgedLogCanAdd(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	future := today.AddDate(0, 0, 3).Format("02/Jan/2006:15:04:05 -0700")
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, `203.0.113.7 - - [`+clf+`] "GET /no-such-page-`+strconv.Itoa(i)+` HTTP/1.1" 404 0 "-" "GPTBot/1.0"`)
	}
	lines = append(lines,
		`203.0.113.7 - - [`+clf+`] "GET /real HTTP/1.1" 200 1 "-" "GPTBot/1.0"`,
		`203.0.113.7 - - [`+future+`] "GET /future HTTP/1.1" 200 1 "-" "GPTBot/1.0"`)
	db.Exec(`INSERT INTO crawl_daily (day, host, path, class, bot, hits, errors) VALUES (?, 'cheapseats.dev', '/stale', 'training', 'GPTBot', 1, 0)`,
		today.AddDate(0, 0, -keepDays-1).Format("2006-01-02"))
	st, err := Import(ctx, db.DB, strings.NewReader(strings.Join(lines, "\n")), "cheapseats.dev")
	if err != nil || st.Counted != 51 || st.Future != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	var paths []string
	rows, _ := db.Query(`SELECT path || ':' || hits || ':' || errors FROM crawl_daily ORDER BY path`)
	for rows.Next() {
		var p string
		rows.Scan(&p)
		paths = append(paths, p)
	}
	rows.Close()
	if strings.Join(paths, " ") != ErrorPath+":50:50 /real:1:0" {
		t.Errorf("crawl_daily: %v; want the 404s in one row, the real page, nothing stale or future", paths)
	}
	rep, err := Build(ctx, db.DB, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rep.Pages {
		if p.Path == ErrorPath && (p.Flag != "" || p.Errors != 50) {
			t.Errorf("the errors row: %+v", p)
		}
	}

	old := maxEvents
	maxEvents = 2
	t.Cleanup(func() { maxEvents = old })
	more := []string{
		`203.0.113.8 - - [` + clf + `] "GET /a HTTP/1.1" 200 1 "-" "GPTBot/1.0"`,
		`203.0.113.8 - - [` + clf + `] "GET /b HTTP/1.1" 200 1 "-" "GPTBot/1.0"`,
		`203.0.113.8 - - [` + clf + `] "GET /c HTTP/1.1" 200 1 "-" "GPTBot/1.0"`,
	}
	if st, err = Import(ctx, db.DB, strings.NewReader(strings.Join(more, "\n")), "cheapseats.dev"); err != nil || st.Counted != 2 || st.OverLimit != 1 {
		t.Fatalf("over the limit: %+v %v", st, err)
	}
	if st, err = Import(ctx, db.DB, strings.NewReader(strings.Join(more, "\n")), "cheapseats.dev"); err != nil || st.Counted != 1 || st.Duplicate != 2 {
		t.Errorf("imported again, the rest counts: %+v %v", st, err)
	}
}
