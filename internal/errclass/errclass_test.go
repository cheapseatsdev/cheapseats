package errclass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/bounded"
)

// The sentinels every leak test looks for: an Anthropic-shaped key, a bearer token, the value
// of a configured key, and text from a page or an answer.
const (
	SentinelKey    = "sk-ant-api03-SENTINELkey0123456789"
	SentinelBearer = "SENTINELbearer0123456789abcdef"
	SentinelEnv    = "SENTINELenvkey-0123456789"
	SentinelPage   = "PAGETEXT-SENTINEL the brand's pricing page says"
)

func errclassNoSentinel(t *testing.T, where, s string) {
	t.Helper()
	for _, v := range []string{SentinelKey, "SENTINELkey", SentinelBearer, SentinelEnv, "PAGETEXT-SENTINEL"} {
		if strings.Contains(s, v) {
			t.Errorf("%s carries %q: %s", where, v, s)
		}
	}
}

// An error reply shows its own message, with secrets masked, and its type and request id; the
// rest of the body (an echoed request, a page's text) stays out of the message.
func TestHTTPErrorShowsOnlyTheRedactedMessage(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", SentinelEnv)
	body := `{"type":"error","error":{"type":"invalid_request_error","message":"bad key ` + SentinelKey + ` and ` + SentinelEnv + ` sent as Bearer ` + SentinelBearer + `"},"request_id":"req_011","echo":{"headers":{"authorization":"Bearer ` + SentinelBearer + `"},"messages":[{"content":"` + SentinelPage + `"}]}}`
	e := HTTP("", 400, body)
	err := fmt.Errorf("analyst: %w", e)
	errclassNoSentinel(t, "Error()", err.Error())
	want := `HTTP 400: {"type":"error","error":{"type":"invalid_request_error","message":"bad key [REDACTED] and [REDACTED] sent as Bearer [REDACTED]"},"request_id":"req_011"}`
	if e.Error() != want {
		t.Errorf("got  %s\nwant %s", e.Error(), want)
	}
	if Category(err) != "http_400" || e.Detail() != body {
		t.Errorf("category %q, detail kept %d of %d bytes", Category(err), len(e.Detail()), len(body))
	}

	// Exa's shape and a long message: the message is cut.
	long := HTTP("exa", 402, `{"requestId":"abc","error":"`+strings.Repeat("x", 2*MaxExcerpt)+`"}`)
	if got := long.Error(); !strings.HasPrefix(got, `exa: HTTP 402: {"error":{"message":"xxx`) || !strings.Contains(got, `…"},"request_id":"abc"}`) || len(got) > MaxExcerpt+80 {
		t.Errorf("exa error %q", got)
	}
	// Not a JSON error: a proxy's page, a stack trace. Only the status shows.
	for _, b := range []string{"<html>" + SentinelPage + "</html>", "panic: " + SentinelKey, `{"messages":["` + SentinelPage + `"]}`} {
		e := HTTP("typesafe", 502, b)
		if e.Error() != "typesafe: HTTP 502" {
			t.Errorf("%q gave %q", b, e.Error())
		}
	}
}

// A CLI's stderr and claude's result text never reach the message; how the CLI ended does.
func TestCLIErrorsSayHowTheCLIEndedNotWhatItWrote(t *testing.T) {
	exit := exec.Command("sh", "-c", "exit 2").Run()
	stderr := "env: ANTHROPIC_API_KEY=" + SentinelEnv + " Authorization: Bearer " + SentinelBearer + "\n" + SentinelPage
	e := CLIExited("claude", exit, nil, stderr)
	errclassNoSentinel(t, "exit", e.Error())
	if e.Error() != "claude: exit status 2" || e.Category != CLIExit || e.Status != 2 || e.Detail() != stderr {
		t.Errorf("%q %s %d", e, e.Category, e.Status)
	}
	var ee *exec.ExitError
	if !errors.As(fmt.Errorf("grading: %w", e), &ee) {
		t.Error("the ExitError is not reachable through the error")
	}

	auth := CLIExited("codex", exit, nil, "Error: Invalid API key "+SentinelKey+" · Please run /login")
	errclassNoSentinel(t, "auth", auth.Error())
	if auth.Category != Auth || auth.Error() != "codex: exit status 2: not logged in" {
		t.Errorf("%q %s", auth, auth.Category)
	}

	timedOut := CLIExited("claude", exit, context.DeadlineExceeded, SentinelPage)
	if !errors.Is(timedOut, context.DeadlineExceeded) || Category(timedOut) != Timeout || timedOut.Error() != "claude: exit status 2: timed out" {
		t.Errorf("%q %s", timedOut, timedOut.Category)
	}

	res := CLIResult("claude", "error_max_turns", "I read "+SentinelPage+" and "+SentinelKey)
	errclassNoSentinel(t, "result", res.Error())
	if res.Error() != "claude: error_max_turns" || res.Category != CallFailed {
		t.Errorf("%q", res)
	}
	if odd := CLIResult("claude", SentinelPage, ""); odd.Error() != "claude: error" {
		t.Errorf("an unexpected subtype is shown: %q", odd)
	}
}

