package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/crawl"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// A database as the first release left it: runs whose snapshots list prompts as plain strings and
// whose results know prompts only by position. Upgrading must keep every one of them in the
// rankings, the quick wins and the per-prompt history.
func TestUpgradeFromSchema1KeepsHistory(t *testing.T) {
	data := t.TempDir()
	old, err := store.OpenAt(data, 1)
	if err != nil {
		t.Fatal(err)
	}
	legacyRun(t, old, "r-1", "2026-10-19", []string{"How do I measure AI visibility?", "Which tools track AI answers?"}, "")
	// The next day the second prompt was reworded and a third added; the fix list cites prompt 2.
	legacyRun(t, old, "r-2", "2026-10-20", []string{"How do I measure AI visibility?", "Which tools track AI answers today?", "Is there an open-source option?"},
		`[{"rank":1,"type":"new-page","title":"Publish a comparison","target":"x","claim":"c","done_when":"d","evidence":"e","prompts":[2],"effort":"S","displaces":null}]`)
	old.Close()

	// Upgrading before the config is converted: nothing to match the prompts against.
	cfgDir := t.TempDir()
	writeConfig(t, cfgDir, "")
	db, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "cheapseats.db.bak-v1")); err != nil {
		t.Fatalf("no backup before migrating: %v", err)
	}
	cfg, err := config.Load(filepath.Join(cfgDir, "cheapseats.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg, db); err == nil || !strings.Contains(err.Error(), "migrate-config") {
		t.Fatalf("an unconverted config should stop startup and say how to fix it: %v", err)
	}
	var empty int
	db.QueryRow(`SELECT COUNT(*) FROM results WHERE prompt_id = ''`).Scan(&empty)
	if empty == 0 {
		t.Fatal("ids were written with no config to match them against; they'd be wrong for good")
	}

	// Then the config is converted, the prompts in a new order, and the app restarts.
	writeConfig(t, cfgDir, `
[[prompts]]
id = "open-source"
text = "Is there an open-source option?"

[[prompts]]
id = "measure"
text = "How do I measure AI visibility?"

[[prompts]]
id = "tools"
text = "Which tools track AI answers today?"
`)
	a, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db.QueryRow(`SELECT COUNT(*) FROM results WHERE prompt_id = ''`).Scan(&empty)
	// r-1's reworded prompt matches nothing today, so it stays unfilled (and tracked under a
	// legacy id); everything else is filled.
	if empty != 2 {
		t.Fatalf("%d results without an id, want 2", empty)
	}

	// A method stored under an older definition (or by an older release) is recomputed from the
	// snapshot on the next start, so it can't split the trend.
	db.Exec(`UPDATE runs SET method='stale' WHERE id='r-1'`)
	if err := a.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	var methods int
	db.QueryRow(`SELECT COUNT(DISTINCT method) FROM runs`).Scan(&methods)
	if methods != 1 {
		t.Fatalf("%d methods across runs that were asked and graded the same way", methods)
	}

	rk, err := a.Rankings(ctx, 3650, 0, "s")
	if err != nil || len(rk) == 0 {
		t.Fatalf("rankings: %v %v", rk, err)
	}
	days := rk[0].Days
	if len(days) != 2 {
		t.Fatalf("want both days in one series, got %+v", days)
	}
	d := days[0]
	if d.NewMethod || d.Change == nil || len(d.Change.Added) != 2 || len(d.Change.Removed) != 1 {
		t.Fatalf("change: %+v", d.Change)
	}
	for _, x := range days {
		if x.RollingLo > x.Rolling.Visible || x.Rolling.Visible > x.RollingHi {
			t.Fatalf("band %.2f–%.2f doesn't hold %.2f", x.RollingLo, x.RollingHi, x.Rolling.Visible)
		}
	}
	if d.Compare == nil || d.Compare.Common != 1 || d.Compare.Of != 3 {
		t.Fatalf("compare: %+v", d.Compare)
	}

	qw, err := a.QuickWins(ctx)
	if err != nil || len(qw) != 1 {
		t.Fatalf("quick wins: %+v %v", qw, err)
	}
	if qw[0].Gain <= 0 || qw[0].Gain > qw[0].Speed {
		t.Errorf("expected gain %.2f should be some of speed %.2f", qw[0].Gain, qw[0].Speed)
	}
	if got := qw[0].Prompts; len(got) != 1 || got[0] != 3 {
		t.Fatalf("the fix's prompt is \"tools\", now number 3; got %v", got)
	}

	s, _ := a.Set("s")
	h, err := a.History(ctx, s, "search", 3650, 0)
	if err != nil || len(h.Rows) != 4 || !h.Rows[1].Cells[0].Asked || !h.Rows[1].Cells[1].Asked {
		t.Fatalf("history: %+v %v", h, err)
	}
}

