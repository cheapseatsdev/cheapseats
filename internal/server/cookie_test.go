package server

import (
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"
)

// The sign-in cookie is the token, so it's Secure whenever people reach Cheap Seats over https,
// including behind a proxy that terminates TLS, where the request itself arrives as plain http.
func TestSignInCookieSecureOverHTTPS(t *testing.T) {
	for _, c := range []struct {
		name, publicURL, proto string
		tls, secure            bool
	}{
		{"plain local", "", "", false, false},
		{"tls", "", "", true, true},
		{"proxy says https", "", "https", false, true},
		{"public url https", "https://seats.example", "", false, true},
		{"public url http", "http://seats.example", "", false, false},
	} {
		s := &Server{tokens: []string{"tok"}, cookie: "cheapseats_token_x", publicURL: c.publicURL}
		req := httptest.NewRequest("POST", "/login", strings.NewReader("token=tok"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c.proto != "" {
			req.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if c.tls {
			req.TLS = &tls.ConnectionState{}
		}
		rec := httptest.NewRecorder()
		s.login(rec, req)
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Secure != c.secure {
			t.Errorf("%s: cookies %+v, want Secure=%v", c.name, cookies, c.secure)
		}
	}
}
