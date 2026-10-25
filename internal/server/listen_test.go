package server

import (
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// An open Cheap Seats listens only on loopback, decided from the address itself (not a prefix of
// it) and from the tokens as parsed, so a setting that holds no token can't leave it open.
func TestOpenOnlyOnLoopback(t *testing.T) {
	t.Setenv("CHEAPSEATS_AUTH_HEADER", "")
	t.Setenv("CHEAPSEATS_READONLY", "")
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	for _, tokens := range []string{",", " , ", " "} {
		t.Setenv("CHEAPSEATS_TOKENS", tokens)
		if _, err := New(newApp(t, testConfig(t), d)); err == nil {
			t.Errorf("CHEAPSEATS_TOKENS=%q started with no token", tokens)
		}
	}
	t.Setenv("CHEAPSEATS_TOKENS", "")
	t.Setenv("CHEAPSEATS_AUTH_HEADER", "  ")
	if _, err := New(newApp(t, testConfig(t), d)); err == nil {
		t.Error("a blank CHEAPSEATS_AUTH_HEADER started")
	}
	t.Setenv("CHEAPSEATS_AUTH_HEADER", "")
	open, err := New(newApp(t, testConfig(t), d))
	if err != nil {
		t.Fatal(err)
	}
	for addr, ok := range map[string]bool{
		"127.0.0.1:3000": true, "127.0.0.2:3000": true, "[::1]:3000": true, "localhost:3000": true, "LOCALHOST:3000": true,
		"localhost.example:3000": false, "127.0.0.1.example:3000": false, "0.0.0.0:3000": false, ":3000": false, "[::]:3000": false, "10.0.0.1:3000": false, "127.0.0.1": false,
	} {
		if err := open.CheckListen(addr); (err == nil) != ok {
			t.Errorf("open on %s: %v", addr, err)
		}
	}
	t.Setenv("CHEAPSEATS_TOKENS", "tok,")
	signed, err := New(newApp(t, testConfig(t), d))
	if err != nil {
		t.Fatal(err)
	}
	if err := signed.CheckListen(":3000"); err != nil {
		t.Errorf("with a token: %v", err)
	}
}
