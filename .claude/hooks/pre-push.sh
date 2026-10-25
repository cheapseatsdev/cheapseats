#!/usr/bin/env bash
# Claude Code PreToolUse hook (see .claude/settings.json): when the Bash command about to run is
# a git push, run the checks CI will run, and block the push (exit 2) if one fails. Agents used
# to learn about an unused import from CI, three minutes and one red check later.
#
# The checks run in the repo being pushed, not the one the session started in: the directory
# comes from `git -C DIR push` or `cd DIR && ... git push` in the command, else the hook's cwd.
# A push from any repo that is not this Go module (another project, a different module) is let
# through unchecked. Agent worktrees under .claude/worktrees/ are not part of the push and are
# left out of gofmt; `go vet ./...` already skips them (the go tool ignores dot directories).
set -u
input=$(cat)
case "$input" in
*git*push*) ;;
*) exit 0 ;;
esac

# This hook's own module, to compare the push target against.
here=$(cd "$(dirname "$0")/../.." 2>/dev/null && pwd) || exit 0
module=$(sed -n 's/^module[[:space:]]*//p' "$here/go.mod" 2>/dev/null | head -1)
[ -n "$module" ] || exit 0

cmd=$input
dir=$PWD
if command -v jq >/dev/null 2>&1; then
	c=$(printf '%s' "$input" | jq -r '.tool_input.command // empty' 2>/dev/null) && [ -n "$c" ] && cmd=$c
	d=$(printf '%s' "$input" | jq -r '.cwd // empty' 2>/dev/null) && [ -n "$d" ] && dir=$d
fi

# A path argument: double-quoted, single-quoted, or a bare word.
sq="'"
arg="(\"[^\"]+\"|${sq}[^${sq}]+${sq}|[^[:space:];&|]+)"
unquote() {
	local v=$1
	v=${v#\"}; v=${v%\"}
	v=${v#"$sq"}; v=${v%"$sq"}
	printf '%s' "$v"
}
re_c="git[[:space:]]+-C[[:space:]]+${arg}[^;&|]*[[:space:]]push"
re_cd="(^|[;&|(][[:space:]]*)cd[[:space:]]+${arg}"
target=
case "$cmd" in
*"git push"*) ;;
*) [[ $cmd =~ $re_c ]] || exit 0 ;;
esac
if [[ $cmd =~ $re_c ]]; then
	target=$(unquote "${BASH_REMATCH[1]}")
elif [[ $cmd =~ $re_cd ]]; then
	target=$(unquote "${BASH_REMATCH[2]}")
fi
case "$target" in
"") ;;
"~"*) dir=$HOME${target#"~"} ;;
/*) dir=$target ;;
*) dir=$dir/$target ;;
esac

top=$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null) || exit 0
[ -n "$top" ] && cd "$top" || exit 0
[ -f go.mod ] || exit 0
[ "$(sed -n 's/^module[[:space:]]*//p' go.mod | head -1)" = "$module" ] || exit 0

fail() { printf 'pre-push: %s\n' "$*" >&2; exit 2; }
unformatted=$(find . -path ./.claude/worktrees -prune -o -name '*.go' -type f -exec gofmt -l {} + 2>/dev/null)
[ -z "$unformatted" ] || fail "gofmt wants to change: $unformatted"
out=$(go vet ./... 2>&1) || fail "go vet: $out"
out=$(go test -race ./... 2>&1) || fail "go test -race:
$(printf '%s\n' "$out" | grep -E '^(--- FAIL|FAIL|panic|ok  )' | head -20)"
for f in deploy/*.sh .claude/hooks/*.sh; do
	[ -f "$f" ] || continue
	out=$(bash -n "$f" 2>&1) || fail "bash -n $f: $out"
done
exit 0
