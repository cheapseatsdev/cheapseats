package audit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"syscall"
	"time"
)

// The URLs fetched come from config, from MCP callers and, for the citation check, from model
// answers, so a fetch must never reach the machine Cheap Seats runs on or its network: a cloud
// metadata address, a database on loopback, an admin panel on 10/8. The check runs on the address
// actually dialed, after DNS, so a redirect or a name that resolves to one is refused too.

// blocked are the ranges no fetch may reach, beyond what netip's own predicates cover: every
// entry in the IANA IPv4 and IPv6 special-purpose address registries that is not globally
// reachable, plus the deprecated ones. 2001::/23 is blocked whole: its few globally reachable
// assignments (AS112, AMT, ORCHIDv2, drone remote ID) never serve a web page.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT (and Tailscale)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation (TEST-NET-1)
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast, deprecated
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation (TEST-NET-2)
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation (TEST-NET-3)
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, and broadcast
	netip.MustParsePrefix("::/96"),           // IPv4-compatible, deprecated: an IPv4 address in disguise
	netip.MustParsePrefix("::ffff:0:0:0/96"), // IPv4-translated (SIIT), likewise
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64, likewise
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("100::/64"),        // discard
	netip.MustParsePrefix("100:0:0:1::/64"),  // dummy prefix
	netip.MustParsePrefix("2001::/23"),       // IETF protocol assignments, Teredo (2001::/32) included
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4: an IPv4 address in disguise
	netip.MustParsePrefix("3fff::/20"),       // documentation
	netip.MustParsePrefix("5f00::/16"),       // SRv6 segment identifiers
	netip.MustParsePrefix("fec0::/10"),       // site-local, deprecated
}

// testLoopback lets tests reach httptest servers on 127.0.0.1; nothing else sets it.
var testLoopback atomic.Bool

// AllowLoopbackForTests lets fetches reach loopback (httptest servers) until the returned func is
// called. For tests only: production code never calls it.
func AllowLoopbackForTests() (restore func()) {
	testLoopback.Store(true)
	return func() { testLoopback.Store(false) }
}

// errBlocked is a fetch refused for where it would go.
var errBlocked = errors.New("refused: not a public address")

// The guard's other refusals. Each is fixed text, so the fetcher can say why a fetch was refused
// without Go's error text around it (refusals).
var (
	errTooManyRedirects = fmt.Errorf("more than %d redirects", maxRedirects)
	errRedirectScheme   = errors.New("redirect to a URL that isn't http or https")
)

// refusals are the guard's errors, which the fetcher returns as they are.
var refusals = []error{errBlocked, errTooManyRedirects, errRedirectScheme}

// publicAddr reports whether an address is on the public internet.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap() // ::ffff:127.0.0.1 is 127.0.0.1
	if a.IsLoopback() && testLoopback.Load() {
		return true
	}
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// guard is a Dialer.Control that refuses any address that isn't public.
func guard(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w (%s)", errBlocked, address)
	}
	if !publicAddr(ap.Addr()) {
		return fmt.Errorf("%w (%s)", errBlocked, ap.Addr())
	}
	return nil
}

// maxRedirects is how many redirects a fetch follows.
const maxRedirects = 5

// safeClient fetches public http and https URLs only, following at most a few redirects, and never
// through a proxy (which would dial on its behalf, unchecked).
func safeClient() *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: guard}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           func(ctx context.Context, n, a string) (net.Conn, error) { return d.DialContext(ctx, n, a) },
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errTooManyRedirects
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errRedirectScheme
			}
			return nil
		},
	}
}
