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

// MinValue is the shortest configured secret masked by value. A shorter one is too weak to be a
// real secret and too short to find without masking ordinary text, so it is left as it is.
const MinValue = 4

// Values are the configured secrets' values, plus extra: each comma-separated entry of each of
// Vars, trimmed the way the server trims CHEAPSEATS_TOKENS, so an entry is masked exactly as
// it is accepted.
func Values(extra ...string) []string {
	for _, k := range Vars {
		for _, v := range strings.Split(os.Getenv(k), ",") {
			extra = append(extra, strings.TrimSpace(v))
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
	// AWS access key ids, bare, and AWS_ACCESS_KEY_ID=... (AWS_SECRET_ACCESS_KEY is env-style below)
	regexp.MustCompile(`()\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`(\bAWS_ACCESS_KEY_ID["']?\s*[=:]\s*["']?)[^\s"',;&]+`),
	// ANTHROPIC_API_KEY=..., GITHUB_TOKEN: ..., DB_PASSWORD=... (env-style names)
	regexp.MustCompile(`(\b[A-Z][A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIALS?)["']?\s*[=:]\s*["']?)[^\s"',;&]+`),
	// key=..., api_key: ..., "x-api-key":"...", access_token=..., password=...
	regexp.MustCompile(`(?i)(\b(?:x-)?(?:api[_-]?key|key|access[_-]?token|auth[_-]?token|refresh[_-]?token|token|secret|client[_-]?secret|password|passwd|authorization)["']?\s*[=:]\s*["']?)[^\s"',;&]+`),
}

// Text is s with every configured secret (and values) masked as [REDACTED], in its plain,
// URL-escaped, base64, shell-quoted and JSON-escaped forms, and every credential-shaped string.
// A value is masked where it stands as a whole token, not inside a longer word, so a short
// secret doesn't mask ordinary text; values shorter than MinValue are not masked by value.
func Text(s string, values ...string) string {
	var forms []string
	for _, v := range Values(values...) {
		if len(v) < MinValue {
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
		s = maskToken(s, v)
	}
	for _, p := range patterns {
		s = p.ReplaceAllString(s, "${1}[REDACTED]")
	}
	return s
}

// maskToken replaces each occurrence of v in s that no letter or digit touches on either side.
func maskToken(s, v string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, v)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(v)
		if wordByte(s, i-1) || wordByte(s, end) {
			b.WriteString(s[:i+1])
			s = s[i+1:]
			continue
		}
		b.WriteString(s[:i] + "[REDACTED]")
		s = s[end:]
	}
}

func wordByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// URL is u without what can carry a credential: its user info, query and fragment. A string
// that doesn't parse as a URL is masked by Text instead.
func URL(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" && p.Scheme == "" {
		return Text(u)
	}
	p.User, p.RawQuery, p.ForceQuery, p.Fragment, p.RawFragment = nil, "", false, "", ""
	return p.String()
}
