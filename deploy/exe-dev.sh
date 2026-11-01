#!/usr/bin/env bash
# Deploys Cheap Seats to an exe.dev VM as a systemd service, with a config repo checkout as its config.
#
#   deploy/exe-dev.sh <vm> <config dir>          e.g. deploy/exe-dev.sh cheapseats ../my-config
#   SEED_DB=path/cheapseats.db deploy/exe-dev.sh …  first deploy only: start from a copy of a database
#   LIVE_CHECK=1 deploy/exe-dev.sh …             also send each structured output schema to the
#                                                Anthropic API once, with the VM's own key, before
#                                                swapping in (cheapseats check -live; a few cents,
#                                                capped at LIVE_BUDGET dollars, default 0.25)
#
# Builds a Linux binary from this checkout, ships the config as committed (a dirty config tree is
# refused, so a run's recorded commit means what it says), checks it on the VM, swaps it in and
# restarts. If Cheap Seats doesn't come up healthy, it rolls back to the previous binary and config,
# except when the failed build migrated the database or its schema can't be read afterwards (or
# there is no previous build): then Cheap Seats is left stopped and disabled, and the script prints
# what to do. A database schema it can't read before the swap stops the deploy before anything
# changes. Run from the repo root. The VM's secrets live in ~/cheapseats/cheapseats.env there (see
# docs/exe-dev.md); this never sends them.
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
# A config is files. A committed symlink would land on the VM pointing wherever it says: a REVISION
# link to ../data/cheapseats.db would have the write below truncate the database, and a prompt set
# linked to ../cheapseats.env would hand the secrets to the answerers.
links=$(git -C "$cfg" ls-tree -r HEAD | awk -F '\t' '$1 ~ /^120000 / { print $2 }')
if [ -n "$links" ]; then
	printf '%s has committed symlinks; commit the files instead:\n%s\n' "$cfg" "$links" >&2
	exit 1
fi
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
remote "mkdir -p '$dir/bin' '$dir/data' && chmod 700 '$dir/data' && rm -rf '$dir/config.new' && mkdir '$dir/config.new'"
scp -q -o BatchMode=yes "$work/cheapseats" "$host:$dir/bin/cheapseats.new"
# rm first, so a REVISION the archive carried is replaced rather than written through.
remote "tar -x -C '$dir/config.new' && rm -f '$dir/config.new/REVISION' && echo '$rev' > '$dir/config.new/REVISION'" <"$work/config.tar"
if [ -n "${SEED_DB:-}" ]; then
	if remote "test -e '$dir/data/cheapseats.db'"; then
		echo "the VM already has a database; not overwriting it with SEED_DB" >&2
		exit 1
	fi
	sqlite3 "$SEED_DB" ".backup '$work/seed.db'"
	scp -q -o BatchMode=yes "$work/seed.db" "$host:$dir/data/cheapseats.db"
	remote "chmod 600 '$dir/data/cheapseats.db'"
	echo "seeded the database from $SEED_DB"
fi

say "check on the VM"
if ! remote "test -s '$dir/cheapseats.env'"; then
	echo "$dir/cheapseats.env is missing on the VM; create it first (docs/exe-dev.md)" >&2
	exit 1
fi
remote "chmod +x '$dir/bin/cheapseats.new' && PATH=/usr/local/bin:\$PATH CHEAPSEATS_CONFIG='$dir/config.new/cheapseats.toml' '$dir/bin/cheapseats.new' check"
if [ "${LIVE_CHECK:-}" = 1 ]; then
	say "live schema check (paid, capped)"
	budget=${LIVE_BUDGET:-0.25}
	case $budget in
	'' | *[!0-9.]* | *.*.*)
		echo "LIVE_BUDGET must be a dollar amount like 0.25" >&2
		exit 1
		;;
	esac
	# Under systemd with the service's own EnvironmentFile, so the check reads the VM's key or
	# proxy exactly as Cheap Seats will, and the secrets never pass through this script.
	remote "sudo systemd-run --quiet --wait --pipe --collect --uid='$user' \
		-p EnvironmentFile='$dir/cheapseats.env' -p WorkingDirectory='$dir' \
		-E HOME='$home' -E PATH=/usr/local/bin:/usr/bin:/bin -E CHEAPSEATS_CONFIG='$dir/config.new/cheapseats.toml' \
		'$dir/bin/cheapseats.new' check -live -live-budget '$budget' -no-jev"
fi

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
# What it writes (the database, its backups) is this user's alone; the CLI's working directory is
# opened to the CLI user explicitly.
UMask=0077

