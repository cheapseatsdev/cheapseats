#!/usr/bin/env bash
# Deploys Cheap Seats to an exe.dev VM as a systemd service, with a config repo checkout as its config.
#
#   deploy/exe-dev.sh <vm> <config dir>          e.g. deploy/exe-dev.sh cheapseats ../my-config
#   SEED_DB=path/cheapseats.db deploy/exe-dev.sh …  first deploy only: start from a copy of a database
#
# Builds a Linux binary from this checkout, ships the config as committed (a dirty config tree is
# refused, so a run's recorded commit means what it says), checks it on the VM, swaps it in,
# restarts, and rolls back if Cheap Seats doesn't come up healthy. Run from the repo root. The VM's
# secrets live in ~/cheapseats/cheapseats.env there (see docs/exe-dev.md); this never sends them.
set -euo pipefail

vm=${1:?usage: deploy/exe-dev.sh <vm> <config dir>}
cfg=${2:?usage: deploy/exe-dev.sh <vm> <config dir>}
host="$vm.exe.xyz"
port=${PORT:-8000}
work=$(mktemp -d "${TMPDIR:-/tmp}/cheapseats-exe.XXXXXX")
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
	-ldflags "-s -w -X github.com/cheapseatsdev/cheapseats/internal/version.Version=$version" \
	-o "$work/cheapseats" ./cmd/cheapseats
echo "cheapseats $version, linux/$arch"

say "upload"
# shellcheck disable=SC2016 # $HOME is the VM's
home=$(remote 'echo $HOME')
user=$(remote whoami)
dir="$home/cheapseats"
remote "mkdir -p '$dir/bin' '$dir/data' && rm -rf '$dir/config.new' && mkdir '$dir/config.new'"
scp -q -o BatchMode=yes "$work/cheapseats" "$host:$dir/bin/cheapseats.new"
remote "tar -x -C '$dir/config.new' && echo '$rev' > '$dir/config.new/REVISION'" <"$work/config.tar"
if [ -n "${SEED_DB:-}" ]; then
	if remote "test -e '$dir/data/cheapseats.db'"; then
		echo "the VM already has a database; not overwriting it with SEED_DB" >&2
		exit 1
	fi
	sqlite3 "$SEED_DB" ".backup '$work/seed.db'"
	scp -q -o BatchMode=yes "$work/seed.db" "$host:$dir/data/cheapseats.db"
	echo "seeded the database from $SEED_DB"
fi

say "check on the VM"
if ! remote "test -s '$dir/cheapseats.env'"; then
	echo "$dir/cheapseats.env is missing on the VM; create it first (docs/exe-dev.md)" >&2
	exit 1
fi
remote "chmod +x '$dir/bin/cheapseats.new' && PATH=/usr/local/bin:\$PATH CHEAPSEATS_CONFIG='$dir/config.new/cheapseats.toml' '$dir/bin/cheapseats.new' check"

say "CLI user"
# The claude and codex CLIs run as a user of their own: each carries a shell the model can use,
# and a prompt-injected search answer that used it would otherwise find this user's env file,
# database and logins. The service runs them through sudo (CHEAPSEATS_CLI_USER); the rule allows
# just the two commands, with the environment Cheap Seats passes. Logins made as this user before
# the CLI user existed move over, once.
#
# Nor may it connect to Cheap Seats' port. Cheap Seats believes the email the proxy names in
# X-ExeDev-Email, so anything on the VM that reaches the port can claim to be anyone, and binding
# loopback wouldn't stop a user on the same machine. An nftables rule refuses connections from
# the CLI user's uid to the port, whatever the address; it loads from a unit cheapseats.service
# requires, so Cheap Seats doesn't start without it.
cliuser=cheapseats-cli
remote bash -s "$user" "$cliuser" "$port" <<'EOF'
set -euo pipefail
user=$1 cliuser=$2 port=$3
if ! id "$cliuser" >/dev/null 2>&1; then
	sudo useradd --system --create-home --home-dir "/home/$cliuser" --shell /usr/sbin/nologin "$cliuser"
	echo "made the $cliuser user"
fi
home=/home/$cliuser
printf '%s ALL=(%s) NOPASSWD:SETENV: /usr/local/bin/claude, /usr/local/bin/codex\n' "$user" "$cliuser" | sudo tee /etc/sudoers.d/cheapseats >/dev/null
sudo chmod 440 /etc/sudoers.d/cheapseats
sudo visudo -cf /etc/sudoers.d/cheapseats >/dev/null
for f in .claude/.credentials.json .codex/auth.json; do
	if [ -e "$HOME/$f" ] && ! sudo test -e "$home/$f"; then
		sudo mkdir -p "$home/${f%/*}"
		sudo mv "$HOME/$f" "$home/$f"
		sudo chown -R "$cliuser:$cliuser" "$home/${f%/*}"
		echo "moved ~/$f to $cliuser"
	fi
