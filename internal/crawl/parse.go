// Package crawl imports web server and CDN access logs and counts what matters to AI visibility:
// fetches by AI crawlers (by user agent: assistant, search, training, search engine), visits
// from people who followed a link in an assistant's answer (by Referer or utm_source), and other
// people's visits (by a browser's user agent), the denominator for those. It keeps daily counts
// per page, not the log lines.
//
// A crawler is known only by the user agent it sends, which anyone can copy, so every count is
// "unverified (UA only)". Checking a fetch's IP against the operator's published ranges is left
// out for now.
package crawl

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Hit is one request from a log line, in whichever format it came.
type Hit struct {
	Time    time.Time
	IP      string
	Host    string // empty when the format doesn't record it (common and combined logs)
	Target  string // the request target: a path with its query, or an absolute URL
	Status  int
	UA      string
	Referer string
}

// Formats Parse reads, for messages.
const Formats = "combined or common log format (nginx, Apache, with or without a leading virtual host), Caddy's JSON access log, or Cloudflare Logpush JSON"

// combined matches the NCSA common and combined log formats, optionally led by a virtual host
// (Apache's vhost_combined, "%v:%p ...").
var combined = regexp.MustCompile(`^(?:(\S+) )?(\S+) \S+ \S+ \[([^\]]+)\] "(?:\S+) (\S+)[^"]*" (\d{3}) \S+(?: "((?:[^"\\]|\\.)*)" "((?:[^"\\]|\\.)*)")?`)

// Parse reads one log line in any of the formats; false for a line it can't read.
func Parse(line string) (Hit, bool) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "{") {
		return parseJSON(line)
	}
	m := combined.FindStringSubmatch(line)
	if m == nil {
		return Hit{}, false
	}
	t, err := time.Parse("02/Jan/2006:15:04:05 -0700", m[3])
	if err != nil {
		return Hit{}, false
	}
	status, _ := strconv.Atoi(m[5])
	h := Hit{Time: t, Host: m[1], IP: m[2], Target: m[4], Status: status, Referer: unescape(m[6]), UA: unescape(m[7])}
	if h.Referer == "-" {
		h.Referer = ""
	}
	return h, true
}

// unescape undoes the log's escaping of quotes and backslashes (\" and nginx's \x22).
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	return strings.NewReplacer(`\"`, `"`, `\x22`, `"`, `\\`, `\`).Replace(s)
}

// caddyLine is Caddy's JSON access log (the http.log.access logger).
type caddyLine struct {
	TS      json.RawMessage `json:"ts"`
	Status  int             `json:"status"`
	Request *struct {
		RemoteIP string              `json:"remote_ip"`
		ClientIP string              `json:"client_ip"`
		Host     string              `json:"host"`
		URI      string              `json:"uri"`
		Headers  map[string][]string `json:"headers"`
	} `json:"request"`
}

// cloudflareLine is a Cloudflare Logpush http_requests record.
type cloudflareLine struct {
	UA      *string         `json:"ClientRequestUserAgent"`
	URI     string          `json:"ClientRequestURI"`
	Host    string          `json:"ClientRequestHost"`
	Start   json.RawMessage `json:"EdgeStartTimestamp"`
	IP      string          `json:"ClientIP"`
	Status  int             `json:"EdgeResponseStatus"`
	Referer string          `json:"ClientRequestReferer"`
}

func parseJSON(line string) (Hit, bool) {
	var cf cloudflareLine
	if json.Unmarshal([]byte(line), &cf) == nil && cf.UA != nil {
		t, ok := timestamp(cf.Start)
		if !ok {
			return Hit{}, false
		}
		return Hit{Time: t, IP: cf.IP, Host: cf.Host, Target: cf.URI, Status: cf.Status, UA: *cf.UA, Referer: cf.Referer}, true
	}
	var c caddyLine
	if json.Unmarshal([]byte(line), &c) != nil || c.Request == nil {
		return Hit{}, false
	}
	t, ok := timestamp(c.TS)
	if !ok {
		return Hit{}, false
	}
	header := func(k string) string {
		for name, v := range c.Request.Headers {
			if strings.EqualFold(name, k) && len(v) > 0 {
				return v[0]
			}
		}
		return ""
	}
	ip := c.Request.ClientIP
	if ip == "" {
		ip = c.Request.RemoteIP
	}
	return Hit{Time: t, IP: ip, Host: c.Request.Host, Target: c.Request.URI, Status: c.Status, UA: header("User-Agent"), Referer: header("Referer")}, true
}

// timestamp reads a JSON time: Unix seconds (Caddy's default, fractional), milliseconds or
// nanoseconds (Cloudflare's default), or an RFC 3339 string.
func timestamp(raw json.RawMessage) (time.Time, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t, true
		}
		return time.Time{}, false
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || f <= 0 {
		return time.Time{}, false
	}
	switch {
	case f > 1e17: // nanoseconds
		var n int64
		if json.Unmarshal(raw, &n) == nil {
			return time.Unix(0, n), true
		}
		return time.Unix(0, int64(f)), true
	case f > 1e11: // milliseconds
		return time.UnixMilli(int64(f)), true
	}
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9)), true
}

// split is a request target's host (when absolute), path and query.
func split(target string) (host, path string, q url.Values) {
	u, err := url.Parse(target)
	if err != nil {
		p, _, _ := strings.Cut(target, "?")
		return "", p, nil
	}
	return u.Host, u.Path, u.Query()
}
