package server

import (
	"encoding/csv"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// Every column of the CSV export is marked as text when it starts like a formula, not only the
// answer's: the prompt and answerer come from a config repo, the model name from the provider, and
// the ids from the database, so any of them can carry "=HYPERLINK(...)".
func TestCSVExportMarksEveryColumn(t *testing.T) {
	t.Setenv("CHEAPSEATS_TOKENS", "")
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	s, err := New(newApp(t, testConfig(t), d))
	if err != nil {
		t.Fatal(err)
	}
	const f = `=HYPERLINK("https://evil.example")`
	if _, err := d.Exec(`INSERT INTO runs(id,set_id,set_version,name,trigger,status,snapshot,created_at,config_rev,app_version) VALUES ('r1','ai-search-visibility','v','n','manual','done','{}',?,?,?)`, store.Now(), "+rev", "-v1"); err != nil {
		t.Fatal(err)
	}
	ans := `{"model":"@model","text":"=answer","searches":[{"query":"=query","completed":true}],"citations":[{"url":"=https://evil.example"}]}`
	grd := `{"verdict":"mentioned","position":2,"top_pick":"=top","named":["=named"],"reasoning":"r","why_not_top":"=why","objections":["=objection"],"evidence":"=evidence"}`
	if _, err := d.Exec(`INSERT INTO results(run_id,prompt_idx,prompt,prompt_id,answerer,model,track,sample,status,answer,grade,verdict,finished_at) VALUES ('r1',0,?,?,?,?,'search',0,'done',?,?,'mentioned',?)`, f, "=pid", "+answerer", "@model", ans, grd, store.Now()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/runs/r1/export.csv", nil))
	if rec.Code != 200 {
		t.Fatalf("export: %d %s", rec.Code, limit(rec.Body.String()))
	}
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows: %v %v", rows, err)
	}
	head, row := rows[0], rows[1]
	for i, c := range row {
		if c != "" && strings.IndexByte("=+-@\t\r", c[0]) >= 0 {
			t.Errorf("%s = %q: starts like a formula", head[i], c)
		}
	}
	// Each hostile value arrived, marked: the test above would pass on an empty row.
	for _, want := range []string{"'" + f, "'+answerer", "'@model", "'=pid", "'+rev", "'-v1", "'=answer", "'=evidence", "'=top"} {
		found := false
		for _, c := range row {
			found = found || c == want
		}
		if !found {
			t.Errorf("no cell %q in %q", want, row)
		}
	}
}