done
for f in claude:.claude/.credentials.json codex:.codex/auth.json; do
	if sudo test -e "$home/${f#*:}"; then
		echo "${f%%:*}: logged in as $cliuser"
	else
		echo "${f%%:*}: not logged in; if an answerer needs it: ssh -t $(hostname) sudo -H -u $cliuser ${f%%:*}"
	fi
done
if ! command -v nft >/dev/null 2>&1; then
	sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nftables >/dev/null
fi
nft=$(command -v nft)
# Declaring the table, deleting it and declaring it again replaces it in one atomic load, so
# running this again (or with another port) leaves exactly one rule.
sudo mkdir -p /etc/cheapseats
printf '%s\n' 'table inet cheapseats_cli' 'delete table inet cheapseats_cli' \
	'table inet cheapseats_cli {' '	chain output {' '		type filter hook output priority 0; policy accept;' \
	"		meta skuid $(id -u "$cliuser") tcp dport $port reject with tcp reset" '	}' '}' |
	sudo tee /etc/cheapseats/cli-firewall.nft >/dev/null
# PartOf: restarting nftables.service flushes the ruleset, and this unit reloads the table after
# (restarting Cheap Seats with it); stopping nftables stops Cheap Seats rather than leave it open.
printf '%s\n' '[Unit]' "Description=Keep $cliuser off Cheap Seats' port" \
	'Before=cheapseats.service' 'After=nftables.service' 'PartOf=nftables.service' \
	'[Service]' 'Type=oneshot' 'RemainAfterExit=yes' \
	"ExecStart=$nft -f /etc/cheapseats/cli-firewall.nft" "ExecStop=$nft delete table inet cheapseats_cli" \
	'[Install]' 'WantedBy=multi-user.target' |
	sudo tee /etc/systemd/system/cheapseats-cli-firewall.service >/dev/null
sudo systemctl daemon-reload
sudo systemctl enable cheapseats-cli-firewall >/dev/null 2>&1
# Start it if it isn't running, and load the rule directly: restarting the unit would restart
# Cheap Seats, which requires it, before the new build is in place.
sudo systemctl start cheapseats-cli-firewall
sudo "$nft" -f /etc/cheapseats/cli-firewall.nft
if ! sudo "$nft" list chain inet cheapseats_cli output | grep -q "dport $port reject"; then
	echo "the rule keeping $cliuser off port $port didn't load" >&2
	exit 1
fi
echo "$cliuser can't connect to port $port"
EOF

say "install"
unit="[Unit]
Description=Cheap Seats
After=network-online.target cheapseats-cli-firewall.service
Wants=network-online.target
Requires=cheapseats-cli-firewall.service

[Service]
User=$user
WorkingDirectory=$dir
EnvironmentFile=$dir/cheapseats.env
Environment=HOME=$home
Environment=PATH=/usr/local/bin:/usr/bin:/bin
Environment=CHEAPSEATS_CONFIG=$dir/config/cheapseats.toml
Environment=CHEAPSEATS_DATA_DIR=$dir/data
Environment=CHEAPSEATS_CLI_USER=$cliuser
ExecStart=$dir/bin/cheapseats serve -addr 0.0.0.0:$port
Restart=on-failure
RestartSec=5
# Runs in progress record themselves as interrupted on SIGTERM and resume on the next start.
KillSignal=SIGTERM
TimeoutStopSec=40

[Install]
WantedBy=multi-user.target"
printf '%s\n' "$unit" | remote "sudo tee /etc/systemd/system/cheapseats.service >/dev/null && sudo systemctl daemon-reload && sudo systemctl enable cheapseats >/dev/null 2>&1"
# Keep the previous binary and config, to roll back to.
remote "cd '$dir' && sudo systemctl stop cheapseats || true
	if [ -e bin/cheapseats ]; then mv -f bin/cheapseats bin/cheapseats.prev; fi
	mv bin/cheapseats.new bin/cheapseats
	rm -rf config.prev; if [ -e config ]; then mv config config.prev; fi
	mv config.new config
	sudo systemctl start cheapseats"

say "healthy?"
for _ in $(seq 1 30); do
	if remote "curl -fsS http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
		echo "ok: https://$host serves cheapseats $version with config $rev"
		# The rule's own proof: as the CLI user, the port refuses (curl prints 000).
		code=$(remote "sudo -u $cliuser curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:$port/healthz || true")
		if [ "$code" != "000" ]; then
			echo "but $cliuser reached port $port (HTTP $code); the firewall rule isn't working" >&2
			exit 1
		fi
		exit 0
	fi
	sleep 2
done
echo "Cheap Seats didn't come up; its log:" >&2
remote "sudo journalctl -u cheapseats -n 30 --no-pager" >&2 || true
if remote "test -e '$dir/bin/cheapseats.prev'"; then
	echo "rolling back to the previous binary and config" >&2
	remote "cd '$dir' && sudo systemctl stop cheapseats; mv -f bin/cheapseats.prev bin/cheapseats; rm -rf config; mv config.prev config; sudo systemctl start cheapseats"
fi
exit 1
