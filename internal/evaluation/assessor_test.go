package evaluation

import (
	"context"
	"encoding/json"
	"github.com/cheapseatsdev/cheapseats/internal/budget"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssessorUsesLabelBlindProductionTransportAndBudget(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			System   string `json:"system"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
		}
		if request.System != "CUSTOM_EXPERIMENT_RULES" || len(request.Messages) != 1 || !strings.Contains(request.Messages[0].Content, evaluationRecord().Answer) {
			t.Error("wrong prompt or incomplete answer")
		}
		for _, secret := range []string{"HUMAN_NOTE_SENTINEL", "OLD_LABEL_SENTINEL"} {
			if strings.Contains(string(body), secret) {
				t.Error("label leaked to assessment")
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"verdict":"absent","named":[],"reasoning":"independent result"}`}}, "usage": map[string]any{"input_tokens": 100, "output_tokens": 20}})
	}))
	defer api.Close()
	old := llm.AnthropicURL
	llm.AnthropicURL = api.URL
	defer func() { llm.AnthropicURL = old }()
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := &config.Config{Grader: config.Model{Model: "test-grader"}, Pricing: map[string]config.Pricing{"test-grader": {Input: 1, Output: 1}}, Budget: config.Budget{DailyUSD: 5, ReservationUSD: .6, Concurrency: 1}}
	a := Assessor{Cfg: cfg, Budget: budget.New(cfg, db)}
	r := evaluationRecord()
	r.Note = "HUMAN_NOTE_SENTINEL"
	r.Evidence = "OLD_LABEL_SENTINEL"
	g, _, err := a.Assess(context.Background(), r, "CUSTOM_EXPERIMENT_RULES", "test-operation", 1)
	if err != nil || g.Verdict != grade.Absent {
		t.Fatalf("assessment: %+v %v", g, err)
	}
	var count int
	var spent float64
	if err := db.QueryRow(`SELECT count(*),sum(amount_usd) FROM spend_events WHERE kind='provider-attempt/test-operation'`).Scan(&count, &spent); err != nil || count != 1 || spent <= 0 || spent >= .6 {
		t.Fatalf("budget settlement: %d %f %v", count, spent, err)
	}
	if grade.System == "CUSTOM_EXPERIMENT_RULES" {
		t.Fatal("experiment changed global production prompt")
	}
}
