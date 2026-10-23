package server

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The brand tokens are a generated copy of cheapseats-site brand/web/tokens.css (deploy/sync-brand.sh).
// A hand edit here would drift from the site, so the copy must carry the sync header; and a style,
// template, script or icon that uses a --cs-* property the copy does not define would render with
// no color at all, so every property used must be defined.
func TestTokensSynced(t *testing.T) {
	tokens, err := assets.ReadFile("static/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(tokens), "/* GENERATED: synced from cheapseats-site brand/web/tokens.css at ") {
		t.Fatalf("static/tokens.css lacks the sync header; run deploy/sync-brand.sh, do not edit it by hand:\n%s", limit(string(tokens)))
	}
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`(--cs-[a-z0-9-]+)\s*:`).FindAllStringSubmatch(string(tokens), -1) {
		defined[m[1]] = true
	}
	if len(defined) < 20 {
		t.Fatalf("only %d tokens defined in static/tokens.css", len(defined))
	}

	used := regexp.MustCompile(`var\(\s*(--cs-[a-z0-9-]+)`)
	var missing []string
	for _, sys := range []fs.FS{assets, iconFiles, meterFiles} {
		fs.WalkDir(sys, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || path == "static/tokens.css" {
				return err
			}
			b, err := fs.ReadFile(sys, path)
			if err != nil {
				return err
			}
			for _, m := range used.FindAllStringSubmatch(string(b), -1) {
				if !defined[m[1]] {
					missing = append(missing, path+": "+m[1])
				}
			}
			return nil
		})
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("properties used but not defined in static/tokens.css:\n%s", strings.Join(missing, "\n"))
	}
}
