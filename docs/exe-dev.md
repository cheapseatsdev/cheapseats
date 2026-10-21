# Popcorn on exe.dev

An [exe.dev](https://exe.dev) VM runs Popcorn as a systemd service behind exe.dev's HTTPS proxy,
which signs people in by email. Because it's a whole VM, its answerers can be the `claude` and
`codex` CLIs on subscriptions (both come installed) as well as the APIs. exe.dev's own docs are at
`ssh exe.dev doc` (`doc proxy`, `doc sharing`, `doc login-with-exe`, `doc send-email`).

## Set up once

1. **Create the VM.** Its proxy serves `https://<vm>.exe.xyz` from port 8000, where Popcorn listens,
   and is private: only people it's shared with get in, after signing in by email.

   ```bash
   ssh exe.dev "new --name=<vm> --comment='Popcorn'"
   ```

2. **Write its secrets** to `~/popcorn/popcorn.env` on the VM, readable only by its user. The proxy
   passes a signed-in person's email as `X-ExeDev-Email`, and nothing reaches the VM except
   through the proxy, so Popcorn trusts that header; `POPCORN_ALLOWED_EMAILS` narrows it further
   (emails, or `@domain`). `POPCORN_HOSTS` names the host the proxy serves it as, which MCP needs (see step 7).

   ```bash
   { printf 'ANTHROPIC_API_KEY=%s\n' "<key>"
     printf 'POPCORN_AUTH_HEADER=X-ExeDev-Email\n'
     printf 'POPCORN_ALLOWED_EMAILS=%s\n' "you@example.com,@yourcompany.com"
     printf 'POPCORN_HOSTS=%s\n' "<vm>.exe.xyz"   # and a custom domain, comma-separated
     printf 'POPCORN_TOKENS=%s\n' "$(openssl rand -hex 32)"
   } | ssh <vm>.exe.xyz 'mkdir -p ~/popcorn && umask 077 && cat > ~/popcorn/popcorn.env'
   ```

   Add `OPENAI_API_KEY` if an answerer uses `provider = "openai"`.

3. **Log the CLIs in**, if your answerers use them. The subscription is whoever logs in.

   ```bash
   ssh -t <vm>.exe.xyz claude                        # /login, then /exit
   ssh -t <vm>.exe.xyz codex login --device-auth
   ```

4. **Deploy** (below), with `SEED_DB=path/to/popcorn.db` the first time to start from an existing
   database.

5. **Backups and alerts:** a nightly copy of the database (the newest 14 kept, in
   `~/popcorn/backups`), and an hourly check that emails when a run ends partial, failed or out of
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
   `X-Exedev-Authorization`; the proxy then passes your email, and Popcorn signs you in as usual.

   ```bash
   tok=$(ssh exe.dev ssh-key generate-api-key --vm=<vm> --label=claude-code --exp=90d --json |
     python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
   claude mcp add -s user --transport http popcorn https://<vm>.exe.xyz/api/mcp \
     --header "X-Exedev-Authorization: Bearer $tok"
   unset tok
   ```

   Revoke it with `ssh exe.dev ssh-key remove claude-code`. Without `POPCORN_HOSTS`, MCP answers
   `403 invalid Host header`: the proxy reaches Popcorn over loopback, and Popcorn refuses a
   loopback request under a name it wasn't told about, against DNS rebinding.

## Every deploy

From a checkout of this repo, with your config repo committed:

```bash
deploy/exe-dev.sh <vm> ../your-config
```

It builds a Linux binary, ships the config as committed (recording its commit on every run), runs
`popcorn check` on the VM, swaps both in, restarts, and rolls back to the previous binary and config
if Popcorn doesn't come up healthy. Runs in progress resume after the restart.

## Running it

| | |
|---|---|
| log | `ssh <vm>.exe.xyz sudo journalctl -u popcorn -f` |
| restart | `ssh <vm>.exe.xyz sudo systemctl restart popcorn` |
| secrets | `~/popcorn/popcorn.env` on the VM; restart after changing it |
| database | `~/popcorn/data`, with `popcorn.db.bak-v<N>` copies from before schema changes |
| backups | `~/popcorn/backups`, nightly at 08:00 UTC; they undo mistakes, not the loss of the VM |
| alerts | `deploy/exe-dev-ops.sh <vm> <email>` again to change who gets them |
| who's in | `ssh exe.dev share show <vm>`, and `POPCORN_ALLOWED_EMAILS` |

To roll back by hand: stop Popcorn, put `bin/popcorn.prev` and `config.prev` back as `bin/popcorn`
and `config`, and start it. A database from a newer Popcorn won't open in an older one; restore the
`popcorn.db.bak-v<N>` from before the upgrade too.
