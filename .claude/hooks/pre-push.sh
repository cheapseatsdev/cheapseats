#!/usr/bin/env bash
# Claude Code PreToolUse hook (see .claude/settings.json): when the Bash command about to run is
# a git push, run the checks CI will run, and block the push (exit 2) if one fails. Agents used
# to learn about an unused import from CI, three minutes and one red check later.
set -u
input=$(cat)
case "$input" in
*"git push"*) ;;
*) exit 0 ;;
esac
cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)" || exit 0
[ -f go.mod ] || exit 0
fail() { printf 'pre-push: %s\n' "$*" >&2; exit 2; }
unformatted=$(gofmt -l . 2>/dev/null)
[ -z "$unformatted" ] || fail "gofmt wants to change: $unformatted"
out=$(go vet ./... 2>&1) || fail "go vet: $out"
out=$(go test -race ./... 2>&1) || fail "go test -race:
$(printf '%s\n' "$out" | grep -E '^(--- FAIL|FAIL|panic|ok  )' | head -20)"
for f in deploy/*.sh .claude/hooks/*.sh; do
	[ -f "$f" ] || continue
	out=$(bash -n "$f" 2>&1) || fail "bash -n $f: $out"
done
exit 0
