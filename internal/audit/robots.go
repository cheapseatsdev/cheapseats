package audit

import (
	"bufio"
	"strings"
)

// Robots is a parsed robots.txt (RFC 9309): groups of rules by user agent, and the sitemaps it
// lists.
type Robots struct {
	groups   []group
	Sitemaps []string
}

type group struct {
	agents []string // lowercased product tokens
	rules  []rule
}

type rule struct {
	allow bool
	path  string
}

// ParseRobots reads robots.txt. Unknown lines and malformed ones are skipped, as crawlers do.
func ParseRobots(body string) *Robots {
	r := &Robots{}
	var cur *group
	inAgents := false // consecutive user-agent lines share one group
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "user-agent":
			if !inAgents || cur == nil {
				r.groups = append(r.groups, group{})
				cur = &r.groups[len(r.groups)-1]
			}
			cur.agents = append(cur.agents, strings.ToLower(v))
			inAgents = true
		case "allow", "disallow":
			inAgents = false
			if cur == nil {
				continue // rules before any user-agent line belong to nobody
			}
			if k == "disallow" && v == "" {
				continue // "Disallow:" with nothing allows everything
			}
			cur.rules = append(cur.rules, rule{allow: k == "allow", path: v})
		case "sitemap":
			if v != "" {
				r.Sitemaps = append(r.Sitemaps, v)
			}
		default:
			inAgents = false
		}
	}
	return r
}

// rulesFor is the rules that apply to a crawler with this product token: those of every group
// naming it, else those of the * groups. Explicit is whether a group named it.
func (r *Robots) rulesFor(token string) (rules []rule, explicit bool) {
	t := strings.ToLower(token)
	var star []rule
	for _, g := range r.groups {
		for _, a := range g.agents {
			if a == t {
				rules, explicit = append(rules, g.rules...), true
			} else if a == "*" {
				star = append(star, g.rules...)
			}
		}
	}
	if explicit {
		return rules, true
	}
	return star, false
}

// Allowed reports whether a crawler with this product token may fetch path: the longest matching
// rule wins, and allow wins a tie. Explicit is whether robots.txt names the crawler itself rather
// than only *.
func (r *Robots) Allowed(token, path string) (allowed, explicit bool) {
	if r == nil {
		return true, false
	}
	if path == "" {
		path = "/"
	}
	rules, explicit := r.rulesFor(token)
	best, allowed := -1, true
	for _, x := range rules {
		if !matchRule(x.path, path) {
			continue
		}
		if n := len(x.path); n > best || n == best && x.allow {
			best, allowed = n, x.allow
		}
	}
	return allowed, explicit
}

// matchRule matches a rule's path pattern against a path: a prefix, where * is any run of
// characters and a trailing $ anchors the end.
func matchRule(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	pattern = strings.TrimSuffix(pattern, "$")
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	rest := path[len(parts[0]):]
	for i, p := range parts[1:] {
		last := i == len(parts)-2
		if last && anchored {
			return strings.HasSuffix(rest, p)
		}
		j := strings.Index(rest, p)
		if j < 0 {
			return false
		}
		rest = rest[j+len(p):]
	}
	return !anchored || rest == ""
}
