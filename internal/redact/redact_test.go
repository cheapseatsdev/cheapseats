package redact

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"
)

func TestKnownSecretEncodings(t *testing.T) {
	secret := "fake-secret-'quote\"-line\nvalue"
	j, _ := json.Marshal(secret)
	for _, value := range []string{secret, string(j[1 : len(j)-1]), url.QueryEscape(secret), base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawStdEncoding.EncodeToString([]byte(secret)), base64.URLEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret))} {
		got := Text("before "+value+" after", secret)
		if got != "before [REDACTED] after" {
			t.Fatalf("redaction failed: %q", got)
		}
	}
}

func TestBase64TransportVariants(t *testing.T) {
	secret := "~~~fake-transport-secret-x"
	if len(secret)%3 == 0 {
		secret += "x"
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		v := enc.EncodeToString([]byte(secret))
		if Text(v, secret) != "[REDACTED]" {
			t.Fatalf("encoded credential not removed: %s", v)
		}
	}
}

// Credentials nobody configured here (a key in an echoed request, a token in a CLI's stderr)
// are masked by their shape, keeping the name so the text still says what was there.
func TestCredentialShapes(t *testing.T) {
	for in, want := range map[string]string{
		"key sk-ant-api03-abcDEF_123-xyz rejected":    "key [REDACTED] rejected",
		"sk-proj-abcdef123456":                        "[REDACTED]",
		"Authorization: Bearer eyJhbGciOi.J9.abc":     "Authorization: [REDACTED] [REDACTED]",
		"authorization bearer abcdef123456":           "authorization bearer [REDACTED]",
		"ANTHROPIC_API_KEY=abc123 next":               "ANTHROPIC_API_KEY=[REDACTED] next",
		"GITHUB_TOKEN: ghp_abcdef":                    "GITHUB_TOKEN: [REDACTED]",
		`{"x-api-key":"abc123def","model":"m"}`:       `{"x-api-key":"[REDACTED]","model":"m"}`,
		"https://h/p?api_key=abc123&q=1":              "https://h/p?api_key=[REDACTED]&q=1",
		"password=hunter2; token: t0k3n":              "password=[REDACTED]; token: [REDACTED]",
		"the monkey ate a sketch-ant, at 4 tokens/s.": "the monkey ate a sketch-ant, at 4 tokens/s.",
	} {
		if got := Text(in); got != want {
			t.Errorf("%q\n got %q\nwant %q", in, got, want)
		}
	}
}
