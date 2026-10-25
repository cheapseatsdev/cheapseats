# Cheap Seats on exe.dev

An [exe.dev](https://exe.dev) VM runs Cheap Seats as a systemd service behind exe.dev's HTTPS proxy,
which signs people in by email. Because it's a whole VM, its answerers can be the `claude` and
`codex` CLIs on subscriptions (both come installed) as well as the APIs. exe.dev's own docs are at
`ssh exe.dev doc` (`doc proxy`, `doc sharing`, `doc login-with-exe`, `doc send-email`).

## Set up once

1. **Create the VM.** Its proxy serves `https://<vm>.exe.xyz` from port 8000, where Cheap Seats listens,
   and is private: only people it's shared with get in, after signing in by email.

   ```bash
   ssh exe.dev "new --name=<vm> --comment='Cheap Seats'"
   ```

2. **Write its secrets** to `~/cheapseats/cheapseats.env` on the VM, readable only by its user. The proxy
   passes a signed-in person's email as `X-ExeDev-Email`, and nothing reaches the VM except
   through the proxy (and on the VM, the CLI user is kept off the port: step 3), so Cheap Seats
   trusts that header; `CHEAPSEATS_ALLOWED_EMAILS` narrows it further
   (emails, or `@domain`). `CHEAPSEATS_HOSTS` names the host the proxy serves it as, which MCP needs (see step 7).

   ```bash
   { printf 'ANTHROPIC_API_KEY=%s\n' "<key>"
     printf 'CHEAPSEATS_AUTH_HEADER=X-ExeDev-Email\n'
     printf 'CHEAPSEATS_ALLOWED_EMAILS=%s\n' "you@example.com,@yourcompany.com"
     printf 'CHEAPSEATS_HOSTS=%s\n' "<vm>.exe.xyz"   # and a custom domain, comma-separated
     printf 'CHEAPSEATS_TOKENS=%s\n' "$(openssl rand -hex 32)"
   } | ssh <vm>.exe.xyz 'mkdir -p ~/cheapseats && umask 077 && cat > ~/cheapseats/cheapseats.env'
   ```

   Add `OPENAI_API_KEY` if an answerer uses `provider = "openai"`.

   To keep the Anthropic key off the VM, add it as an exe.dev integration instead, and replace the
   `ANTHROPIC_API_KEY` line with `CHEAPSEATS_ANTHROPIC_BASE_URL=https://anthropic.int.exe.xyz`.
   Cheap Seats' own API calls (grading, the fix list, `anthropic` answerers) then go through that
   proxy, which adds the key. The `claude` CLI doesn't read this variable, so `claude-code`
   answerers stay on your subscription.

   The optional providers work the same way: with `[typesafe]` or `[exa]` in the config, add each
   as an integration and set `CHEAPSEATS_TYPESAFE_BASE_URL=https://typesafe.int.exe.xyz` and
   `CHEAPSEATS_EXA_BASE_URL=https://exa.int.exe.xyz` instead of `TYPESAFE_API_KEY` and
   `EXA_API_KEY`. See [Optional providers](optional-providers.md).

3. **Deploy** (below), with `SEED_DB=path/to/cheapseats.db` the first time to start from an existing
   database. The deploy also makes a `cheapseats-cli` user for the `claude` and `codex` CLIs to run
   as: each is an agent with tools the model drives, and this keeps the VM's env file, database
   and logins out of a prompt-injected answer's reach whatever a CLI update adds. Since anything
   on the VM that connects to Cheap Seats' port could send its own `X-ExeDev-Email`, an nftables
   rule (`/etc/cheapseats/cli-firewall.nft`, loaded by `cheapseats-cli-firewall.service`, which
   Cheap Seats won't start without) refuses that user's connections to the port, and the deploy
   fails if the CLI user can still reach it.

4. **Log the CLIs in** as that user, if your answerers use them. The subscription is whoever logs
   in. (Logins made before the CLI user existed move over on the first deploy.)

   ```bash
   ssh -t <vm>.exe.xyz sudo -H -u cheapseats-cli claude                      # /login, then /exit
   ssh -t <vm>.exe.xyz sudo -H -u cheapseats-cli codex login --device-auth
   ```

5. **Backups and alerts:** a nightly copy of the database (the newest 14 kept, in
   `~/cheapseats/backups`), and an hourly check that emails when a run ends partial, failed or out of
   budget, a fix list fails, or a run stalls. The address must be one exe.dev lets the VM write
   to: its owner, a teammate, or someone who has signed in to it.

   ```bash
   deploy/exe-dev-ops.sh <vm> you@example.com
   ```

6. **Share it** with whoever should see it (web access only; they sign in with that email, and get
   an invitation if they're new to exe.dev):

   ```bash
   ssh exe.dev share add <vm> someone@example.com
   ```

7. Connect Claude Code (or any MCP client). The proxy only lets in people who signed in by email,
   so a client gets through with an exe.dev token scoped to the VM, sent as
   `X-Exedev-Authorization`; the proxy then passes your email, and Cheap Seats signs you in as usual.

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

From a checkout of this repo, with your config repo committed:

```bash
deploy/exe-dev.sh <vm> ../your-config
```

It builds a Linux binary, ships the config as committed (recording its commit on every run, and
refusing a config with committed symlinks, which would point outside it on the VM), runs
`cheapseats check` on the VM, swaps both in, restarts, and rolls back to the previous binary and config
if Cheap Seats doesn't come up healthy. Runs in progress resume after the restart.

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
and `config`, and start it. A database from a newer Cheap Seats won't open in an older one; restore the
`cheapseats.db.bak-v<N>` from before the upgrade too.
