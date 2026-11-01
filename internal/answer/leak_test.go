package answer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/errclass"
)

const (
	leakKey    = "sk-ant-api03-SENTINELkey0123456789"
	leakBearer = "SENTINELbearer0123456789abcdef"
	leakPage   = "PAGETEXT-SENTINEL the pricing page says"
)

// A claude CLI that fails with a key, a token and the prompt's page text on stderr and in its
// result: the error from the grader's and analyst's path (ClaudeStructured) and from an answer
// (askClaudeCode) says how it ended, and none of what it wrote. Detail still has it.
func TestCLIFailuresLeakNothing(t *testing.T) {
	cliCapFake(t, "echo 'ANTHROPIC_API_KEY="+leakKey+" Authorization: Bearer "+leakBearer+" "+leakPage+"' >&2\n"+
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true,\"result\":\""+leakPage+" "+leakKey+"\"}'\nexit 1\n")
	var out struct{ Verdict string }
	errs := map[string]error{"structured": ClaudeStructured(context.Background(), "m", "s", "p", cliTestSchema, &out)}
	_, errs["answer"] = askClaudeCode(context.Background(), "m", Recall, "s", "p", false)
	for name, err := range errs {
		if err == nil {
			t.Fatalf("%s: want an error", name)
		}
		for _, v := range []string{"SENTINELkey", leakBearer, "PAGETEXT-SENTINEL"} {
			if strings.Contains(err.Error(), v) {
				t.Errorf("%s: %q carries %q", name, err, v)
			}
		}
		var ce *errclass.Error
		if !errors.As(err, &ce) || ce.Category != errclass.CLIExit || !strings.Contains(ce.Detail(), leakBearer) {
			t.Errorf("%s: %v is not a CLI exit with its stderr kept", name, err)
		}
	}
	if got := errs["structured"].Error(); got != "claude: exit status 1 (claude: error_during_execution)" {
		t.Errorf("structured error %q", got)
	}
}
