#!/usr/bin/env bash
# Adds the two things a Popcorn on an exe.dev VM needs to be left alone, as systemd timers:
#
#   nightly  a copy of the database in ~/popcorn/backups (14 kept), before the 09:00 UTC runs
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
dir="$home/popcorn"

remote "mkdir -p '$dir/bin' '$dir/backups' && cat > '$dir/bin/popcorn-backup' && chmod +x '$dir/bin/popcorn-backup'" <<'EOF'
#!/usr/bin/env bash
# A consistent copy of the database (safe while Popcorn runs), keeping the newest 14.
set -euo pipefail
dir="$HOME/popcorn"
sqlite3 "$dir/data/popcorn.db" ".backup '$dir/backups/popcorn-$(date -u +%Y-%m-%d).db'"
ls -1t "$dir"/backups/popcorn-*.db | tail -n +15 | xargs -r rm -f
EOF

remote "cat > '$dir/bin/popcorn-watch' && chmod +x '$dir/bin/popcorn-watch'" <<'EOF'
#!/usr/bin/env bash
# Emails each new problem with the last two days' runs, once: runs that ended partial, failed or
# out of budget, fix lists that failed, and runs with no answer finished for two hours.
set -euo pipefail
to=$1
dir="$HOME/popcorn"
seen="$dir/watch.seen"
touch "$seen"
db="file:$dir/data/popcorn.db?mode=ro"
since=$(date -u -d '2 days ago' +%Y-%m-%dT%H:%M:%SZ)
stale=$(date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%SZ)
problems=$(sqlite3 "$db" "
  SELECT id || ' ' || status || ': ' || name || ' — ' || COALESCE(error, '') FROM runs
    WHERE status IN ('partial','failed','budget') AND finished_at >= '$since';
  SELECT id || ' fix list failed: ' || name || ' — ' || fixes_error FROM runs
    WHERE fixes_error IS NOT NULL AND finished_at >= '$since';
  SELECT r.id || ' stalled: ' || r.name || ' — no answer finished since ' || COALESCE(MAX(x.finished_at), r.started_at)
    FROM runs r LEFT JOIN results x ON x.run_id = r.id
    WHERE r.status = 'running' GROUP BY r.id
    HAVING COALESCE(MAX(x.finished_at), r.started_at) < '$stale';")
new=""
while IFS= read -r line; do
	[ -z "$line" ] && continue
	key=${line%%:*}
	if ! grep -qxF "$key" "$seen"; then
		new="$new$line"$'\n'
		printf '%s\n' "$key" >>"$seen"
	fi
done <<<"$problems"
[ -z "$new" ] && exit 0
host=$(hostname)
body="Popcorn on $host needs a look:

$new
Open https://$host.exe.xyz to retry a run (\"Retry unfinished\") or rebuild a fix list.
If answers fail with login errors, the Claude Code or Codex login on the VM has expired.
Log: sudo journalctl -u popcorn"
python3 -c 'import json,sys; print(json.dumps({"to": sys.argv[1], "subject": sys.argv[2], "body": sys.argv[3]}))' \
	"$to" "Popcorn on $host: a run needs a look" "$body" |
	curl -fsS -X POST http://169.254.169.254/gateway/email/send -H 'Content-Type: application/json' --data-binary @- >/dev/null
EOF

unit() { # name, contents
	printf '%s\n' "$2" | remote "sudo tee /etc/systemd/system/$1 >/dev/null"
}
unit popcorn-backup.service "[Unit]
Description=Back up the Popcorn database
[Service]
Type=oneshot
User=$user
ExecStart=$dir/bin/popcorn-backup"
unit popcorn-backup.timer "[Unit]
Description=Back up the Popcorn database nightly
[Timer]
OnCalendar=*-*-* 08:00:00 UTC
Persistent=true
[Install]
WantedBy=timers.target"
unit popcorn-watch.service "[Unit]
Description=Email when a Popcorn run needs a look
[Service]
Type=oneshot
User=$user
ExecStart=$dir/bin/popcorn-watch $to"
unit popcorn-watch.timer "[Unit]
Description=Check Popcorn's runs hourly
[Timer]
OnCalendar=hourly
Persistent=true
[Install]
WantedBy=timers.target"
remote "sudo systemctl daemon-reload && sudo systemctl enable --now popcorn-backup.timer popcorn-watch.timer >/dev/null 2>&1"
remote "systemctl list-timers 'popcorn-*' --no-pager | head -4"
echo "ok: nightly backups in $dir/backups, alerts to $to"
