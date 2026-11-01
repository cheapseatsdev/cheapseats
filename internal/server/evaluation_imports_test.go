package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

func evaluationImportRunFixture(t *testing.T, s *Server) (*run.Run, []run.Result) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "fake-evaluation-import-no-calls")
	set, ok := s.App.Set("ai-search-visibility")
	if !ok {
		t.Fatal("missing test set")
	}
	set.Prompts = []sets.Prompt{{ID: "choose-tracker", Text: "Which tracker should I choose?"}, {ID: "explain-metrics", Text: "What do visibility metrics mean?", Intent: "info"}}
	ctx := context.Background()
	r, err := s.App.Runs.Create(ctx, set, "manual")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := s.App.Runs.Results(ctx, r.ID)
	if err != nil || len(rs) < 3 {
		t.Fatalf("results: %v %v", rs, err)
	}
	for _, result := range rs {
		text := fmt.Sprintf("  Use **%s** for this question.\nSaved answer %d has its exact spacing.\n ", r.Snapshot.Brand.Name, result.ID)
		a, _ := json.Marshal(answer.Answer{Text: text})
		g, _ := json.Marshal(grade.Grade{Verdict: grade.Recommended, TopPick: &r.Snapshot.Brand.Name, Tone: grade.Positive, Reasoning: "OLD_MODEL_REASON_NOT_REVIEW_EVIDENCE", Evidence: "Use **" + r.Snapshot.Brand.Name + "** for this question."})
		if _, err := s.App.DB.Exec(`UPDATE results SET status='done',answer=?,grade=?,verdict='recommended',top_pick=1 WHERE id=?`, string(a), string(g), result.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.App.DB.Exec(`UPDATE runs SET status='done' WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = s.App.Runs.Get(ctx, r.ID)
	rs, _ = s.App.Runs.Results(ctx, r.ID)
	return r, rs
}

func evaluationImportCounts(t *testing.T, s *Server) [3]int {
	t.Helper()
	var counts [3]int
	for i, table := range []string{"evaluation_imports", "evaluation_cases", "evaluation_events"} {
		if err := s.App.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func TestEvaluationRunImportPinsSavedContextAndRetainsReviews(t *testing.T) {
	s, wb := evaluationServer(t)
	r, results := evaluationImportRunFixture(t, s)
	ctx := context.Background()
	// A changed live config cannot relabel the old identity or buyer intent.
	s.App.Cfg.Brand.Name = "Later identity"
	s.App.Cfg.Brand.Aliases = []string{"later-alias"}
	p, err := s.App.EvaluationRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Brand != r.Snapshot.Brand.Name || !reflect.DeepEqual(p.Answers[0].Record.Brand, grade.GoldenBrandOf(r.Snapshot.Brand)) || !reflect.DeepEqual(p.Answers[0].Record.Competitors, r.Snapshot.Set.Competitors) {
		t.Fatal("preview used current config instead of saved grading context")
	}
	if p.Answers[len(p.Answers)-1].Record.Intent != "info" {
		t.Fatal("saved informational intent was lost")
	}
	h := s.Handler()
	for _, path := range []string{"/evaluation", "/evaluation/runs", "/evaluation/runs/" + r.ID, "/evaluation/runs/" + r.ID + "?result=" + strconv.FormatInt(results[0].ID, 10)} {
		got := evaluationRequest(h, "GET", path, nil)
		if got.Code != 200 || strings.Contains(got.Body.String(), "OLD_MODEL_REASON_NOT_REVIEW_EVIDENCE") {
			t.Fatalf("preview: %s %d %s", path, got.Code, limit(got.Body.String()))
		}
	}
	if counts := evaluationImportCounts(t, s); counts != [3]int{} {
		t.Fatalf("GET imported evidence: %v", counts)
	}
	values := url.Values{"digest": {p.Digest}, "result": {strconv.FormatInt(results[0].ID, 10)}}
	got := evaluationRequest(h, "POST", "/evaluation/runs/"+r.ID, values)
	if got.Code != http.StatusSeeOther || !strings.HasPrefix(got.Header().Get("Location"), "/evaluation/cases/") {
		t.Fatalf("import did not open the single case: %d %s", got.Code, got.Body.String())
	}
	a, err := s.App.ImportEvaluationRun(ctx, r.ID, []int64{results[0].ID}, p.Digest)
	if err != nil || a.Cases != 1 || a.Blocked != 0 {
		t.Fatalf("repeat import: %+v %v", a, err)
	}
	c, err := wb.Case(ctx, a.Rows[0].CaseID)
	if err != nil || c.FirstReviewed || c.Review.State != "unreviewed" || c.Record.Answer != results[0].Answer.Text || c.Record.LabeledBy != "claude" || c.Record.Source.Result != results[0].ID || c.Record.Source.Method != r.Method {
		t.Fatalf("incorrect source/provenance: %+v %v", c, err)
	}
	page := evaluationRequest(h, "GET", got.Header().Get("Location"), nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), fmt.Sprintf("/runs/%s#result-%d", r.ID, results[0].ID)) || strings.Contains(page.Body.String(), "Compare with the imported model proposal") {
		t.Fatal("first review lost its source or revealed the imported proposal")
	}
	if _, err := wb.Save(ctx, c.ID, 0, "judgment", evaluation.Judgment{Verdict: grade.Absent, AbsenceReason: "echo", Evidence: []string{"Use **" + r.Snapshot.Brand.Name + "** for this question."}, Note: "Fictional test judgment, not a reference label."}, "test reviewer", evaluation.Rubric()); err != nil {
		t.Fatal(err)
	}
	again, err := s.App.ImportEvaluationRun(ctx, r.ID, []int64{results[0].ID}, p.Digest)
	if err != nil || again.ID != a.ID {
		t.Fatal("repeat import was not idempotent", err)
	}
	c, _ = wb.Case(ctx, c.ID)
	if !c.FirstReviewed || c.Review.Revision != 1 || c.Review.Judgment.Verdict != grade.Absent || c.Record.Expected.Verdict != grade.Recommended {
		t.Fatal("reimport reset a human review or rewrote its original proposal")
	}
	if counts := evaluationImportCounts(t, s); counts != [3]int{1, 1, 1} {
		t.Fatal("unselected answers or duplicate reviews imported", counts)
	}
	if _, err := wb.Grouping(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	frozen, err := wb.Approve(ctx, evaluation.Approval{ImportID: a.ID, Name: "Synthetic frozen reference", Members: map[string]int{c.ID: c.Review.Revision}}, "test approver")
	if err != nil {
		t.Fatal(err)
	}
	before, err := frozen.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	// A larger import must not replace the existing version or its denominator.
	more, err := s.App.ImportEvaluationRun(ctx, r.ID, []int64{results[1].ID, results[0].ID}, p.Digest)
	if err != nil || more.Cases != 2 {
		t.Fatalf("larger import: %+v %v", more, err)
	}
	frozenAfter, err := wb.Dataset(ctx, frozen.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterJSONL, err := frozenAfter.JSONL()
	if err != nil || string(before) != string(afterJSONL) || len(frozenAfter.Members) != 1 {
		t.Fatal("import changed frozen reference evidence", err)
	}
	after, _ := s.App.Runs.Results(ctx, r.ID)
	if !reflect.DeepEqual(after, results) {
		t.Fatal("review import or human judgment changed production answers/grades")
	}
}

func TestEvaluationRunImportRejectsStaleOrInvalidSelectionAtomically(t *testing.T) {
	s, _ := evaluationServer(t)
	r, rs := evaluationImportRunFixture(t, s)
	p, err := s.App.EvaluationRun(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/evaluation/runs/" + r.ID
	first := strconv.FormatInt(rs[0].ID, 10)
	for _, ids := range [][]string{nil, {first, first}, {first, "9223372036854775807"}, {"-1"}} {
		got := evaluationRequest(s.Handler(), "POST", path, url.Values{"digest": {p.Digest}, "result": ids})
		if got.Code != http.StatusBadRequest {
			t.Fatalf("selection %v: %d", ids, got.Code)
		}
	}
	tooMany := make([]int64, app.MaxEvaluationRunAnswers+1)
	if _, err := s.App.ImportEvaluationRun(context.Background(), r.ID, tooMany, p.Digest); err == nil {
		t.Fatal("oversized selection accepted")
	}
	// Simulate a regrade between preview and submission. No partial import may survive.
	if _, err := s.App.DB.Exec(`UPDATE results SET grade=? WHERE id=?`, `{"verdict":"absent"}`, rs[0].ID); err != nil {
		t.Fatal(err)
	}
	got := evaluationRequest(s.Handler(), "POST", path, url.Values{"digest": {p.Digest}, "result": {first}})
	if got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "changed since the import preview") || !strings.Contains(got.Body.String(), `value="`+first+`" checked`) {
		t.Fatalf("stale preview lost selection or was imported: %d %s", got.Code, limit(got.Body.String()))
	}
	if got.Result().Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal("repair preview was not served as HTML")
	}
	if counts := evaluationImportCounts(t, s); counts != [3]int{} {
		t.Fatalf("invalid selection partially imported: %v", counts)
	}
}

func TestEvaluationRunImportAccountsForUnavailableEvidence(t *testing.T) {
	s, _ := evaluationServer(t)
	r, rs := evaluationImportRunFixture(t, s)
	for _, q := range []struct {
		sql string
		id  int64
	}{
		{`UPDATE results SET answer=NULL WHERE id=?`, rs[0].ID},
		{`UPDATE results SET grade=NULL WHERE id=?`, rs[1].ID},
		{`UPDATE results SET status='error' WHERE id=?`, rs[2].ID},
	} {
		if _, err := s.App.DB.Exec(q.sql, q.id); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.App.EvaluationRun(context.Background(), r.ID)
	if err != nil || len(p.Answers) != len(rs) {
		t.Fatal("unavailable evidence disappeared", err)
	}
	bad := map[int64]bool{rs[0].ID: true, rs[1].ID: true, rs[2].ID: true}
	for _, row := range p.Answers {
		if bad[row.ID] && (row.Record != nil || row.Problem == "") {
			t.Fatalf("missing/failed evidence became a scored absence: %+v", row)
		}
	}
	if _, err := s.App.ImportEvaluationRun(context.Background(), r.ID, []int64{rs[0].ID, rs[3].ID}, p.Digest); err == nil {
		t.Fatal("unavailable selected answer accepted")
	}
	if counts := evaluationImportCounts(t, s); counts != [3]int{} {
		t.Fatal("invalid evidence selection partially imported", counts)
	}
	// Legacy context remains unknown even with a usable current config.
	r.Snapshot.Brand.Name = ""
	snapshot, _ := json.Marshal(r.Snapshot)
	if _, err := s.App.DB.Exec(`UPDATE runs SET snapshot=? WHERE id=?`, string(snapshot), r.ID); err != nil {
		t.Fatal(err)
	}
	page := evaluationRequest(s.Handler(), "GET", "/evaluation/runs/"+r.ID, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "did not record its intended brand identity") || strings.Contains(page.Body.String(), "Import selected answers</button>") {
		t.Fatal("missing context was guessed or hidden", page.Code)
	}
}

func TestEvaluationRunImportRoutesRequireFeatureAndAuthentication(t *testing.T) {
	s, _ := evaluationServer(t)
	r, rs := evaluationImportRunFixture(t, s)
	p, err := s.App.EvaluationRun(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"digest": {p.Digest}, "result": {strconv.FormatInt(rs[0].ID, 10)}}
	for _, mode := range []string{"disabled", "readonly", "authenticated"} {
		t.Setenv("CHEAPSEATS_EVALUATION", "1")
		t.Setenv("CHEAPSEATS_READONLY", "")
		t.Setenv("CHEAPSEATS_TOKENS", "")
		if mode == "disabled" {
			t.Setenv("CHEAPSEATS_EVALUATION", "")
		}
		if mode == "readonly" {
			t.Setenv("CHEAPSEATS_READONLY", "1")
		}
		if mode == "authenticated" {
			t.Setenv("CHEAPSEATS_TOKENS", "test-import-access-token")
		}
		guarded, err := New(s.App)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/evaluation/runs", "/evaluation/runs/" + r.ID} {
			get := evaluationRequest(guarded.Handler(), "GET", path, nil)
			if get.Code == http.StatusOK {
				t.Fatalf("%s exposed %s", mode, path)
			}
			post := evaluationRequest(guarded.Handler(), "POST", path, values)
			if post.Code == http.StatusSeeOther && mode != "authenticated" {
				t.Fatalf("%s imported through %s", mode, path)
			}
		}
		if mode != "authenticated" {
			page := evaluationRequest(guarded.Handler(), "GET", "/runs/"+r.ID, nil)
			if strings.Contains(page.Body.String(), "Review this grade") || strings.Contains(page.Body.String(), "Review saved answers</a>") {
				t.Fatal("inactive workbench has import links")
			}
		} else {
			req := httptest.NewRequest("GET", "http://localhost/evaluation/runs/"+r.ID, nil)
			req.Header.Set("Authorization", "Bearer test-import-access-token")
			got := httptest.NewRecorder()
			guarded.Handler().ServeHTTP(got, req)
			if got.Code != 200 {
				t.Fatal("authenticated preview refused", got.Code)
			}
		}
	}
	if counts := evaluationImportCounts(t, s); counts != [3]int{} {
		t.Fatal("guarded requests wrote evaluation data", counts)
	}
}
