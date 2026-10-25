package main

import (
	"os"
	"strings"
	"testing"
)

// A .env sets keys and settings, but never a variable that would send the real environment's API
// keys elsewhere, run code or decide who signs in by a header: those are ignored, each with a
// warning. Tokens stay allowed (the documented local setup), and the real environment wins over
// .env either way.
func TestDotEnvRefusesRedirects(t *testing.T) {
	allowed := []string{"ANTHROPIC_API_KEY", "CHEAPSEATS_TOKENS"}
	refused := []string{"CHEAPSEATS_ANTHROPIC_BASE_URL", "CHEAPSEATS_EXA_BASE_URL", "CHEAPSEATS_CLI_ENV", "HTTPS_PROXY", "https_proxy",
		"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "NODE_OPTIONS", "PYTHONSTARTUP", "BASH_ENV",
		"CHEAPSEATS_AUTH_HEADER", "CHEAPSEATS_ALLOWED_EMAILS"}
	names := append(append([]string{"CHEAPSEATS_DATA_DIR"}, allowed...), refused...)
	for _, n := range names {
		t.Setenv(n, "")
		os.Unsetenv(n)
	}
	t.Setenv("CHEAPSEATS_DATA_DIR", "real")
	var file strings.Builder
	for _, n := range names {
		file.WriteString("export " + n + "=\"from-dotenv\"\n")
	}
	var warn strings.Builder
	dotenvApply(".env", []byte(file.String()), &warn)

	for _, n := range allowed {
		if os.Getenv(n) != "from-dotenv" {
			t.Errorf("%s not set from .env", n)
		}
		if strings.Contains(warn.String(), n) {
			t.Errorf("warned about %s, which .env may set:\n%s", n, warn.String())
		}
	}
	if os.Getenv("CHEAPSEATS_DATA_DIR") != "real" {
		t.Error(".env overrode the real environment")
	}
	for _, n := range refused {
		if v, set := os.LookupEnv(n); set {
			t.Errorf("%s=%s taken from .env", n, v)
		}
		if !strings.Contains(warn.String(), "ignoring "+n+" from .env") {
			t.Errorf("no warning for %s:\n%s", n, warn.String())
		}
	}
}