func TestCategoryReadsTypesNotMessages(t *testing.T) {
	for want, err := range map[string]error{
		"":         nil,
		Timeout:    fmt.Errorf("x: %w", context.DeadlineExceeded),
		Canceled:   context.Canceled,
		NoKey:      fmt.Errorf("ANTHROPIC_API_KEY: %w", ErrNoKey),
		"http_529": fmt.Errorf("verify: %w", HTTP("", 529, "overloaded")),
		CallFailed: errors.New("HTTP 529: " + SentinelPage),
	} {
		if got := Category(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
}

// A request that got no reply names the URL without its query, user info or fragment, and
// never Go's own text, which can quote a malformed reply.
func TestTransportErrorsRedactTheURL(t *testing.T) {
	u := "https://user:" + SentinelBearer + "@proxy.example/v1/messages?key=" + SentinelEnv + "#" + SentinelKey
	_, err := http.Get(u[:8] + "user:" + SentinelBearer + "@127.0.0.1:1/v1/messages?key=" + SentinelEnv)
	e := Transport("exa", err)
	errclassNoSentinel(t, "refused", e.Error())
	if e.Category != Network || !strings.HasPrefix(e.Error(), "exa: network error") || !strings.Contains(e.Error(), "(https://127.0.0.1:1/v1/messages)") {
		t.Errorf("%q %s", e, e.Category)
	}
	quoted := Transport("", &url.Error{Op: "Post", URL: u, Err: errors.New(`malformed HTTP response "` + SentinelPage + `"`)})
	errclassNoSentinel(t, "malformed", quoted.Error())
	if quoted.Error() != "network error (https://proxy.example/v1/messages)" {
		t.Errorf("%q", quoted)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	timed := Transport("", &url.Error{Op: "Post", URL: u, Err: ctx.Err()})
	if !errors.Is(timed, context.DeadlineExceeded) || Category(timed) != Timeout || timed.Error() != "timed out (https://proxy.example/v1/messages)" {
		t.Errorf("%q %s", timed, Category(timed))
	}
	if big := Transport("", fmt.Errorf("x: %w", bounded.ErrTooLarge)); !errors.Is(big, bounded.ErrTooLarge) {
		t.Errorf("%q lost ErrTooLarge", big)
	}
}

// A reply that isn't the JSON expected says where reading stopped, never what it read.
func TestDecodeErrorsQuoteNoInput(t *testing.T) {
	var v map[string]any
	in := `{"a": ` + SentinelPage
	e := Undecodable("claude", "output", json.Unmarshal([]byte(in), &v))
	errclassNoSentinel(t, "decode", e.Error())
	if e.Error() != "claude: decoding output: invalid JSON" || e.Category != Decode || e.Offset == 0 || Category(e) != Decode {
		t.Errorf("%q %s offset %d", e, e.Category, e.Offset)
	}
	var n struct{ A int }
	if e := Undecodable("", "response", json.Unmarshal([]byte(`{"A":"`+SentinelPage+`"}`), &n)); e.Error() != "decoding response: unexpected JSON shape" {
		t.Errorf("%q", e)
	}
}

func TestWordClipAndReplyIDs(t *testing.T) {
	for in, want := range map[string]string{"end_turn": "end_turn", "model_context_window_exceeded": "model_context_window_exceeded", "": "other", "Text": "other", SentinelPage: "other", strings.Repeat("a", 33): "other"} {
		if got := Word(in); got != want {
			t.Errorf("Word(%q) = %q", in, got)
		}
	}
	if got := Clip(strings.Repeat("é", 400), 300); len([]rune(got)) != 300 || !strings.HasSuffix(got, "…") {
		t.Errorf("Clip to 300 gave %d runes", len([]rune(got)))
	}
	if got := Clip("short", 300); got != "short" {
		t.Errorf("%q", got)
	}
	e := HTTP("", 400, `{"type":"`+SentinelKey+`","error":{"type":"x","message":"m"},"request_id":"`+SentinelKey+`"}`)
	errclassNoSentinel(t, "ids", e.Error())
}
