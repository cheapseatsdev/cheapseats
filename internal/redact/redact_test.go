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

// A configured secret is masked by value from MinValue characters up, as a whole token only, so
// a short one doesn't eat ordinary words. CHEAPSEATS_TOKENS entries are trimmed as the server
// trims them.
func TestShortConfiguredSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "hunter2")
	t.Setenv("CHEAPSEATS_TOKENS", " ab12 , xyz,")
	for in, want := range map[string]string{
		"pass hunter2 failed":       "pass [REDACTED] failed",
		"cookie=ab12; then":         "cookie=[REDACTED]; then",
		"hunter22 and xab12 stay":   "hunter22 and xab12 stay",
		"xyz is too short to count": "xyz is too short to count",
	} {
		if got := Text(in); got != want {
			t.Errorf("%q\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestAWSKeysAndURLs(t *testing.T) {
	for in, want := range map[string]string{
		"id AKIAIOSFODNN7EXAMPLE used":                "id [REDACTED] used",
		"ASIAIOSFODNN7EXAMPLE":                        "[REDACTED]",
		"AWS_ACCESS_KEY_ID=AKIDEXAMPLE1":              "AWS_ACCESS_KEY_ID=[REDACTED]",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG": "AWS_SECRET_ACCESS_KEY=[REDACTED]",
	} {
		if got := Text(in); got != want {
			t.Errorf("%q\n got %q\nwant %q", in, got, want)
		}
	}
	if got := URL("https://user:pw@example.com/p/a?api_key=abc&x=1#frag"); got != "https://example.com/p/a" {
		t.Errorf("URL = %q", got)
	}
}

// A URL url.Parse refuses still loses its user info, query and fragment, by text.
func TestUnparseableURLs(t *testing.T) {
	for in, want := range map[string]string{
		"https://operator:p%ssword@proxy.example/v1/messages":      "https://proxy.example/v1/messages",
		"https://operator:p%ssword@proxy.example":                  "https://proxy.example",
		"https://a@b:p%zz@proxy.example/x?access_token=s#f":        "https://proxy.example/x",
		"https://login.example/%zz?access_token=opaqueSecret#frag": "https://login.example/%zz",
		"https://h.example/p@x/%zz":                                "https://h.example/p@x/%zz",
	} {
		if got := URL(in); got != want {
			t.Errorf("URL(%q) = %q, want %q", in, got, want)
		}
	}
}
