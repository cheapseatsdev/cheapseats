package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Revision is the commit of the config repo this config came from, so every run can say which
// version of the prompts and settings produced it. In order: POPCORN_CONFIG_REV (set by a deploy),
// a REVISION file beside popcorn.toml (written by a build), or the checkout's own .git, read
// directly since the image has no git. Empty when none of them says.
func Revision(dir string) string {
	if r := strings.TrimSpace(os.Getenv("POPCORN_CONFIG_REV")); r != "" {
		return r
	}
	if b, err := os.ReadFile(filepath.Join(dir, "REVISION")); err == nil {
		if r := strings.TrimSpace(string(b)); r != "" {
			return r
		}
	}
	return gitHead(dir)
}

// gitHead resolves HEAD in the git checkout containing dir, without running git.
func gitHead(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		gitDir := filepath.Join(d, ".git")
		if fi, err := os.Stat(gitDir); err == nil {
			if !fi.IsDir() { // a worktree: "gitdir: <path>"
				b, err := os.ReadFile(gitDir)
				if err != nil {
					return ""
				}
				p := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
				if !filepath.IsAbs(p) {
					p = filepath.Join(d, p)
				}
				gitDir = p
			}
			return resolveHead(gitDir)
		}
		if parent := filepath.Dir(d); parent == d {
			return ""
		}
	}
}

func resolveHead(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(b))
	ref, ok := strings.CutPrefix(head, "ref: ")
	if !ok {
		return head // detached
	}
	// A worktree keeps refs in the main repo; its commondir file says where.
	common := gitDir
	if c, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common = filepath.Join(gitDir, strings.TrimSpace(string(c)))
	}
	for _, g := range []string{gitDir, common} {
		if b, err := os.ReadFile(filepath.Join(g, ref)); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	if b, err := os.ReadFile(filepath.Join(common, "packed-refs")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if sha, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok && name == ref {
				return sha
			}
		}
	}
	return ""
}
