package server

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A share link: made behind auth, opened without it, showing only its run's digest, and dead once
// revoked or expired.
func TestShareLifecycle(t *testing.T) {
	a, db, execute, _ := fixFixture(t)
	r1, r2 := execute(), execute()
	t.Setenv("CHEAPSEATS_TOKENS", "tok")
	t.Setenv("CHEAPSEATS_PUBLIC_URL", "https://cheapseats.example.com/")
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	do := func(method, path, body string, authed bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if authed {
			req.Header.Set("Authorization", "Bearer tok")
		}
		if strings.HasPrefix(body, "days=") {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	create := func(run, body string) NewShare {
		t.Helper()
		rec := do("POST", "/api/runs/"+run+"/shares", body, true)
		var sh NewShare
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &sh) != nil {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
		return sh
	}
	pathOf := func(u string) string { p, _ := url.Parse(u); return p.Path }

	// Making and revoking need auth.
	if rec := do("POST", "/api/runs/"+r1+"/shares", "", false); rec.Code != 401 {
		t.Errorf("unauthenticated create: %d", rec.Code)
	}
	sh := create(r1, "")
	if !strings.HasPrefix(sh.URL, "https://cheapseats.example.com/share/") || sh.RunID != r1 || sh.IncludeAnswers {
		t.Fatalf("share: %+v", sh)
	}
	if exp, _ := time.Parse(time.RFC3339, sh.ExpiresAt); exp.Sub(time.Now()) < 29*24*time.Hour || exp.Sub(time.Now()) > 31*24*time.Hour {
		t.Errorf("default expiry: %s", sh.ExpiresAt)
	}
	// Only the token's hash is kept.
	token := strings.TrimPrefix(pathOf(sh.URL), "/share/")
	var stored string
	db.QueryRow(`SELECT token_hash FROM shares WHERE id=?`, sh.ID).Scan(&stored)
	if len(token) != 22 || stored == "" || strings.Contains(stored, token) {
		t.Errorf("token %q stored as %q", token, stored)
	}

	// Opened without auth: the digest, with its headers, and nothing else.
	rec := do("GET", pathOf(sh.URL), "", false)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Share of voice") || !strings.Contains(body, "Write a credential-gateway guide") || !strings.Contains(body, "keys-off-the-box") {
		t.Fatalf("share page: %d\n%s", rec.Code, limit(body))
	}
	for k, want := range map[string]string{"X-Robots-Tag": "noindex", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store"} {
		if !strings.Contains(rec.Header().Get(k), want) {
			t.Errorf("%s: %q", k, rec.Header().Get(k))
		}
	}
	fixes, _, _ := a.Fixes(context.Background(), r1)
	for _, leak := range []string{r1, r2, "/runs/", "/api/", "Use Docker Sandboxes or Cheap Seats.", "claude_prompt", `href="/quick-wins"`} {
		if strings.Contains(body, leak) {
			t.Errorf("share page shows %q", leak)
		}
	}
	if len(fixes) > 0 && fixes[0].ClaudePrompt != "" && strings.Contains(body, fixes[0].ClaudePrompt[:40]) {
		t.Error("share page shows the fix's Claude prompt")
	}
	// Its token opens its run and nothing else: not the other run, nor anything behind auth.
	for path, code := range map[string]int{"/runs/" + r2: 303, "/api/runs/" + r2: 401, "/api/runs/" + r1: 401, "/share/" + token + "x": 404, "/share/" + token + "/../../api/runs/" + r2: 401} {
		if rec := do("GET", path, "", false); rec.Code != code && !(strings.Contains(path, "..") && rec.Code != 200) {
			t.Errorf("%s: %d, want %d", path, rec.Code, code)
		}
	}
	if rec := do("POST", pathOf(sh.URL), "", false); rec.Code == 200 {
		t.Errorf("POST to a share: %d", rec.Code)
	}

	// With answers, when asked for; made from the run page's form, which shows the link once.
	rec = do("POST", "/runs/"+r1+"/shares", "days=7&include_answers=1", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "https://cheapseats.example.com/share/") || !strings.Contains(rec.Body.String(), "copy it now") {
		t.Fatalf("form: %d %s", rec.Code, limit(rec.Body.String()))
	}
	shares, _ := a.Shares(context.Background(), r1)
	if len(shares) != 2 || !shares[0].IncludeAnswers {
		t.Fatalf("shares: %+v", shares)
	}
	// Every answer named the brand and none recommended it, so no prompt stands out. Make a
	// selection question a loss to a competitor; informational prompts are excluded from the digest.
	sharedRun, err := a.Runs.Get(context.Background(), r1)
	if err != nil {
		t.Fatal(err)
	}
	lossID, lossText := "", ""
	for _, prompt := range sharedRun.Snapshot.Set.Prompts {
		if !prompt.Info() {
			lossID, lossText = prompt.ID, prompt.Text
			break
		}
	}
	if lossID == "" {
		t.Fatal("share fixture needs a selection question")
	}
	if _, err := db.Exec(`UPDATE results SET verdict='absent', grade=replace(replace(grade, '"verdict":"mentioned"', '"verdict":"absent"'), ',"Cheap Seats"]', ']') WHERE run_id=? AND prompt_id=?`, r1, lossID); err != nil {
		t.Fatal(err)
	}
	withAnswers := create(r1, `{"include_answers":true}`)
	if body := do("GET", pathOf(withAnswers.URL), "", false).Body.String(); !strings.Contains(body, html.EscapeString(lossText)) || !strings.Contains(body, "Use Docker Sandboxes or Cheap Seats.") {
		t.Errorf("include_answers: no answer text in\n%s", limit(body))
	}

	// Revoked: gone, and the run page says so.
	if rec := do("DELETE", "/api/shares/"+strconv.FormatInt(sh.ID, 10), "", false); rec.Code != 401 {
		t.Errorf("unauthenticated revoke: %d", rec.Code)
	}
	if rec := do("DELETE", "/api/shares/"+strconv.FormatInt(sh.ID, 10), "", true); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"revoked_at"`) {
		t.Errorf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("GET", pathOf(sh.URL), "", false); rec.Code != 404 {
		t.Errorf("revoked: %d", rec.Code)
	}
	if page := do("GET", "/runs/"+r1, "", true).Body.String(); !strings.Contains(page, "revoked ") || !strings.Contains(page, `action="/shares/`) {
		t.Error("run page doesn't list the shares")
	}
	// Expired: gone too.
	db.Exec(`UPDATE shares SET expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), withAnswers.ID)
	if rec := do("GET", pathOf(withAnswers.URL), "", false); rec.Code != 404 {
		t.Errorf("expired: %d", rec.Code)
	}
	// Too long, or a run that isn't finished, is refused.
	if rec := do("POST", "/api/runs/"+r1+"/shares", `{"days":400}`, true); rec.Code != 400 {
		t.Errorf("400 days: %d", rec.Code)
	}
	db.Exec(`INSERT INTO runs(id,set_id,set_version,name,trigger,status,snapshot,created_at) VALUES ('r-busy','fix-ai-recommendations','v','busy','manual','running','{}','2026-01-01T00:00:00Z')`)
	if rec := do("POST", "/api/runs/r-busy/shares", "", true); rec.Code != 400 {
		t.Errorf("unfinished run: %d", rec.Code)
	}

	// A client asking for unknown tokens is slowed down; a link that works isn't counted.
	s.shareLimit = shareLimiter{}
	var last int
	for i := 0; i < shareMisses+1; i++ {
		last = do("GET", "/share/AAAAAAAAAAAAAAAAAAAAAA", "", false).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after %d misses: %d", shareMisses+1, last)
	}
	// Someone else's misses never lock out a link that works.
	live := create(r2, "")
	for i := 0; i < 25; i++ {
		do("GET", "/share/BBBBBBBBBBBBBBBBBBBBBB", "", false)
	}
	if rec := do("GET", pathOf(live.URL), "", false); rec.Code != 200 {
		t.Errorf("a working link after 25 misses: %d", rec.Code)
	}
}

// Without a public URL, a share link is built from the host the request came in on, over MCP too,
// and a set's newest finished run can be shared by naming the set.
func TestShareRunOverMCP(t *testing.T) {
	a, _, execute, _ := fixFixture(t)
	id := execute()
	t.Setenv("CHEAPSEATS_PUBLIC_URL", "")
	s, _ := New(a)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "share_run", Arguments: map[string]any{"set": "ai-search-visibility", "days": 3}})
	if err != nil || res.IsError {
		t.Fatalf("share_run: %v %+v", err, res)
	}
	var sh NewShare
	b, _ := json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &sh)
	if sh.RunID != id || !strings.HasPrefix(sh.URL, ts.URL+"/share/") || sh.CreatedBy != "mcp" {
		t.Fatalf("share_run: %s", b)
	}
	resp, err := http.Get(sh.URL)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("open: %v %v", err, resp)
	}
	resp.Body.Close()
	// The test server is on loopback: nobody else could open that link, and the result says so.
	if !strings.Contains(sh.Warning, "CHEAPSEATS_PUBLIC_URL") {
		t.Errorf("no loopback warning: %s", b)
	}
	// A proxy's X-Forwarded-Host names the host people used.
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "127.0.0.1:3000"
	req.Header.Set("X-Forwarded-Host", "cheapseats.example.org, inner")
	req.Header.Set("X-Forwarded-Proto", "https")
	if got := s.linkBase(requestHost(req), requestScheme(req)); got != "https://cheapseats.example.org" || linkWarning(got) != "" {
		t.Errorf("forwarded: %s", got)
	}
	// CHEAPSEATS_HOSTS names the public host when there's no public URL.
	s.hosts = []string{"cheapseats.example.com"}
	if got := s.linkBase("127.0.0.1:3000", "http"); got != "https://cheapseats.example.com" {
		t.Errorf("with hosts: %s", got)
	}
	if res, _ := cs.CallTool(ctx, &mcp.CallToolParams{Name: "share_run", Arguments: map[string]any{}}); !res.IsError {
		t.Error("share_run without run or set should be an error")
	}
}

// The strongest prompts are recommended most over two or more answers; the weakest are won by a
// competitor with the brand left out, at most one of them a total loss.
func TestPickPrompts(t *testing.T) {
	row := func(idx, rec, named, n int, picks ...string) report.PromptRow {
		p := report.PromptRow{Idx: idx, Rates: report.Rates{Recommended: report.NewRate(rec, n), Visible: report.NewRate(named, n)}}
		for _, x := range picks {
			p.TopPicks = append(p.TopPicks, report.Count{Name: x, N: 1})
		}
		return p
	}
	rows := []report.PromptRow{
		row(0, 2, 2, 2, "Acme"),
		row(1, 1, 1, 1, "Acme"), // one answer: not enough to be strongest
		row(2, 1, 2, 2, "Acme"),
		row(3, 0, 0, 2, "Rival"),
		row(4, 0, 0, 2, "Other"),
		row(5, 0, 1, 2, "Rival"),
		row(6, 0, 0, 2), // no top pick at all: nobody won it
	}
	rows = append(rows, report.PromptRow{Idx: 7, Info: true}, report.PromptRow{Idx: 8, Branded: true, Rates: report.Rates{Recommended: report.NewRate(2, 2)}})
	strong, weak := pickPrompts(rows, "acme")
	idx := func(ps []report.PromptRow) (out []int) {
		for _, p := range ps {
			out = append(out, p.Idx)
		}
		return out
	}
	if got := idx(strong); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("strongest: %v", got)
	}
	if got := idx(weak); len(got) != 2 || got[0] != 3 || got[1] != 5 {
		t.Errorf("weakest: %v", got)
	}
}
