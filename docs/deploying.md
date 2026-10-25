# Deploying Cheap Seats

Cheap Seats ships as an image, `ghcr.io/cheapseatsdev/cheapseats:<version>`, and a Helm chart,
`oci://ghcr.io/cheapseatsdev/charts/cheapseats`, released together at the same version. The image has no
config in it: each deployment builds its own image on top, from a config repo:

```dockerfile
FROM ghcr.io/cheapseatsdev/cheapseats:0.1.0
COPY cheapseats.toml /config/cheapseats.toml
COPY prompt-sets /config/prompt-sets
ARG REVISION=""
ENV CHEAPSEATS_CONFIG_REV=$REVISION
```

That image is one artifact per config commit: the same thing runs on a Docker host or Kubernetes,
rolling back is deploying the previous tag, and upgrading Cheap Seats is a reviewed change to the
`FROM` line. The [config template](https://github.com/cheapseatsdev/cheapseats-config-template) sets all of
this up, with workflows that check and plan every pull request and deploy every merge, and
[guides](https://github.com/cheapseatsdev/cheapseats-config-template/blob/main/deploy/README.md) for a
Docker host and for Kubernetes. Start there.

Mounting a checkout at `/config` instead of building an image works too, as in the README.

## What any deployment needs

- **One instance, one volume.** Run progress lives in the process and the database is SQLite on
  `/data`; never run two against the same data. The Helm chart runs one replica and replaces it
  rather than rolling.
- **Secrets in the environment:** `CHEAPSEATS_TOKENS` (Cheap Seats refuses to listen beyond localhost
  without it), `ANTHROPIC_API_KEY` (grading), `OPENAI_API_KEY` for OpenAI answerers, and `TYPESAFE_API_KEY` or `EXA_API_KEY` only with `[typesafe]` or `[exa]` in the config (or their `CHEAPSEATS_*_BASE_URL` proxies; see [Optional providers](optional-providers.md)). Deployed
  answerers use the APIs: the image has no Claude Code or Codex CLI, and `cheapseats check` fails on a
  config that needs one.
- **Time to stop.** On SIGTERM Cheap Seats marks runs in progress as interrupted, and resumes them on
  the next start; give it 30 seconds.
- **Probes:** `GET /healthz` answers `ok` without a token.
- **Request limits:** a client has a minute to send a request and two to start the next on an idle
  connection. A body holds at most 1 MiB (4 KiB for `/login`), a fix attempt's link 2048 bytes and
  its note 4096. Responses aren't time-limited: MCP's `wait_for_run` holds a call open for up to ten
  minutes, so a proxy in front needs a read timeout longer than that.
- **Share links:** `GET /share/<token>` answers without a token, serving only that link's run
  ([sharing](sharing.md)). Set `CHEAPSEATS_PUBLIC_URL` to the address people reach Cheap Seats at, so the
  links point there; a proxy that signs people in must let `/share/` through for them to work.
- **TLS at the proxy:** the `/login` cookie is the token, so it's `Secure` (sent only over https)
  when `CHEAPSEATS_PUBLIC_URL` is https or the proxy sends `X-Forwarded-Proto: https`.

## Backups

`cheapseats backup <file>` takes a consistent copy of the live database (SQLite's `VACUUM INTO`); copying
`cheapseats.db` while Cheap Seats runs can miss recent writes still in its write-ahead log.

- Docker: `docker compose exec cheapseats /app/cheapseats backup /data/backup.db`, then
  `docker compose cp cheapseats:/data/backup.db .`
- Kubernetes: `kubectl exec deploy/cheapseats -- /app/cheapseats backup /data/backup.db`; the image has no
  shell or `tar`, so copy it off the volume with a pod that mounts the claim.
- exe.dev: `deploy/exe-dev-ops.sh` sets up nightly copies (see [exe-dev.md](exe-dev.md)).

Cheap Seats also copies the database to `cheapseats.db.bak-v<N>` before any schema change.

The database holds every answer, fix and share link, so it is private to the user Cheap Seats runs
as: the data directory is `0700`, and the database, its `-wal` and `-shm`, the `bak-v<N>` copies and
`cheapseats backup` files are `0600`. A data directory or database left more open by an earlier
release, or copied in, is tightened at start; symlinks are left alone, and a mode it can't change
(a mount owned by another user) is logged, not fatal.

## Releasing (maintainers)

Push a tag `vX.Y.Z` on `main`. The release workflow tests, builds the image for linux/amd64 and
linux/arm64, pushes it as `X.Y.Z` and `X.Y`, pushes the chart at `X.Y.Z`, and drafts a GitHub
release with the chart attached; edit the notes (call out anything that changes how answers are
graded, since that restarts trends) and publish. Run the workflow by hand to build without
publishing. `deploy/kind-smoke.sh` runs the chart on a local kind cluster; CI runs it on every
push.
