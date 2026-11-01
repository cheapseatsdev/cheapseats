# Cheap Seats on exe.dev

Run Cheap Seats as a systemd service behind [exe.dev](https://exe.dev)'s email sign-in and HTTPS
proxy. The VM supports API answerers and the installed `claude` and `codex` CLIs on subscriptions.
exe.dev's own docs are at
`ssh exe.dev doc` (`doc proxy`, `doc sharing`, `doc login-with-exe`, `doc send-email`).

For a first local run, [choose Go with subscriptions or Docker with API keys](../README.md#quick-start).
This guide covers the VM setup; its deploy script builds the app on your machine with Go 1.27 or later.

## Set up once

1. **Create the VM.** Its proxy serves `https://<vm>.exe.xyz` from port 8000, where Cheap Seats listens,
   and is private: only people it's shared with get in, after signing in by email.

   ```bash
   ssh exe.dev "new --name=<vm> --comment='Cheap Seats'"
   ```

2. **Write its access settings** to `~/cheapseats/cheapseats.env`, readable only by the VM user. Cheap Seats
   trusts the proxy's `X-ExeDev-Email` only because outside requests arrive through that proxy
   and the CLI user is blocked from the port (step 3). `CHEAPSEATS_ALLOWED_EMAILS` restricts access
   to emails or `@domain`; `CHEAPSEATS_HOSTS` names the proxy's host for MCP (step 7).

   ```bash
   { printf 'CHEAPSEATS_AUTH_HEADER=X-ExeDev-Email\n'
     printf 'CHEAPSEATS_ALLOWED_EMAILS=%s\n' "you@example.com,@yourcompany.com"
     printf 'CHEAPSEATS_HOSTS=%s\n' "<vm>.exe.xyz"   # and a custom domain, comma-separated
     printf 'CHEAPSEATS_TOKENS=%s\n' "$(openssl rand -hex 32)"
   } | ssh <vm>.exe.xyz 'mkdir -p ~/cheapseats && umask 077 && cat > ~/cheapseats/cheapseats.env'
   ```

   **Subscriptions need no model API keys:** use `provider = "claude-code"` for the Claude answerer,
   grader and analyst, and optionally `provider = "codex"` for a second answerer. The
   [subscription example](../examples/cheapseats-config/cheapseats.subscriptions.toml) supplies
   this config. Log the CLIs in as the service's CLI user in step 4.

   **API roles need their key or a proxy:** add `ANTHROPIC_API_KEY` if any answerer, grader or
   analyst uses `anthropic` (the grader and analyst's default provider). Add `OPENAI_API_KEY`
   if an answerer uses `provider = "openai"`.

   To keep an Anthropic key off the VM, add it as an exe.dev integration and set
   `CHEAPSEATS_ANTHROPIC_BASE_URL=https://anthropic.int.exe.xyz` instead of `ANTHROPIC_API_KEY`.
   Roles configured for `anthropic` then go through that proxy, which adds the key. The `claude`
   CLI doesn't read this variable, so `claude-code` roles stay on your subscription.

   The optional providers work the same way: with `[typesafe]` or `[exa]` in the config, add each
   as an integration and set `CHEAPSEATS_TYPESAFE_BASE_URL=https://typesafe.int.exe.xyz` and
   `CHEAPSEATS_EXA_BASE_URL=https://exa.int.exe.xyz` instead of `TYPESAFE_API_KEY` and
   `EXA_API_KEY`. See [Optional providers](optional-providers.md).

3. **Deploy** (below), with `SEED_DB=path/to/cheapseats.db` the first time to start from an existing
   database. The deploy creates a separate `cheapseats-cli` user for the model-driven CLIs,
   keeping the service user's env file, database and logins out of their reach. Local connections
   could forge `X-ExeDev-Email`, so nftables blocks that user's connections to Cheap Seats' port.
   The rule lives at `/etc/cheapseats/cli-firewall.nft`; Cheap Seats requires its
   `cheapseats-cli-firewall.service`, and deployment fails if the CLI user can reach the port.

4. **Log the CLIs in** as that user: Claude if any answerer, grader or analyst uses `claude-code`,
   and Codex only if an answerer uses `codex`. The subscription is whoever logs in. (Logins made
   before the CLI user existed move over on the first deploy.)

   ```bash
   ssh -t <vm>.exe.xyz sudo -H -u cheapseats-cli claude                      # /login, then /exit
   ssh -t <vm>.exe.xyz sudo -H -u cheapseats-cli codex login --device-auth
   ```

5. **Backups and alerts:** a nightly copy of the database (the newest 14 kept, in
   `~/cheapseats/backups`), and an hourly check that emails when a run ends partial, failed or out of
   budget, a fix list fails, or a run stalls. Each problem is sent once; one the email gateway
   refused is tried again the next hour. A fix list failure is sent whenever it happens, including
   a rebuild of an old run. Another rebuild that fails with the same error as the last one sent is
   not sent again; a different error, an earlier error coming back, or a failure after a good build
   is. Earlier versions sent only the first fix list failure of each run, and only for runs from the
   last two days. The first check after upgrading from one sends a recent run's fix list failure
   unless that version had sent one for the run; it doesn't send an old run's, or check whether the
   failure that version sent is the one there now, so a later one it never sent stays unsent until
   the run's fix list fails differently. The address must be one exe.dev lets the VM write to: its
   owner, a teammate, or someone who has signed in to it.

   The same check emails when a set's newest day's **recommended** rate fell beyond noise against
   the previous run day, for either track: the Rankings page's paired day-over-day test (see
   [statistics](statistics.md#rankings)), read from `/api/rankings` over the last 8 days with the
   first token in `CHEAPSEATS_TOKENS`. Daily and weekly sets are compared; a set that runs less
   often than weekly is not. Named and top-pick rates are not checked. Each set, track and day is
   sent once. The test's margin is 95%, so a drop outside noise happens by chance about once in
   40 runs of each set and track; read it on Rankings before acting. Without a token, or when
   Cheap Seats doesn't answer, only runs are checked. The watch reads Cheap Seats' port from its
   service on the VM, so a deploy on another port needs nothing extra.

   ```bash
   deploy/exe-dev-ops.sh <vm> you@example.com
   ```

6. **Share it** with whoever should see it (web access only; they sign in with that email, and get
   an invitation if they're new to exe.dev):

   ```bash
   ssh exe.dev share add <vm> someone@example.com
   ```

7. **Connect an MCP client.** Use a VM-scoped exe.dev token in `X-Exedev-Authorization`; the proxy
   passes your email to Cheap Seats. For Claude Code:

   ```bash
   tok=$(ssh exe.dev ssh-key generate-api-key --vm=<vm> --label=claude-code --exp=90d --json |
     python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
   claude mcp add -s user --transport http cheapseats https://<vm>.exe.xyz/api/mcp \
     --header "X-Exedev-Authorization: Bearer $tok"
   unset tok
   ```

   Revoke it with `ssh exe.dev ssh-key remove claude-code`. Without `CHEAPSEATS_HOSTS`, MCP answers
   `403 invalid Host header`: the proxy reaches Cheap Seats over loopback, and Cheap Seats refuses a
   loopback request under a name it wasn't told about, against DNS rebinding.

## Every deploy

From a checkout of this repo, with Go 1.27 or later on the machine running the script and your
config repo committed:

```bash
deploy/exe-dev.sh <vm> ../your-config
```

The script builds a Linux binary, ships the committed config, runs `cheapseats check` on the VM
and restarts. It records the config commit on every run and refuses committed symlinks, which
could reach outside the config. Runs in progress resume after restart.

If startup fails, it restores the previous binary and config, except in three cases, where it
leaves Cheap Seats stopped and prints what to do:

- **The new build migrated the database.** The script compares the database's schema (`PRAGMA
  user_version`, read with `sqlite3`, which it installs if missing) before the swap and after the
  failure. A migration that failed and rolled back leaves the schema as it was, and the rollback
  goes ahead. A database at a later schema can't be opened by the previous build, and putting the
  pre-migration copy back would discard what the new build wrote after it. The script prints the
  steps to restore `cheapseats.db.bak-v<N>`, `N` being the schema before the deploy, with the
  previous build; or you fix the build and deploy again.
- **The schema can't be read after the failure,** so the script can't tell whether the build
  migrated it. It prints the command to read it.
- **There is no previous build**, on a first deploy.

Left stopped, Cheap Seats is also disabled, so a reboot of the VM doesn't start the failed build;
the next deploy enables it again, and `sudo systemctl enable --now cheapseats` starts it by hand.
If there was no database before the deploy and the failed build made one, at a schema the previous
build may not know, the rollback moves it aside as `cheapseats.db.failed-<time>` and the previous
build starts without one, as it did before. If the schema can't be read before the swap, the deploy
stops there, with nothing changed, and starts Cheap Seats again if it was running. The stop, the
schema read and the swap run as one script on the VM, which finishes even if the connection drops.

With `LIVE_CHECK=1`, the check on the VM also runs `cheapseats check -live`
([the live schema check](deploying.md#the-live-schema-check)). It sends each structured output
schema to the API once, using the service's `cheapseats.env`, and stops the deploy before the swap
if the API refuses one. It costs a few cents and is capped at `LIVE_BUDGET` dollars (default
`0.25`). Use it on a release with schema changes when the VM's grader or analyst uses the API.

## Running it

| | |
|---|---|
| log | `ssh <vm>.exe.xyz sudo journalctl -u cheapseats -f` |
| restart | `ssh <vm>.exe.xyz sudo systemctl restart cheapseats` |
| secrets | `~/cheapseats/cheapseats.env` on the VM; restart after changing it |
| database | `~/cheapseats/data`, with `cheapseats.db.bak-v<N>` copies from before schema changes |
| backups | `~/cheapseats/backups`, nightly at 08:00 UTC; they undo mistakes, not the loss of the VM |
| file modes | the service runs with `UMask=0077`: data and backups are `0700`, the database and every copy `0600` |
| alerts | `deploy/exe-dev-ops.sh <vm> <email>` again to change who gets them |
| who's in | `ssh exe.dev share show <vm>`, and `CHEAPSEATS_ALLOWED_EMAILS` |

To roll back by hand: stop Cheap Seats, put `bin/cheapseats.prev` and `config.prev` back as `bin/cheapseats`
and `config`, and start it with `sudo systemctl enable --now cheapseats` (a failed deploy that left it stopped
also disabled it). A database from a newer Cheap Seats won't open in an older one; restore the
`cheapseats.db.bak-v<N>` from before the upgrade too, `N` being the schema the older build knows
(`bin/cheapseats.prev version` prints it), moving `cheapseats.db` and its `-wal` and `-shm` aside first
(the failed deploy prints these commands). An upgrade that failed partway can leave copies at later
schemas too; the older build can't open those.
