// Package errclass is the one error type for a failed call to something outside Cheap Seats: the
// Anthropic and OpenAI APIs, the claude and codex CLIs, Exa and TypeSafe. Their replies, the
// CLIs' stderr and result text can carry answer text, page text or a secret (a key echoed back,
// a token in a stack trace), and every error ends up somewhere: the service log, a stored error
// column, a page, an API or MCP reply, the ops email. So the clients build their errors here,
// and Error() is safe text by construction: a fixed description of what failed, plus for an
// HTTP error the provider's own error message, redacted and cut short. What came back verbatim
// stays inside the error, reachable only through Detail, which nothing in a log, store or
// response path calls. Wrapping with fmt.Errorf("...: %w", err) keeps it safe.
package errclass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/cheapseatsdev/cheapseats/internal/bounded"
	"github.com/cheapseatsdev/cheapseats/internal/redact"
)

// The fixed categories. HTTP errors are "http_<status>".
const (
	Timeout    = "timeout"     // the call's deadline passed
	Canceled   = "canceled"    // the caller stopped it
	NoKey      = "no_key"      // no credential configured
	Auth       = "auth"        // a CLI that says its login is missing or expired
	Decode     = "decode"      // a reply that isn't the JSON expected
	Network    = "network"     // no HTTP reply: DNS, connection, TLS, a cut-off body
	CLIExit    = "cli_exit"    // a CLI that exited non-zero
	CallFailed = "call_failed" // anything else
)

// ErrNoKey is a provider call made without its API key.
var ErrNoKey = errors.New("API key not set")

// MaxExcerpt is the most of a provider's error message an Error shows, in runes.
const MaxExcerpt = 300

// Error is a failed external call. Every field is safe to log, store and show.
type Error struct {
	Op       string // what was called, fixed text ("exa", "claude"); "" for the model APIs, whose errors always read "HTTP <status>: ..."
	Category string // one of the constants above, or http_<status>
	Status   int    // the HTTP status, or the CLI's exit code; 0 when there is none
	Excerpt  string // the provider's error message, redacted and at most MaxExcerpt runes; only for HTTP errors
	Offset   int64  // for a decode error, the byte offset where reading stopped; not in Error(), which stays the same when a failure repeats
	detail   string // a CLI's subtype or exit status: fixed vocabulary, shown
	plain    bool   // detail is the whole message (transport and decode errors)
	raw      string // what came back verbatim: the body, stderr or result text. Never shown.
	cause    error  // a safe error kept for errors.Is/As: a context error or *exec.ExitError
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.Op != "" {
		b.WriteString(e.Op + ": ")
	}
	switch {
	case strings.HasPrefix(e.Category, "http_"):
		fmt.Fprintf(&b, "HTTP %d", e.Status)
		if e.Excerpt != "" {
			b.WriteString(": " + e.Excerpt)
		}
	case e.plain:
		b.WriteString(e.detail)
	case e.detail != "":
		b.WriteString(e.detail)
		if e.Category == Auth {
			b.WriteString(": not logged in")
		} else if e.Category == Timeout {
			b.WriteString(": timed out")
		} else if e.Category == Canceled {
			b.WriteString(": canceled")
		}
	default:
		b.WriteString(e.Category)
	}
	return b.String()
}

// Unwrap gives errors.Is and errors.As the safe cause: context.DeadlineExceeded for a CLI that
// timed out, the *exec.ExitError of one that failed.
func (e *Error) Unwrap() error { return e.cause }

// Detail is what came back verbatim: an HTTP body's first bytes, a CLI's stderr tail or its
// result text. It is unredacted. Tests read it; nothing that logs, stores or shows an error may.
func (e *Error) Detail() string { return e.raw }

// Category names err's kind from its type or sentinel, never from its message: one of the
// constants above, http_<status>, or "" for no error.
func Category(err error) string {
	var e *Error
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	var exit *exec.ExitError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &e):
		return e.Category
	case errors.Is(err, context.DeadlineExceeded):
		return Timeout
	case errors.Is(err, context.Canceled):
		return Canceled
	case errors.Is(err, ErrNoKey):
		return NoKey
	case errors.As(err, &syntax), errors.As(err, &typ):
		return Decode
	case errors.As(err, &exit):
		return CLIExit
	}
	return CallFailed
}

// HTTP is a non-2xx reply from op ("" for the model APIs). body is what was read of it. Only
// the provider's error message and request id are shown, redacted and cut short; a body that is
// not a JSON error (a proxy's HTML page, a stack trace) shows nothing but its status.
func HTTP(op string, status int, body string) *Error {
	return &Error{Op: op, Category: fmt.Sprintf("http_%d", status), Status: status, Excerpt: httpExcerpt(body), raw: body}
}

