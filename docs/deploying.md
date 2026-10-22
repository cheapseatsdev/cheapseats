# Deploying Popcorn

Popcorn ships as an image, `ghcr.io/metcalfc/popcorn:<version>`, and a Helm chart,
`oci://ghcr.io/metcalfc/charts/popcorn`, released together at the same version. The image has no
config in it: each deployment builds its own image on top, from a config repo:

```dockerfile
FROM ghcr.io/metcalfc/popcorn:0.1.0
COPY popcorn.toml /config/popcorn.toml
COPY prompt-sets /config/prompt-sets
ARG REVISION=""
ENV POPCORN_CONFIG_REV=$REVISION
```

That image is one artifact per config commit: the same thing runs on a Docker host or Kubernetes,
rolling back is deploying the previous tag, and upgrading Popcorn is a reviewed change to the
`FROM` line. The [config template](https://github.com/metcalfc/popcorn-config-template) sets all of
this up, with workflows that check and plan every pull request and deploy every merge, and
[guides](https://github.com/metcalfc/popcorn-config-template/blob/main/deploy/README.md) for a
Docker host and for Kubernetes. Start there.

Mounting a checkout at `/config` instead of building an image works too, as in the README.

## What any deployment needs

- **One instance, one volume.** Run progress lives in the process and the database is SQLite on
  `/data`; never run two against the same data. The Helm chart runs one replica and replaces it
  rather than rolling.
- **Secrets in the environment:** `POPCORN_TOKENS` (Popcorn refuses to listen beyond localhost
  without it), `ANTHROPIC_API_KEY` (grading), `OPENAI_API_KEY` for OpenAI answerers. Deployed
  answerers use the APIs: the image has no Claude Code or Codex CLI, and `popcorn check` fails on a
  config that needs one.
- **Time to stop.** On SIGTERM Popcorn marks runs in progress as interrupted, and resumes them on
  the next start; give it 30 seconds.
- **Probes:** `GET /healthz` answers `ok` without a token.
- **Share links:** `GET /share/<token>` answers without a token, serving only that link's run
  ([sharing](sharing.md)). Set `POPCORN_PUBLIC_URL` to the address people reach Popcorn at, so the
  links point there; a proxy that signs people in must let `/share/` through for them to work.

## Backups

`popcorn backup <file>` takes a consistent copy of the live database (SQLite's `VACUUM INTO`); copying
`popcorn.db` while Popcorn runs can miss recent writes still in its write-ahead log.

- Docker: `docker compose exec popcorn /app/popcorn backup /data/backup.db`, then
  `docker compose cp popcorn:/data/backup.db .`
- Kubernetes: `kubectl exec deploy/popcorn -- /app/popcorn backup /data/backup.db`; the image has no
  shell or `tar`, so copy it off the volume with a pod that mounts the claim.
- exe.dev: `deploy/exe-dev-ops.sh` sets up nightly copies (see [exe-dev.md](exe-dev.md)).

Popcorn also copies the database to `popcorn.db.bak-v<N>` before any schema change.

## Releasing (maintainers)

Push a tag `vX.Y.Z` on `main`. The release workflow tests, builds the image for linux/amd64 and
linux/arm64, pushes it as `X.Y.Z` and `X.Y`, pushes the chart at `X.Y.Z`, and drafts a GitHub
release with the chart attached; edit the notes (call out anything that changes how answers are
graded, since that restarts trends) and publish. Run the workflow by hand to build without
publishing. `deploy/kind-smoke.sh` runs the chart on a local kind cluster; CI runs it on every
push.
