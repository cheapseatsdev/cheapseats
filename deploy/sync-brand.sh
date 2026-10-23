#!/usr/bin/env bash
# Copies the brand's design tokens from the cheapseats-site repo into this one.
#
#   deploy/sync-brand.sh <path to a cheapseats-site checkout>
#
# brand/web/tokens.css there is the source of truth for the --cs-* custom properties (colors in
# light and dark, type stacks, radii, content width); internal/server/static/tokens.css here is a
# generated copy with a header naming the commit it came from. Edit the tokens in cheapseats-site,
# merge, then run this and open a PR here. A dirty source tree is refused, so the recorded commit
# means what it says. TestTokensSynced fails when the copy lacks its header or the styles use a
# token the copy does not define. Run from the repo root.
set -euo pipefail

site=${1:?usage: deploy/sync-brand.sh <path to cheapseats-site checkout>}
src="$site/brand/web/tokens.css"
dst=internal/server/static/tokens.css

if [ ! -f "$src" ]; then
	echo "$src not found; pass the root of a cheapseats-site checkout" >&2
	exit 1
fi
if [ ! -f "$dst" ] && [ ! -d internal/server/static ]; then
	echo "run from the repo root (no internal/server/static here)" >&2
	exit 1
fi
if [ -n "$(git -C "$site" status --porcelain -- brand/web/tokens.css)" ]; then
	echo "$src has uncommitted changes; commit them first so the copy records a real commit" >&2
	exit 1
fi
commit=$(git -C "$site" rev-parse --short HEAD)

{
	printf '/* GENERATED: synced from cheapseats-site brand/web/tokens.css at %s; edit there, run deploy/sync-brand.sh. */\n' "$commit"
	cat "$src"
} >"$dst.tmp"
mv "$dst.tmp" "$dst"
echo "$dst: tokens from cheapseats-site@$commit"
