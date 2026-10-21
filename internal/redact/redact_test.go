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
