// Package redact masks API keys and tokens in text: the configured ones by value, including
// their encoded forms, and anything shaped like a credential (an sk- key, a bearer token, a
// key=value or "api_key": "value" pair, an env-style SECRET=value) by pattern. It is the one
// redactor: errclass excerpts and the analyst's reply excerpt in the log both go through it.
package redact

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Vars are the environment variables whose values are secrets.
var Vars = []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CHEAPSEATS_TOKENS", "EXA_API_KEY", "TYPESAFE_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CODEX_API_KEY"}

// Values are the configured secrets' values, plus extra.
func Values(extra ...string) []string {
	for _, k := range Vars {
		for _, v := range strings.Split(os.Getenv(k), ",") {
			extra = append(extra, v)
		}
	}
	return extra
}

// patterns are credential shapes, masked whatever their value. Each keeps its first group (the
// name, so the text still says what was there) and masks the rest.
var patterns = []*regexp.Regexp{
	// Anthropic, OpenAI and similar keys: sk-ant-..., sk-proj-..., sk-...
	regexp.MustCompile(`()\bsk-[A-Za-z0-9_-]{6,}`),
	// Authorization: Bearer <token>, Basic <credentials>
	regexp.MustCompile(`(?i)(\b(?:bearer|basic)\s+)[A-Za-z0-9._~+/=-]{6,}`),
	// ANTHROPIC_API_KEY=..., GITHUB_TOKEN: ..., DB_PASSWORD=... (env-style names)
	regexp.MustCompile(`(\b[A-Z][A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIALS?)["']?\s*[=:]\s*["']?)[^\s"',;&]+`),
	// key=..., api_key: ..., "x-api-key":"...", access_token=..., password=...
	regexp.MustCompile(`(?i)(\b(?:x-)?(?:api[_-]?key|key|access[_-]?token|auth[_-]?token|refresh[_-]?token|token|secret|client[_-]?secret|password|passwd|authorization)["']?\s*[=:]\s*["']?)[^\s"',;&]+`),
}

// Text is s with every configured secret (and values) masked as [REDACTED], in its plain,
// URL-escaped, base64, shell-quoted and JSON-escaped forms, and every credential-shaped string.
func Text(s string, values ...string) string {
	var forms []string
	for _, v := range Values(values...) {
		// A value this short is no real key, and masking it would mask ordinary text: a key
		// set to "x" in a test would mask every x.
		if len(v) < 8 {
			continue
		}
		forms = append(forms, v, url.QueryEscape(v), url.PathEscape(v), base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v)), base64.URLEncoding.EncodeToString([]byte(v)), base64.RawURLEncoding.EncodeToString([]byte(v)), strings.ReplaceAll(v, "'", `'\''`))
		j, _ := json.Marshal(v)
		escaped := string(j[1 : len(j)-1])
		forms = append(forms, escaped)
		j, _ = json.Marshal(escaped)
		forms = append(forms, string(j[1:len(j)-1]))
	}
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	for _, v := range forms {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	for _, p := range patterns {
		s = p.ReplaceAllString(s, "${1}[REDACTED]")
	}
	return s
}
