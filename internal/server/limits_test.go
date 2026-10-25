package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/store"
)

// A request body past the cap isn't read: a JSON call fails instead of decoding it, and sign-in,
// which anyone can reach, takes only a few kilobytes.
func TestRequestBodiesAreCapped(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "fake")
	t.Setenv("CHEAPSEATS_TOKENS", "tok")
	t.Setenv("CHEAPSEATS_AUTH_HEADER", "")
	d, _ := store.Open(t.TempDir())
	defer d.Close()
	s, err := New(newApp(t, testConfig(t), d))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	dryRun := func(pad int) int {
		req := httptest.NewRequest("POST", "/api/runs", strings.NewReader(`{"pad":"`+strings.Repeat("x", pad)+`","set":"fix-ai-recommendations","dry_run":true}`))
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := dryRun(10); code != 200 {
		t.Fatalf("small dry run: %d", code)
	}
	if code := dryRun(maxBody); code != 400 {
		t.Errorf("dry run past the cap: %d, want 400", code)
	}
	login := func(pad int) int {
		req := httptest.NewRequest("POST", "/login", strings.NewReader("token=tok&pad="+strings.Repeat("x", pad)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := login(10); code != 303 {
		t.Fatalf("sign-in: %d", code)
	}
	if code := login(maxLoginBody); code == 303 {
		t.Error("a sign-in past its cap was read")
	}
}
