package main

import (
	"os"
	"strings"
	"testing"
)

// A .env sets keys and settings, but never a variable that would send the real environment's API
// keys elsewhere or run code: those are ignored, each with a warning. The real environment wins
// over .env either way.
func TestDotEnvRefusesRedirects(t *testing.T) {
	names := []string{"ANTHROPIC_API_KEY", "CHEAPSEATS_DATA_DIR", "CHEAPSEATS_ANTHROPIC_BASE_URL", "CHEAPSEATS_EXA_BASE_URL",
		"CHEAPSEATS_CLI_ENV", "HTTPS_PROXY", "https_proxy", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES",
		"NODE_OPTIONS", "PYTHONSTARTUP", "BASH_ENV", "CHEAPSEATS_TOKENS"}
	for _, n := range names {
		t.Setenv(n, "")
		os.Unsetenv(n)
	}
	t.Setenv("CHEAPSEATS_TOKENS", "real")
	var file strings.Builder
	for _, n := range names {
		file.WriteString("export " + n + "=\"from-dotenv\"\n")
	}
	var warn strings.Builder
	dotenvApply(".env", []byte(file.String()), &warn)

	for _, n := range []string{"ANTHROPIC_API_KEY", "CHEAPSEATS_DATA_DIR"} {
		if os.Getenv(n) != "from-dotenv" {
			t.Errorf("%s not set from .env", n)
		}
	}
	if os.Getenv("CHEAPSEATS_TOKENS") != "real" {
		t.Error(".env overrode the real environment")
	}
	for _, n := range names[2 : len(names)-1] {
		if v, set := os.LookupEnv(n); set {
			t.Errorf("%s=%s taken from .env", n, v)
		}
		if !strings.Contains(warn.String(), "ignoring "+n+" from .env") {
			t.Errorf("no warning for %s:\n%s", n, warn.String())
		}
	}
	if strings.Contains(warn.String(), "ANTHROPIC_API_KEY") || strings.Contains(warn.String(), "CHEAPSEATS_TOKENS") {
		t.Errorf("warned about a line it may take:\n%s", warn.String())
	}
}
