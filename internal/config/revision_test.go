package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRevision(t *testing.T) {
	t.Setenv("CHEAPSEATS_CONFIG_REV", "")
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A checkout: HEAD names a branch whose ref is loose, then only in packed-refs.
	repo := t.TempDir()
	write(filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(filepath.Join(repo, ".git", "refs", "heads", "main"), "aaa111\n")
	if got := Revision(repo); got != "aaa111" {
		t.Fatalf("loose ref: %q", got)
	}
	os.Remove(filepath.Join(repo, ".git", "refs", "heads", "main"))
	write(filepath.Join(repo, ".git", "packed-refs"), "# pack-refs\nbbb222 refs/heads/main\n")
	if got := Revision(repo); got != "bbb222" {
		t.Fatalf("packed ref: %q", got)
	}
	// A worktree: .git is a file pointing at the main repo's worktree dir.
	wt := t.TempDir()
	wtGit := filepath.Join(repo, ".git", "worktrees", "pr")
	write(filepath.Join(wt, ".git"), "gitdir: "+wtGit+"\n")
	write(filepath.Join(wtGit, "HEAD"), "ref: refs/heads/pr\n")
	write(filepath.Join(wtGit, "commondir"), "../..\n")
	write(filepath.Join(repo, ".git", "refs", "heads", "pr"), "ccc333\n")
	if got := Revision(filepath.Join(wt)); got != "ccc333" {
		t.Fatalf("worktree: %q", got)
	}
	// A copy with a REVISION file, and the environment, win.
	write(filepath.Join(repo, "REVISION"), "ddd444\n")
	if got := Revision(repo); got != "ddd444" {
		t.Fatalf("REVISION: %q", got)
	}
	t.Setenv("CHEAPSEATS_CONFIG_REV", "eee555")
	if got := Revision(repo); got != "eee555" {
		t.Fatalf("env: %q", got)
	}
	t.Setenv("CHEAPSEATS_CONFIG_REV", "")
	if got := Revision(t.TempDir()); got != "" {
		t.Fatalf("no git: %q", got)
	}
}
