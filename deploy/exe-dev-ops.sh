#!/usr/bin/env bash
# Adds the two things a Cheap Seats on an exe.dev VM needs to be left alone, as systemd timers:
#
#   nightly  a copy of the database in ~/cheapseats/backups (14 kept), before the 09:00 UTC runs
#   hourly   an email when a run ends partial, failed or out of budget, a fix list fails, or a
#            run stops making progress (each problem once), sent with exe.dev's email gateway
#
#   deploy/exe-dev-ops.sh <vm> <alert email>
#
# The email must be someone exe.dev lets the VM write to: the VM's owner, a teammate, or someone
# who has signed in to it. Run again to change it. The backups are on the VM's own disk: they
# undo mistakes, not the loss of the VM.
set -euo pipefail

vm=${1:?usage: deploy/exe-dev-ops.sh <vm> <alert email>}
to=${2:?usage: deploy/exe-dev-ops.sh <vm> <alert email>}
host="$vm.exe.xyz"
remote() { ssh -o BatchMode=yes "$host" "$@"; }

# shellcheck disable=SC2016 # $HOME is the VM's
home=$(remote 'echo $HOME')
user=$(remote whoami)
dir="$home/cheapseats"

remote "mkdir -p '$dir/bin' '$dir/backups' && chmod 700 '$dir/backups' && cat > '$dir/bin/cheapseats-backup' && chmod +x '$dir/bin/cheapseats-backup'" <<'EOF'
#!/usr/bin/env bash
# A consistent copy of the database (safe while Cheap Seats runs), keeping the newest 14. The
# copies are this user's alone, like the database.
set -euo pipefail
umask 077
dir="$HOME/cheapseats"
chmod 700 "$dir/backups"
find "$dir/backups" -maxdepth 1 -type f -name 'cheapseats-*.db' -exec chmod 600 {} +
sqlite3 "$dir/data/cheapseats.db" ".backup '$dir/backups/cheapseats-$(date -u +%Y-%m-%d).db'"
ls -1t "$dir"/backups/cheapseats-*.db | tail -n +15 | xargs -r rm -f
EOF

remote "cat > '$dir/bin/cheapseats-watch' && chmod +x '$dir/bin/cheapseats-watch'" <<'EOF'
#!/usr/bin/env bash
# Emails each new problem once. Runs that ended partial, failed or out of budget count for two
# days after they finish; a failed fix list counts whenever it fails, since a rebuild can be of
# any run, however old; a run counts as stalled after two hours with no answer finished. Runs on
# the VM (Linux): GNU date and systemd.
set -euo pipefail
to=$1
dir="$HOME/cheapseats"
seen="$dir/watch.seen"
touch "$seen"
db="file:$dir/data/cheapseats.db?mode=ro"
since=$(date -u -d '2 days ago' +%Y-%m-%dT%H:%M:%SZ)
stale=$(date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%SZ)
# Each line is "key<TAB>message"; a key goes in the seen file once its message is sent. A failed
# fix list keeps its error until the next good build replaces it, so its key is the run, the time
# of its last good build and a checksum of the error: a rebuild that fails differently, or fails
# after a good build, is new; the same failure next hour is not.
problems=$(sqlite3 -separator $'\t' "$db" "
  SELECT id || ' ' || status, id || ' ' || status || ': ' || name || ' — ' || replace(replace(COALESCE(error, ''), char(10), ' '), char(9), ' ') FROM runs
    WHERE status IN ('partial','failed','budget') AND finished_at >= '$since';
  SELECT id || ' fix list failed ' || COALESCE(fixes_at, 'never'), id || ' fix list failed: ' || name || ' — ' || replace(replace(fixes_error, char(10), ' '), char(9), ' ') FROM runs
    WHERE fixes_error IS NOT NULL;
  SELECT r.id || ' stalled', r.id || ' stalled: ' || r.name || ' — no answer finished since ' || COALESCE(MAX(x.finished_at), r.started_at)
    FROM runs r LEFT JOIN results x ON x.run_id = r.id
    WHERE r.status = 'running' GROUP BY r.id
    HAVING COALESCE(MAX(x.finished_at), r.started_at) < '$stale';")
new=""
while IFS=$'\t' read -r key line; do
	[ -z "$key" ] && continue
	case $key in
	*' fix list failed '*) key="$key $(printf '%s' "$line" | cksum | cut -d' ' -f1)" ;;
	esac
	if ! grep -qxF "$key" "$seen"; then
		new="$new$line"$'\n'
		printf '%s\n' "$key" >>"$seen"
	fi
done <<<"$problems"
[ -z "$new" ] && exit 0
host=$(hostname)
body="Cheap Seats on $host needs a look:

$new
Open https://$host.exe.xyz to retry a run (\"Retry unfinished\") or rebuild a fix list.
If answers fail with login errors, the Claude Code or Codex login on the VM has expired.
Log: sudo journalctl -u cheapseats"
python3 -c 'import json,sys; print(json.dumps({"to": sys.argv[1], "subject": sys.argv[2], "body": sys.argv[3]}))' \
	"$to" "Cheap Seats on $host: a run needs a look" "$body" |
	curl -fsS -X POST http://169.254.169.254/gateway/email/send -H 'Content-Type: application/json' --data-binary @- >/dev/null
EOF

unit() { # name, contents
	printf '%s\n' "$2" | remote "sudo tee /etc/systemd/system/$1 >/dev/null"
}
unit cheapseats-backup.service "[Unit]
Description=Back up the Cheap Seats database
[Service]
Type=oneshot
User=$user
ExecStart=$dir/bin/cheapseats-backup"
unit cheapseats-backup.timer "[Unit]
Description=Back up the Cheap Seats database nightly
[Timer]
OnCalendar=*-*-* 08:00:00 UTC
Persistent=true
[Install]
WantedBy=timers.target"
unit cheapseats-watch.service "[Unit]
Description=Email when a Cheap Seats run needs a look
[Service]
Type=oneshot
User=$user
ExecStart=$dir/bin/cheapseats-watch $to"
unit cheapseats-watch.timer "[Unit]
Description=Check Cheap Seats' runs hourly
[Timer]
OnCalendar=hourly
Persistent=true
[Install]
WantedBy=timers.target"
remote "sudo systemctl daemon-reload && sudo systemctl enable --now cheapseats-backup.timer cheapseats-watch.timer >/dev/null 2>&1"
remote "systemctl list-timers 'cheapseats-*' --no-pager | head -4"
echo "ok: nightly backups in $dir/backups, alerts to $to"
