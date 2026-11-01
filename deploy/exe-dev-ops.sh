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
# Emails each new problem once (one the gateway refuses goes again next hour). Runs that ended
# partial, failed or out of budget count for two days after they finish; a failed fix list counts
# whenever it fails, since a rebuild can be of any run, however old; a run counts as stalled after
# two hours with no answer finished. Runs on the VM (Linux): GNU date and systemd.
set -euo pipefail
to=$1
dir="$HOME/cheapseats"
seen="$dir/watch.seen"
touch "$seen"
db="file:$dir/data/cheapseats.db?mode=ro"
since=$(date -u -d '2 days ago' +%Y-%m-%dT%H:%M:%SZ)
stale=$(date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%SZ)
# Each line is "key<TAB>recent<TAB>message"; a key goes in the seen file once its message is sent.
# A failed fix list keeps its error until a good build replaces it, so its key is the run, its
# fixes_at and a checksum of the error. fixes_at changes only when an analysis is stored (a good
# build, or a first build that kept a degraded one); a rebuild that fails leaves it as it was. A
# failed fix list is new when its key differs from the last one sent for that run: the same
# failure again, whether next hour or from another rebuild, is not; a different error is, and so is
# an error that comes back after another one, or a failure after a good build. recent is 1 for a
# run finished in the last two days.
problems=$(sqlite3 -separator $'\t' "$db" "
  SELECT id || ' ' || status, 1, id || ' ' || status || ': ' || name || ' — ' || replace(replace(COALESCE(error, ''), char(10), ' '), char(9), ' ') FROM runs
    WHERE status IN ('partial','failed','budget') AND finished_at >= '$since';
  SELECT id || ' fix list failed ' || COALESCE(fixes_at, 'never'), COALESCE(finished_at, '') >= '$since', id || ' fix list failed: ' || name || ' — ' || replace(replace(fixes_error, char(10), ' '), char(9), ' ') FROM runs
    WHERE fixes_error IS NOT NULL;
  SELECT r.id || ' stalled', 1, r.id || ' stalled: ' || r.name || ' — no answer finished since ' || COALESCE(MAX(x.finished_at), r.started_at)
    FROM runs r LEFT JOIN results x ON x.run_id = r.id
    WHERE r.status = 'running' GROUP BY r.id
    HAVING COALESCE(MAX(x.finished_at), r.started_at) < '$stale';")
# Earlier versions keyed a failed fix list "<run> fix list failed", sent only the first fix list
# failure of each run and looked only at runs finished in the last two days, so their seen file
# holds none of these keys. The first time this version runs, it records a failed fix list already
# there as seen, without an email, if the run finished more than two days ago (an earlier version
# never looked at it) or the old key is in the seen file. The old key says an earlier version sent
# a fix list failure for the run, not that it was this one: one that replaced it later was never
# sent then, and isn't sent now either. A failed fix list of a recent run without the old key is
# sent, as an earlier version would have sent it. Then it marks the seen file, and from there a
# failure is sent whenever it differs from the last one sent for its run.
upgrade=""
if ! grep -qxF '# keys v2' "$seen"; then
	upgrade=1
fi
# The last key sent for a fix list of the run in $1: the seen file only grows, so the last line.
last_fixes() {
	awk -v p="$1 fix list failed " 'index($0, p) == 1 { k = $0 } END { print k }' "$seen"
}
new=""
keys="" # written to the seen file once the email has gone, so a failed send is tried again
while IFS=$'\t' read -r key recent line; do
	[ -z "$key" ] && continue
	case $key in
	*' fix list failed '*)
		run=${key%% *}
		old="$run fix list failed" # as earlier versions keyed it
		key="$key $(printf '%s' "$line" | cksum | cut -d' ' -f1)"
		if [ -n "$upgrade" ] && ! grep -qxF "$key" "$seen" && { [ "$recent" != 1 ] || grep -qxF "$old" "$seen"; }; then
			printf '%s\n' "$key" >>"$seen"
			continue
		fi
		if [ "$(last_fixes "$run")" = "$key" ]; then
			continue
		fi
		;;
	*)
		if grep -qxF "$key" "$seen"; then
			continue
		fi
		;;
	esac
	new="$new$line"$'\n'
	keys="$keys$key"$'\n'
done <<<"$problems"
if [ -n "$upgrade" ]; then
	printf '%s\n' '# keys v2' >>"$seen"
fi
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
printf '%s' "$keys" >>"$seen"
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