[Install]
WantedBy=multi-user.target"
printf '%s\n' "$unit" | remote "sudo tee /etc/systemd/system/cheapseats.service >/dev/null && sudo systemctl daemon-reload && sudo systemctl enable cheapseats >/dev/null 2>&1"
# The database's schema before the new build touches it, to tell after a failed start whether the
# build migrated it (see below). sqlite3 reads it.
remote "command -v sqlite3 >/dev/null 2>&1 || sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq sqlite3 >/dev/null"
# Prints a number, "none" when there is no database, or nothing when it can't be read.
schema() {
	local v
	v=$(remote "if [ -e '$dir/data/cheapseats.db' ]; then sqlite3 '$dir/data/cheapseats.db' 'PRAGMA user_version;' 2>/dev/null; else echo none; fi" || true)
	case "$v" in none) echo none ;; '' | *[!0-9]*) ;; *) echo "$v" ;; esac
}
# Stop, read the schema and swap in one script on the VM, keeping the previous binary and config to
# roll back to. Cheap Seats is down from the stop until the start, so nothing at this end may leave
# it there: the script is a function, so none of it runs until all of it has arrived, and it ignores
# the hangup and the closed pipe of a dropped connection or a Ctrl-C here, so it runs to the end.
# A schema it can't read is found before anything changes; it then starts the previous build again
# (if it was running) and the deploy stops, rather than go on without knowing what a failure did.
IFS= read -r -d '' swap_script <<'EOF' || true
swap() {
	trap '' HUP PIPE
	cd "$1" || exit 1
	was=$(systemctl is-active cheapseats 2>/dev/null || true)
	sudo systemctl stop cheapseats || true
	before=none
	if [ -e data/cheapseats.db ]; then
		before=$(sqlite3 data/cheapseats.db 'PRAGMA user_version;' 2>/dev/null || true)
		if ! [[ $before =~ ^[0-9]+$ ]]; then
			if [ "$was" = active ]; then sudo systemctl start cheapseats || true; fi
			echo "unreadable $was"
			return
		fi
	fi
	if [ -e bin/cheapseats ]; then mv -f bin/cheapseats bin/cheapseats.prev; fi
	mv bin/cheapseats.new bin/cheapseats
	rm -rf config.prev
	if [ -e config ]; then mv config config.prev; fi
	mv config.new config
	sudo systemctl start cheapseats || true
	echo "swapped $before"
}
swap "$@"
EOF
swapping=1
# Says what happened if this end stops while the VM swaps.
trap 'rm -rf "$work"; if [ "${swapping:-}" = 1 ]; then echo "stopped while the VM swapped in the new build. The VM finishes the swap and starts the new build on its own, but nothing checks that it came up or rolls it back: see ssh $host systemctl status cheapseats, and docs/exe-dev.md to roll back by hand" >&2; fi' EXIT
swapped=$(remote bash -s "$dir" <<<"$swap_script" || true)
swapped=$(printf '%s\n' "$swapped" | tail -n 1)
case "$swapped" in
"swapped "*)
	before=${swapped#swapped }
	;;
"unreadable active")
	swapping=0
	echo "the schema of $dir/data/cheapseats.db couldn't be read, so a failed start couldn't be told apart from a migration; nothing was swapped, and Cheap Seats runs the previous build again" >&2
	exit 1
	;;
"unreadable "*)
	swapping=0
	echo "the schema of $dir/data/cheapseats.db couldn't be read, so a failed start couldn't be told apart from a migration; nothing was swapped (Cheap Seats wasn't running before, and still isn't)" >&2
	exit 1
	;;
*)
	swapping=0
	echo "lost the VM while swapping in the new build. If the swap had begun, the VM finishes it and starts the new build on its own, but nothing checks that it came up or rolls it back: see ssh $host systemctl status cheapseats, and docs/exe-dev.md to roll back by hand" >&2
	exit 1
	;;
esac
swapping=0

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
# systemd keeps restarting the failed build; stop it, so it can't migrate further or resume paid
# work while the database is read and, perhaps, replaced.
remote "sudo systemctl stop cheapseats" || true
# Where the failed build stays stopped, it is disabled too, so a reboot of the VM doesn't start it
# (resuming paid runs, perhaps migrating further). The next deploy enables the service again.
leave_stopped() {
	if [ "$(remote "sudo systemctl disable cheapseats >/dev/null 2>&1 && echo ok" || true)" = ok ]; then
		echo "Cheap Seats is stopped and disabled, so a reboot doesn't start this build; the next deploy enables it again. To start this build anyway: ssh $host sudo systemctl enable --now cheapseats" >&2
	else
		echo "Cheap Seats is stopped, but disabling it failed, so a reboot of the VM would start this build. Disable it with: ssh $host sudo systemctl disable cheapseats" >&2
	fi
}
after=$(schema)
# Whether there is a previous build to go back to: a first deploy (perhaps with SEED_DB) has none.
prev=$(remote "cd '$dir' && if [ -e bin/cheapseats.prev ] && [ -d config.prev ]; then echo yes; fi" || true)
# Each migration commits on its own, so a start that failed partway leaves the database at a
# schema between the two; one that failed in its first migration leaves it as it was. Only a
# database at a later schema than before stops the previous build opening it. ($before is always
# a number or "none" here: the swap refuses a schema it can't read.)
if [ "$before" = none ] || [ "$after" = none ]; then
	migrated=no # there was no database to protect
