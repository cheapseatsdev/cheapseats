package app

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/errclass"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

const (
	leakKey    = "sk-ant-api03-SENTINELkey0123456789"
	leakBearer = "SENTINELbearer0123456789abcdef"
	leakEnv    = "SENTINELenvkey-0123456789"
	leakPage   = "PAGETEXT-SENTINEL the pricing page says"
)

func leakCheck(t *testing.T, where, s string) {
	t.Helper()
	for _, v := range []string{"SENTINELkey", leakBearer, leakEnv, "PAGETEXT-SENTINEL"} {
		if strings.Contains(s, v) {
			t.Errorf("%s carries %q:\n%s", where, v, s)
		}
	}
}

// leakErrors are the failures of real external calls, carrying a key, a bearer token, a
// configured key's value and page text: an Anthropic error reply (through the real client), a
// CLI that failed with them on stderr, and a claude result quoting them.
func leakErrors(t *testing.T) map[string]error {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", leakEnv)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"key %s rejected"},"request_id":"req_leak","echo":{"authorization":"Bearer %s","x-api-key":"%s","content":"%s"}}`, leakKey, leakBearer, leakEnv, leakPage)
	}))
	t.Cleanup(api.Close)
	old := llm.AnthropicURL
	llm.AnthropicURL = api.URL
	t.Cleanup(func() { llm.AnthropicURL = old })
	_, httpErr := llm.Anthropic(context.Background(), map[string]any{"model": "m"})
	if httpErr == nil {
		t.Fatal("the fake API did not fail")
	}
	exit := exec.Command("sh", "-c", "exit 1").Run()
	return map[string]error{
		"http":       httpErr,
		"cli stderr": errclass.CLIExited("claude", exit, nil, "ANTHROPIC_API_KEY="+leakEnv+"\nAuthorization: Bearer "+leakBearer+"\n"+leakPage+" "+leakKey),
		"cli result": errclass.CLIResult("claude", "error_during_execution", leakPage+" "+leakKey),
	}
}

// The known instance: recordAnalysis logged a failed verification's raw error, and stored it
// in fixes_error, which the run page, the hub, MCP and the ops email all show. Neither the log
// nor the column carries a key, a token or page text, whichever call failed and whether the
// build failed in verification (a first build, and a rebuild that keeps the earlier analysis)
// or before it.
func TestRecordedAnalysisErrorsLeakNothing(t *testing.T) {
	a, id := providersApp(t)
	errs := leakErrors(t)
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	ctx := context.Background()
	for name, err := range errs {
		for i, build := range []error{
			&analysis.VerificationError{Candidates: 1, Err: err}, // first build: stored with its analysis
			&analysis.VerificationError{Candidates: 1, Err: err}, // rebuild: the earlier analysis is kept
			fmt.Errorf("analyst: %w", err),                       // failed before verification
		} {
			buf.Reset()
			if i == 0 {
				if _, uerr := a.DB.Exec(`UPDATE runs SET fixes=NULL, fixes_at=NULL, fixes_error=NULL WHERE id=?`, id); uerr != nil {
					t.Fatal(uerr)
				}
			}
			returned := a.recordAnalysis(ctx, id, verifyFailureFrom(t, a, id), verifyFailureAnalysis(), 0, build)
			var fe sql.NullString
			a.DB.QueryRow(`SELECT fixes_error FROM runs WHERE id=?`, id).Scan(&fe)
			if !fe.Valid {
				t.Fatalf("%s: no fixes_error stored", name)
			}
			leakCheck(t, name+" fixes_error", fe.String)
			leakCheck(t, name+" log", buf.String())
			if returned != nil {
				leakCheck(t, name+" returned error", returned.Error())
			}
			if strings.Contains(buf.String(), "analysis verification") && !strings.Contains(buf.String(), "category="+errclass.Category(err)) {
				t.Errorf("%s: log has no category: %s", name, buf.String())
			}
		}
	}
	// What the product shows stays: the provider's message, redacted, and its request id.
	a.DB.Exec(`UPDATE runs SET fixes=NULL, fixes_at=NULL, fixes_error=NULL WHERE id=?`, id)
	a.recordAnalysis(ctx, id, verifyFailureFrom(t, a, id), verifyFailureAnalysis(), 0, &analysis.VerificationError{Candidates: 1, Err: errs["http"]})
	var fe string
	a.DB.QueryRow(`SELECT fixes_error FROM runs WHERE id=?`, id).Scan(&fe)
	if got := ReadAnalysisError(fe); got.RequestID != "req_leak" || !strings.HasSuffix(got.Message, "HTTP 400: key [REDACTED] rejected") {
		t.Errorf("the provider's message did not survive: %+v", got)
	}
}