func writeConfig(t *testing.T, dir, prompts string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "prompt-sets"), 0o755)
	os.WriteFile(filepath.Join(dir, "cheapseats.toml"), []byte(`[brand]
name = "Cheap Seats"
audience = "a software developer"

[[answerers]]
id = "claude"
provider = "anthropic"
model = "m"
samples = { search = 1, recall = 1 }

[grader]
model = "g"
`), 0o644)
	body := "id = \"s\"\nname = \"S\"\n" + prompts
	if prompts == "" {
		body += "prompts = [\"From before ids\"]\n" // not converted yet: fails to load
	}
	os.WriteFile(filepath.Join(dir, "prompt-sets", "s.toml"), []byte(body), 0o644)
}

// legacyRun writes a finished run the way the first release stored it.
func legacyRun(t *testing.T, db *store.DB, id, day string, prompts []string, fixes string) {
	t.Helper()
	snap := map[string]any{
		"answer_prompt": answer.System("a software developer"),
		"search_policy": answer.SearchPolicy,
		"grader_prompt": grade.System,
		"set":           map[string]any{"id": "s", "name": "S", "tier": "core", "prompts": prompts, "competitors": []any{}, "version": "v", "file": "s.toml"},
		"brand":         map[string]any{"Name": "Cheap Seats", "Aliases": nil, "Domains": nil, "About": "", "Facts": nil},
		"answerers":     []any{map[string]any{"ID": "claude", "Provider": "anthropic", "Model": "m", "Samples": map[string]any{"Search": 1, "Recall": 1}}},
		"grader":        "g",
	}
	js, _ := json.Marshal(snap)
	var fx any
	if fixes != "" {
		fx = fixes
	}
	at := day + "T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO runs (id,set_id,set_version,name,trigger,status,snapshot,created_at,finished_at,fixes,profile,design) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, "s", "v", "S "+day, "schedule", "done", string(js), at, at, fx, "p", "d"); err != nil {
		t.Fatal(err)
	}
	for i, p := range prompts {
		for _, track := range []string{"search", "recall"} {
			verdict := "absent"
			if i == 0 {
				verdict = "mentioned"
			}
			g, _ := json.Marshal(map[string]any{"verdict": verdict, "named": []string{}})
			if _, err := db.Exec(`INSERT INTO results (run_id,prompt_idx,prompt,answerer,model,track,sample,status,grade,verdict,finished_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				id, i, p, "claude", "m", track, 0, "done", string(g), verdict, at); err != nil {
				t.Fatal(fmt.Errorf("result: %w", err))
			}
		}
	}
}

func TestEveryDaysByTier(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tiers.Core.EveryDays, cfg.Tiers.PLG.EveryDays = 7, 14
	a := &App{Cfg: cfg}
	if a.everyDays(sets.Set{Tier: sets.Core}) != 7 || a.everyDays(sets.Set{Tier: sets.PLG}) != 14 {
		t.Fatal("wrong interval")
	}
}

// Crawl counts from before errors were pooled under crawl.ErrorPath keep their own rows and still
// count: a 404 counted under its path by an older release reads beside the pooled row new imports
// write, and the totals hold both.
func TestUpgradeKeepsCrawlErrorsCountedByPath(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO crawl_daily (day, host, path, class, bot, hits, errors) VALUES (?, 'cheapseats.dev', '/gone', 'training', 'GPTBot', 3, 3)`, now.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	line := `203.0.113.7 - - [` + now.Format("02/Jan/2006:15:04:05 -0700") + `] "GET /gone-too HTTP/1.1" 404 0 "-" "GPTBot/1.0"`
	ctx := context.Background()
	if _, err := crawl.Import(ctx, db.DB, strings.NewReader(line), "cheapseats.dev"); err != nil {
		t.Fatal(err)
	}
	rep, err := crawl.Build(ctx, db.DB, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	errs := map[string]int{}
	for _, p := range rep.Pages {
		errs[p.Path] = p.Errors
	}
	if errs["/gone"] != 3 || errs[crawl.ErrorPath] != 1 || rep.Totals.Errors != 4 || rep.Totals.Training != 4 {
		t.Errorf("errors by path %v, totals %+v", errs, rep.Totals)
	}
}