// httpExcerpt rebuilds a JSON error reply from its message, types and request id only, in the
// shape the Anthropic API sends, so a stored error reads the same as before and
// app.ReadAnalysisError still finds its message and request id.
func httpExcerpt(body string) string {
	var in struct {
		Type      string          `json:"type"`
		Error     json.RawMessage `json:"error"`
		Message   string          `json:"message"`
		Detail    json.RawMessage `json:"detail"`
		RequestID string          `json:"request_id"`
		ReqID     string          `json:"requestId"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &in) != nil {
		return ""
	}
	type inner struct {
		Type    string `json:"type,omitempty"`
		Message string `json:"message"`
	}
	var out struct {
		Type      string `json:"type,omitempty"`
		Error     inner  `json:"error"`
		RequestID string `json:"request_id,omitempty"`
	}
	out.Type = word(redact.Text(in.Type))
	var obj struct{ Type, Message string }
	var s string
	switch {
	case json.Unmarshal(in.Error, &obj) == nil && (obj.Message != "" || obj.Type != ""):
		out.Error = inner{Type: word(redact.Text(obj.Type)), Message: obj.Message}
	case json.Unmarshal(in.Error, &s) == nil && s != "":
		out.Error.Message = s
	case in.Message != "":
		out.Error.Message = in.Message
	case json.Unmarshal(in.Detail, &s) == nil && s != "":
		out.Error.Message = s
	default:
		return ""
	}
	out.Error.Message = Clip(redact.Text(out.Error.Message), MaxExcerpt)
	out.RequestID = word(redact.Text(in.RequestID + in.ReqID))
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(out) != nil {
		return ""
	}
	return strings.TrimSpace(b.String())
}

var wordRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,80}$`)

// word is s when it is an identifier-like token (an error type, a request id), else "".
func word(s string) string {
	if wordRE.MatchString(s) {
		return s
	}
	return ""
}

var subtypeRE = regexp.MustCompile(`^[a-z_]{1,40}$`)

// CLIResult is a CLI that ran but reported a failure: claude's "is_error" result with its
// subtype (error_max_turns, error_during_execution). The result text, which can quote the
// prompt, is kept only as Detail.
func CLIResult(op, subtype, result string) *Error {
	if !subtypeRE.MatchString(subtype) {
		subtype = "error"
	}
	return &Error{Op: op, Category: CallFailed, detail: subtype, raw: result}
}

// authMarkers are what the claude and codex CLIs print when their login is missing or expired.
var authMarkers = []string{"/login", "not logged in", "login", "invalid api key", "authentication", "unauthorized", "oauth"}

// CLIExited is a CLI that failed: runErr is what exec returned, ctxErr the call's context error
// (nil while it was live), stderr the tail of what it wrote there.
func CLIExited(op string, runErr, ctxErr error, stderr string) *Error {
	e := &Error{Op: op, Category: CLIExit, raw: stderr, detail: "failed"}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		// "exit status 1" or "signal: terminated": the os package's words, never the CLI's.
		e.Status, e.detail, e.cause = exit.ExitCode(), exit.Error(), exit
	}
	low := strings.ToLower(stderr)
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		e.Category, e.cause = Timeout, ctxErr
	case errors.Is(ctxErr, context.Canceled):
		e.Category, e.cause = Canceled, ctxErr
	default:
		for _, m := range authMarkers {
			if strings.Contains(low, m) {
				e.Category = Auth
				break
			}
		}
	}
	return e
}

// Clip cuts s to at most n runes, the last of them "…" when it was cut.
func Clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

var replyWordRE = regexp.MustCompile(`^[a-z_]{1,32}$`)

// Word is s when it looks like a reply's stop reason or block type (lowercase letters and
// underscores, at most 32), else "other": a reply's metadata goes into stored errors and alerts,
// so only that vocabulary does.
func Word(s string) string {
	if replyWordRE.MatchString(s) {
		return s
	}
	return "other"
}

// Transport is a request to op ("" for the model APIs) that got no HTTP reply, or whose body
// could not be read: the kind of failure and the URL without its query, user info or fragment.
// The underlying error stays as Detail (Go's transport can quote bytes of a malformed reply);
// errors.Is still finds a context error or bounded.ErrTooLarge.
func Transport(op string, err error) *Error {
	e := &Error{Op: op, Category: Network, raw: errString(err), plain: true}
	kind := "network error"
	var dns *net.DNSError
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout():
		e.Category, kind = Timeout, "timed out"
		if errors.Is(err, context.DeadlineExceeded) {
			e.cause = context.DeadlineExceeded
		}
	case errors.Is(err, context.Canceled):
		e.Category, kind, e.cause = Canceled, "canceled", context.Canceled
	case errors.Is(err, bounded.ErrTooLarge):
		kind, e.cause = "reply over the size limit", bounded.ErrTooLarge
	case errors.As(err, &dns):
		kind = "network error: host not found"
	case errors.Is(err, syscall.ECONNREFUSED):
		kind = "network error: connection refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		kind = "network error: connection closed"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		kind += " (" + redact.URL(ue.URL) + ")"
	}
	e.detail = kind
	return e
}

// Undecodable is a reply from op whose JSON could not be read as expected: what was being read, and
// the byte offset where reading stopped. It never quotes the input.
func Undecodable(op, what string, err error) *Error {
	e := &Error{Op: op, Category: Decode, raw: errString(err), detail: "decoding " + what + ": invalid JSON", plain: true}
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		e.Offset = syntax.Offset
	case errors.As(err, &typ):
		e.Offset, e.detail = typ.Offset, "decoding "+what+": unexpected JSON shape"
	}
	return e
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
