package run

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Without OPENAI_API_KEY the OpenAI answerer is left out of the design, so a Claude-only run
// still completes, and error rows from an earlier attempt are retried.
func TestErrorRowsAreRetriedAndMissingAnswerersAreLeftOut(t *testing.T) {
	e, s, calls := setup(t, 0)
	ctx := context.Background()
	r, err := e.Create(ctx, s, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Snapshot.Answerers) != 1 || len(r.Snapshot.Skipped) != 1 || r.Snapshot.Skipped[0] != "o" {
		t.Fatalf("answerers=%v skipped=%v", r.Snapshot.Answerers, r.Snapshot.Skipped)
	}
	e.DB.Exec(`UPDATE results SET status='error' WHERE run_id=?`, r.ID)
	if err = e.Execute(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Get(ctx, r.ID)
	if got.Status != "done" || calls.Load() != 12 {
		t.Fatalf("status=%s calls=%d", got.Status, calls.Load())
	}
	var openai int
	e.DB.QueryRow(`SELECT COUNT(*) FROM results WHERE run_id=? AND answerer='o'`, r.ID).Scan(&openai)
	if openai != 0 {
		t.Fatal(openai)
	}
}

// A key added later changes the design, so mixed-provider runs never rank against Claude-only ones.
func TestAnswererKeysSeparateDesigns(t *testing.T) {
	e, s, _ := setup(t, 0)
	claudeOnly := Design(NewSnapshot(e.Cfg, s))
	t.Setenv("OPENAI_API_KEY", "test")
	both := NewSnapshot(e.Cfg, s)
	if len(both.Answerers) != 2 || both.Skipped != nil || Design(both) == claudeOnly {
		t.Fatalf("answerers=%v skipped=%v", both.Answerers, both.Skipped)
	}
}
func TestGradingRetryReusesAnswerAndFrozenModel(t *testing.T) {
	e, s, _ := setup(t, 0)
	e.Cfg.Answerers = e.Cfg.Answerers[:1]
	e.Cfg.Answerers[0].Samples = config.Samples{Search: 1}
	s.Prompts = s.Prompts[:1]
	base := llm.AnthropicURL
	var answers, grades atomic.Int32
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "output_config") {
			// Fail twice: the run's own one more try fails too, so it ends partial.
			if grades.Add(1) <= 2 {
				w.WriteHeader(400)
				return
			}
			var req map[string]any
			json.Unmarshal(body, &req)
			if req["model"] != "g" {
				t.Error("grader model drifted")
			}
		} else {
			answers.Add(1)
		}
		resp, err := http.Post(base, "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}))
	defer wrapper.Close()
	llm.AnthropicURL = wrapper.URL
	r, err := e.Create(context.Background(), s, "test")
	if err != nil {
		t.Fatal(err)
	}
	e.Execute(context.Background(), r.ID)
	got, _ := e.Get(context.Background(), r.ID)
	if got.Status != "partial" {
		t.Fatal(got.Status)
	}
	e.Cfg.Grader.Model = "changed"
	if err = e.Execute(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = e.Get(context.Background(), r.ID)
	if got.Status != "done" || answers.Load() != 1 || grades.Load() != 3 || got.Profile == "" {
		t.Fatalf("status=%s answers=%d grades=%d profile=%s", got.Status, answers.Load(), grades.Load(), got.Profile)
	}
}

func TestDesignChangesWithGraderFactsAndSampleMix(t *testing.T) {
	e, s, _ := setup(t, 0)
	a := Design(NewSnapshot(e.Cfg, s))
	e.Cfg.Grader.Model = "new"
	if a == Design(NewSnapshot(e.Cfg, s)) {
		t.Fatal("grader omitted")
	}
	snap := NewSnapshot(e.Cfg, s)
	a = Design(snap)
	snap.GraderPrompt += "modified"
	if a == Design(snap) {
		t.Fatal("policy omitted")
	}
	snap = NewSnapshot(e.Cfg, s)
	a = Design(snap)
	snap.Answerers = append([]config.Answerer(nil), snap.Answerers...)
	snap.Answerers[0].Samples.Search++
	if a == Design(snap) {
		t.Fatal("sample mix omitted")
	}
}

// One run per set at a time: a double click or an overlapping schedule must not pay twice.
func TestOneRunPerSetAtATime(t *testing.T) {
	e, s, _ := setup(t, 0)
	ctx := context.Background()
	first, err := e.Create(ctx, s, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Create(ctx, s, "test")
	var busy *BusyError
	if !errors.As(err, &busy) || busy.RunID != first.ID {
		t.Fatalf("second run of a busy set: %v", err)
	}
	other := s
	other.ID = "other"
	if _, err := e.Create(ctx, other, "test"); err != nil {
		t.Fatalf("a different set should still start: %v", err)
	}
	e.DB.Exec(`UPDATE runs SET status='cancelled' WHERE id=?`, first.ID)
	if _, err := e.Create(ctx, s, "test"); err != nil {
		t.Fatalf("once the first is over, the set can run again: %v", err)
	}
}
