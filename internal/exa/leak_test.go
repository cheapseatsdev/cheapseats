package exa

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

// Exa's error reply used to go into the error verbatim, unredacted: a key it echoed, or the
// text of a page it read. Now only its message shows, with secrets masked.
func TestExaErrorReplyLeaksNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"requestId":"r9","error":"invalid key sk-ant-api03-SENTINELkey0123456789","echo":"Bearer SENTINELbearer0123 PAGETEXT-SENTINEL"}`))
	}))
	defer srv.Close()
	t.Setenv(KeyVar, "SENTINELexakey-0123456789")
	c := &Client{URL: srv.URL, Cfg: &config.Config{Pricing: map[string]config.Pricing{ContentsModel: {PagePer1K: 1}}}}
	_, err := c.Contents(context.Background(), "t", []string{"https://a.example/p"})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, v := range []string{"SENTINELkey", "SENTINELbearer", "PAGETEXT-SENTINEL", "SENTINELexakey"} {
		if strings.Contains(err.Error(), v) {
			t.Errorf("%q carries %q", err, v)
		}
	}
	if want := `exa: HTTP 401: {"error":{"message":"invalid key [REDACTED]"},"request_id":"r9"}`; err.Error() != want {
		t.Errorf("got  %s\nwant %s", err, want)
	}
}
