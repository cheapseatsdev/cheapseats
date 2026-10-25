package audit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.215.14": true, "2606:2800:21f:cb07:6820:80da:af6b:8b2c": true,
		"127.0.0.1": false, "::1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "fe80::1": false, "fd00::1": false, "100.64.0.1": false, "0.0.0.0": false,
		"::": false, "224.0.0.1": false, "ff02::1": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false,
		"64:ff9b::a00:1": false, "2002:a00:1::": false, "255.255.255.255": false,
		// The rest of the IANA special-purpose registries that is not globally reachable.
		"192.0.2.1": false, "198.51.100.7": false, "203.0.113.200": false, "192.88.99.1": false,
		"::7f00:1": false, "::ffff:0:a00:1": false, "100:0:0:1::1": false, "2001::1": false,
		"2001:2::1": false, "2001:1ff:ffff::1": false, "3fff::1": false, "5f00::1": false, "fec0::1": false,
		// Just outside them: public.
		"192.0.3.1": true, "198.51.101.1": true, "203.0.114.1": true, "2001:200::1": true, "4000::1": true,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

// A fetch to a private address, directly or by a redirect from a public-looking one, is refused,
// and so is a scheme other than http and https.
func TestFetchRefusesPrivateAddresses(t *testing.T) {
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("reached the private server")
	}))
	defer private.Close()
	f := NewFetcher(nil, 2)
	ctx := context.Background()
	for _, u := range []string{private.URL + "/", "http://169.254.169.254/latest/meta-data/", "http://10.0.0.1/", "http://[::1]/", "http://[::ffff:127.0.0.1]/"} {
		if _, err := f.Get(ctx, u); !errors.Is(err, errBlocked) {
			t.Errorf("%s: %v, want refused", u, err)
		}
	}
	if _, err := f.Get(ctx, "file:///etc/passwd"); err == nil {
		t.Error("a file: URL was fetched")
	}

	// A redirect: the first server is reachable (loopback allowed for it), the target is not.
	restore := AllowLoopbackForTests()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/meta":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			http.Redirect(w, r, "gopher://example.com/", http.StatusFound)
		}
	}))
	defer redirect.Close()
	if _, err := f.Get(ctx, redirect.URL+"/meta"); !errors.Is(err, errBlocked) {
		t.Errorf("redirect to the metadata address: %v, want refused", err)
	}
	if _, err := f.Get(ctx, redirect.URL+"/loop"); err == nil {
		t.Error("followed redirects forever")
	}
	if _, err := f.Get(ctx, redirect.URL+"/other"); err == nil {
		t.Error("followed a redirect to gopher:")
	}
	restore()
	// With loopback refused again, the redirecting server itself is out of reach.
	if _, err := f.Get(ctx, redirect.URL+"/meta"); !errors.Is(err, errBlocked) {
		t.Errorf("loopback after restore: %v", err)
	}
}
