package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestLogRedactsShareTokens(t *testing.T) {
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	for _, c := range []struct{ path, want string }{
		{"/share/synthetic-bearer_token", "/share/[redacted]"},
		{"/share/invalid/token", "/share/[redacted]"},
		{"/share/", "/share/[redacted]"},
		{"/share/%73ynthetic-bearer_token", "/share/[redacted]"},
		{"/runs/example?token=synthetic-query", "/runs/example"},
		{"/static/app.css", ""},
	} {
		output.Reset()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", c.path, nil))
		if c.want == "" {
			if output.Len() != 0 {
				t.Error("static request was logged")
			}
			continue
		}
		var entry struct{ Path string }
		if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Path != c.want {
			t.Errorf("logged path = %q, want %q", entry.Path, c.want)
		}
	}
}
