#!/usr/bin/env bash
# Deploys Popcorn to an exe.dev VM as a systemd service, with a config repo checkout as its config.
#
#   deploy/exe-dev.sh <vm> <config dir>          e.g. deploy/exe-dev.sh popcorn ../my-config
#   SEED_DB=path/popcorn.db deploy/exe-dev.sh …  first deploy only: start from a copy of a database
#
# Builds a Linux binary from this checkout, ships the config as committed (a dirty config tree is
# refused, so a run's recorded commit means what it says), checks it on the VM, swaps it in,
# restarts, and rolls back if Popcorn doesn't come up healthy. Run from the repo root. The VM's
# secrets live in ~/popcorn/popcorn.env there (see docs/exe-dev.md); this never sends them.
set -euo pipefail

vm=${1:?usage: deploy/exe-dev.sh <vm> <config dir>}
cfg=${2:?usage: deploy/exe-dev.sh <vm> <config dir>}
host="$vm.exe.xyz"
port=${PORT:-8000}
work=$(mktemp -d "${TMPDIR:-/tmp}/popcorn-exe.XXXXXX")
trap 'rm -rf "$work"' EXIT
remote() { ssh -o BatchMode=yes "$host" "$@"; }
say() { printf '== %s\n' "$*"; }

say "config: $cfg"
if ! git -C "$cfg" rev-parse --git-dir >/dev/null 2>&1; then
	echo "$cfg isn't a git checkout; deploy from the config repo so runs record its commit" >&2
	exit 1
fi
if [ -n "$(git -C "$cfg" status --porcelain)" ]; then
	echo "$cfg has uncommitted changes; commit them first" >&2
	exit 1
fi
rev=$(git -C "$cfg" rev-parse HEAD)
git -C "$cfg" archive --format=tar -o "$work/config.tar" HEAD
echo "commit $rev"

say "binary"
case "$(remote uname -m)" in
x86_64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) echo "unsupported VM architecture" >&2; exit 1 ;;
esac
version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
	-ldflags "-s -w -X github.com/metcalfc/popcorn/internal/version.Version=$version" \
	-o "$work/popcorn" ./cmd/popcorn
echo "popcorn $version, linux/$arch"

say "upload"
# shellcheck disable=SC2016 # $HOME is the VM's
home=$(remote 'echo $HOME')
user=$(remote whoami)
dir="$home/popcorn"
remote "mkdir -p '$dir/bin' '$dir/data' && rm -rf '$dir/config.new' && mkdir '$dir/config.new'"
scp -q -o BatchMode=yes "$work/popcorn" "$host:$dir/bin/popcorn.new"
remote "tar -x -C '$dir/config.new' && echo '$rev' > '$dir/config.new/REVISION'" <"$work/config.tar"
if [ -n "${SEED_DB:-}" ]; then
	if remote "test -e '$dir/data/popcorn.db'"; then
		echo "the VM already has a database; not overwriting it with SEED_DB" >&2
		exit 1
	fi
	sqlite3 "$SEED_DB" ".backup '$work/seed.db'"
	scp -q -o BatchMode=yes "$work/seed.db" "$host:$dir/data/popcorn.db"
	echo "seeded the database from $SEED_DB"
fi

say "check on the VM"
if ! remote "test -s '$dir/popcorn.env'"; then
	echo "$dir/popcorn.env is missing on the VM; create it first (docs/exe-dev.md)" >&2
	exit 1
fi
remote "chmod +x '$dir/bin/popcorn.new' && PATH=/usr/local/bin:\$PATH POPCORN_CONFIG='$dir/config.new/popcorn.toml' '$dir/bin/popcorn.new' check"

say "install"
unit="[Unit]
Description=Popcorn
After=network-online.target
Wants=network-online.target

[Service]
User=$user
WorkingDirectory=$dir
EnvironmentFile=$dir/popcorn.env
Environment=HOME=$home
Environment=PATH=/usr/local/bin:/usr/bin:/bin
Environment=POPCORN_CONFIG=$dir/config/popcorn.toml
Environment=POPCORN_DATA_DIR=$dir/data
ExecStart=$dir/bin/popcorn serve -addr 0.0.0.0:$port
Restart=on-failure
RestartSec=5
# Runs in progress record themselves as interrupted on SIGTERM and resume on the next start.
KillSignal=SIGTERM
TimeoutStopSec=40

[Install]
WantedBy=multi-user.target"
printf '%s\n' "$unit" | remote "sudo tee /etc/systemd/system/popcorn.service >/dev/null && sudo systemctl daemon-reload && sudo systemctl enable popcorn >/dev/null 2>&1"
# Keep the previous binary and config, to roll back to.
remote "cd '$dir' && sudo systemctl stop popcorn || true
	if [ -e bin/popcorn ]; then mv -f bin/popcorn bin/popcorn.prev; fi
	mv bin/popcorn.new bin/popcorn
	rm -rf config.prev; if [ -e config ]; then mv config config.prev; fi
	mv config.new config
	sudo systemctl start popcorn"

say "healthy?"
for _ in $(seq 1 30); do
	if remote "curl -fsS http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
		echo "ok: https://$host serves popcorn $version with config $rev"
		exit 0
	fi
	sleep 2
done
echo "Popcorn didn't come up; its log:" >&2
remote "sudo journalctl -u popcorn -n 30 --no-pager" >&2 || true
if remote "test -e '$dir/bin/popcorn.prev'"; then
	echo "rolling back to the previous binary and config" >&2
	remote "cd '$dir' && sudo systemctl stop popcorn; mv -f bin/popcorn.prev bin/popcorn; rm -rf config; mv config.prev config; sudo systemctl start popcorn"
fi
exit 1
