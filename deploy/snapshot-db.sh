#!/usr/bin/env bash
# A consistent copy of a deployment's database, for the weekly cut's read-only agents and for
# behavior compares before a refactor merges.
#
#   deploy/snapshot-db.sh <vm> [out.db]     e.g. deploy/snapshot-db.sh cheapseats
#
# Uses sqlite3's online backup on the VM (the same call deploy/exe-dev-ops.sh makes nightly), so
# it is safe while serve runs and includes what is still in the write-ahead log. Copies it down
# to data/snapshots/<vm>-<date>.db (data/ is gitignored) and removes the remote temp file. Prints
# the local path last. Never points anything at the live database.
set -euo pipefail

vm=${1:?usage: deploy/snapshot-db.sh <vm> [out.db]}
host="$vm.exe.xyz"
stamp=$(date -u +%Y-%m-%d)
out=${2:-data/snapshots/$vm-$stamp.db}
remote() { ssh -o BatchMode=yes "$host" "$@"; }

if [ -e "$out" ]; then
	echo "$out already exists; pass another path or remove it" >&2
	exit 1
fi
mkdir -p "$(dirname "$out")"

# shellcheck disable=SC2016 # $HOME is the VM's
db=$(remote 'echo "$HOME/cheapseats/data/cheapseats.db"')
tmp="/tmp/cheapseats-snapshot-$$.db"
remote "test -s '$db'" || { echo "no database at $db on $host" >&2; exit 1; }
remote "rm -f '$tmp' && sqlite3 '$db' \".backup '$tmp'\""
scp -q -o BatchMode=yes "$host:$tmp" "$out"
remote "rm -f '$tmp'"

pages=$(sqlite3 "$out" 'PRAGMA integrity_check;')
if [ "$pages" != "ok" ]; then
	echo "integrity check failed on the copy: $pages" >&2
	exit 1
fi
runs=$(sqlite3 "$out" 'SELECT count(*) FROM runs;')
echo "snapshot of $host: $runs runs, schema $(sqlite3 "$out" 'PRAGMA user_version;')"
echo "$out"