elif [ -z "$after" ]; then
	migrated=unknown
elif [ "$after" -gt "$before" ]; then
	migrated=yes
else
	migrated=no
fi
if [ "$migrated" = no ]; then
	if [ "$prev" != yes ]; then
		echo "there is no previous build on the VM to roll back to." >&2
		leave_stopped
		exit 1
	fi
	aside=""
	if [ "$before" = none ] && [ "$after" != none ]; then
		# The new build made a database at its own, newer schema, which the previous build can't
		# open; it starts without one, as it would have before this deploy.
		ts=$(date -u +%Y%m%dT%H%M%SZ)
		aside="for f in data/cheapseats.db data/cheapseats.db-wal data/cheapseats.db-shm; do if [ -e \"\$f\" ]; then mv \"\$f\" \"\$f.failed-$ts\"; fi; done;"
		echo "there was no database before this deploy; moving the one this build made to $dir/data/cheapseats.db.failed-$ts (with its -wal and -shm, if any)" >&2
	fi
	echo "rolling back to the previous binary and config" >&2
	remote "cd '$dir' && sudo systemctl stop cheapseats; $aside mv -f bin/cheapseats.prev bin/cheapseats; rm -rf config; mv config.prev config; sudo systemctl start cheapseats"
	exit 1
fi
# This build migrated the database (or its schema couldn't be read). The previous binary refuses
# a migrated database, so rolling back the binary alone would leave it restarting forever.
# Before migrating, the new build copied the database to cheapseats.db.bak-v<N>, N the schema it
# found: the copy the previous build can open is the one named for the schema before this deploy.
# A later restart that found the database partly migrated writes a copy at that schema too, which
# the previous build can't open. Putting the copy back would undo the migration, but it would
# also discard whatever the new build wrote after it (a resumed run records paid answers), and
# only the operator can tell whether that matters or the new build is better fixed forward. So
# the script doesn't restore it: Cheap Seats stays stopped and the script prints the steps.
bak="$dir/data/cheapseats.db.bak-v$before"
if [ "$migrated" = unknown ]; then
	printf '\nNot rolling back: the database schema couldn\047t be read after the failed start (before this deploy: %s).\n' "$before" >&2
	leave_stopped
	echo "Check the schema with: ssh $host \"sqlite3 '$dir/data/cheapseats.db' 'PRAGMA user_version;'\"" >&2
	exit 1
fi
if [ -z "$(remote "test -e '$bak' && echo yes" || true)" ]; then
	bak=""
fi
printf '\nNot rolling back: this build migrated the database from schema %s to %s, and the previous\nbuild can\047t open it.\n' "$before" "$after" >&2
leave_stopped
if [ -z "$bak" ]; then
	echo "The copy from before the migration, $dir/data/cheapseats.db.bak-v$before, isn't there; fix the build and deploy again." >&2
	exit 1
fi
if [ "$prev" != yes ]; then
	cat >&2 <<MSG
There is no previous build on the VM to go back to, so fix the build and deploy again; the database
stays as it is. The database as it was before this deploy is $bak.
MSG
	exit 1
fi
cat >&2 <<MSG
The database as it was before this deploy is $bak (schema $before, which the
previous build opens; any other cheapseats.db.bak-v* is from a different schema).

Either fix the build and deploy again (the database stays as it is), or go back to the previous
build with the database as it was before this deploy. Anything the new build wrote is kept in
the .failed copies; it is not in the restored database:

  ssh $host
  cd '$dir' && sudo systemctl stop cheapseats
  ts=\$(date -u +%Y%m%dT%H%M%SZ)
  for f in data/cheapseats.db data/cheapseats.db-wal data/cheapseats.db-shm; do [ -e "\$f" ] && mv "\$f" "\$f.failed-\$ts"; done
  cp -p '$bak' data/cheapseats.db
  mv -f bin/cheapseats.prev bin/cheapseats && rm -rf config && mv config.prev config
  sudo systemctl enable --now cheapseats && curl -fsS http://127.0.0.1:$port/healthz
MSG
exit 1
