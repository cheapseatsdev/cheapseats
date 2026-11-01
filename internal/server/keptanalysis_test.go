package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

// A rebuild that failed and kept the run's earlier analysis answered with only the bare error,
// so a client lost the analysis the run still shows and didn't learn that it was the earlier one.
func TestFailedRebuildAnswersWithTheKeptAnalysis(t *testing.T) {
	a, _, execute, _ := fixFixture(t)
	id := execute()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"schema refused"},"request_id":"req_kept"}`))
	}))
	t.Cleanup(failing.Close)
	old := llm.AnthropicURL
	llm.AnthropicURL = failing.URL
	t.Cleanup(func() { llm.AnthropicURL = old })
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/runs/"+id+"/fixes", nil))
	var got struct {
		Error    string
		Showing  string
		Analysis struct {
			Actions []app.QuickWin
			Error   *string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 500 || !strings.Contains(got.Error, "schema refused") || got.Showing != app.KeptAnalysisNote {
		t.Fatalf("want a 500 with the error and the note: %d %s", rec.Code, rec.Body.String())
	}
	if len(got.Analysis.Actions) != 1 || got.Analysis.Actions[0].Title != "Write a credential-gateway guide" || got.Analysis.Error == nil || *got.Analysis.Error != got.Error {
		t.Errorf("the body must hold the kept analysis and its recorded error: %s", rec.Body.String())
	}
}
