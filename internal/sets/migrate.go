package sets

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Migrate rewrites every set in dir that still lists prompts as plain strings into [[prompts]]
// tables, giving each prompt an id made from its words. The rest of each file is left as it was.
// Ids are unique across the directory; review them before merging, since they're permanent.
// Only regular files are read or written: a set that is a symlink (or anything else) is an error,
// so a checkout cannot point the rewrite at a file outside dir. Each rewrite goes to a temporary
// file beside the set and is renamed over it, so a failed write leaves the set as it was.
// Returns the files it changed.
func Migrate(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	taken := map[string]bool{}
	type pending struct {
		file    string
		mode    os.FileMode
		src     string
		prompts []string
	}
	var todo []pending
	for _, f := range files {
		b, mode, err := readRegular(f)
		if err != nil {
			return nil, err
		}
		var old struct {
			Prompts any `toml:"prompts"`
		}
		if _, err := toml.Decode(string(b), &old); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		list, ok := old.Prompts.([]any)
		if !ok || len(list) == 0 {
			continue
		}
		if _, isTable := list[0].(map[string]any); isTable {
			for _, x := range list { // already migrated: its ids are taken
				if id, ok := x.(map[string]any)["id"].(string); ok {
					taken[id] = true
				}
			}
			continue
		}
		var texts []string
		for _, x := range list {
			if t, ok := x.(string); ok && strings.TrimSpace(t) != "" {
				texts = append(texts, strings.TrimSpace(t))
			}
		}
		todo = append(todo, pending{f, mode, string(b), texts})
	}
	var changed []string
	for _, p := range todo {
		out, err := rewrite(p.src, p.prompts, taken)
		if err != nil {
			return changed, fmt.Errorf("%s: %w", filepath.Base(p.file), err)
		}
		if _, err := toml.Decode(out, &Set{}); err != nil {
			return changed, fmt.Errorf("%s: the rewrite doesn't parse: %w", filepath.Base(p.file), err)
		}
		if err := replace(p.file, []byte(out), p.mode); err != nil {
			return changed, err
		}
		changed = append(changed, p.file)
	}
	return changed, nil
}

// readRegular reads f only if it is a regular file, checked without following a symlink and
// again on the open file, so a swap between the check and the open is caught too.
func readRegular(f string) ([]byte, os.FileMode, error) {
	st, err := os.Lstat(f)
	if err != nil {
		return nil, 0, err
	}
	if !st.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s: not a regular file (%s); migrate-config only rewrites plain files", filepath.Base(f), st.Mode().Type())
	}
	h, err := os.Open(f)
	if err != nil {
		return nil, 0, err
	}
	defer h.Close()
	if hs, err := h.Stat(); err != nil || !os.SameFile(st, hs) {
		return nil, 0, fmt.Errorf("%s: changed while it was being read", filepath.Base(f))
	}
	b, err := io.ReadAll(h)
	return b, st.Mode().Perm(), err
}

// replace writes b to a new file beside f and renames it over f. The rename replaces the path
// itself, never what a symlink there would point to.
func replace(f string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(f), "."+filepath.Base(f)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), f)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

var promptsKey = regexp.MustCompile(`(?m)^prompts\s*=\s*\[`)
var tableHeader = regexp.MustCompile(`(?m)^\s*\[`)

// rewrite takes the `prompts = [...]` array out of src and puts [[prompts]] tables before the
// first table, where top-level keys end.
func rewrite(src string, prompts []string, taken map[string]bool) (string, error) {
	loc := promptsKey.FindStringIndex(src)
	if loc == nil {
		return "", fmt.Errorf("can't find `prompts = [`")
	}
	end := arrayEnd(src, loc[1])
	if end < 0 {
		return "", fmt.Errorf("can't find the end of the prompts array")
	}
	for end < len(src) && src[end] != '\n' {
		end++
	}
	if end < len(src) {
		end++
	}
	rest := src[:loc[0]] + src[end:]
	var b strings.Builder
	for _, t := range prompts {
		id := promptID(t, taken)
		fmt.Fprintf(&b, "[[prompts]]\nid = %q\ntext = %q\n\n", id, t)
	}
	at := len(rest)
	if m := tableHeader.FindStringIndex(rest); m != nil {
		at = m[0]
	}
	head := strings.TrimRight(rest[:at], "\n") + "\n\n"
	return head + b.String() + strings.TrimLeft(rest[at:], "\n"), nil
}

// arrayEnd is the index just past the `]` that closes the array opened before i, skipping
// brackets inside strings.
func arrayEnd(s string, i int) int {
	depth := 1
	for ; i < len(s); i++ {
		switch s[i] {
		case '"':
			if strings.HasPrefix(s[i:], `"""`) {
				j := strings.Index(s[i+3:], `"""`)
				if j < 0 {
					return -1
				}
				i += j + 5
				continue
			}
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		case '\'':
			for i++; i < len(s) && s[i] != '\''; i++ {
			}
		case '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the and or but of to in on for with without from by at as is are was be been
		i im i'm my me we our us you your it its this that these those what whats what's which who how
		do does did can could should would will just so if than then there their them they into out
		about any some all get got have has had not no yes very really most more best good way ways`) {
		stopWords[w] = true
	}
}

// promptID makes an id from the first few words that carry meaning, unique against taken.
func promptID(text string, taken map[string]bool) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '\'')
	})
	var keep []string
	for _, w := range words {
		w = strings.Trim(w, "'")
		if w == "" || stopWords[w] {
			continue
		}
		keep = append(keep, strings.ReplaceAll(w, "'", ""))
		if len(keep) == 5 {
			break
		}
	}
	base := strings.Join(keep, "-")
	if base == "" {
		base = "prompt"
	}
	id := base
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	taken[id] = true
	return id
}
