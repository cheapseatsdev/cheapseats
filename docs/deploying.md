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
- **Share links:** `GET /share/<token>` answers without a token, serving only that link's run
  ([sharing](sharing.md)). Set `CHEAPSEATS_PUBLIC_URL` to the address people reach Cheap Seats at, so the
  links point there; a proxy that signs people in must let `/share/` through for them to work.

## Upgrading from Popcorn

Cheap Seats was called Popcorn until v0.3.0. An existing deployment upgrades without changes, and
can rename at its own pace:

- **Environment.** Every `POPCORN_*` variable still works, with a warning at startup naming the
  ones to rename to `CHEAPSEATS_*`. Where both are set, `CHEAPSEATS_*` wins.
- **Files.** A config repo's `popcorn.toml` and a data directory's `popcorn.db` are used where
  `cheapseats.toml` and `cheapseats.db` don't exist. Rename them only with Cheap Seats stopped.
- **Image and chart.** They move to `ghcr.io/cheapseatsdev/cheapseats` and
  `oci://ghcr.io/cheapseatsdev/charts/cheapseats`. The old ones stay pullable at the versions
  they have. Change your config image's `FROM` line.
- **Helm.** Upgrade a release installed from the popcorn chart with `--set nameOverride=popcorn`
  (or `nameOverride: popcorn` in its values). Without it the chart's resources get new names: the
  upgrade fails on the Deployment's selector, and a new volume claim would start on an empty disk.
- **exe.dev.** `deploy/exe-dev.sh` moves `~/popcorn` to `~/cheapseats` on its first run, with the
  service stopped, and copies the database to `backups/pre-rename.db` first.
- **Sign-ins, audits, MCP.** Browsers signed in stay signed in. A robots.txt rule for
  `Popcorn-Audit` still applies to the site audit, which now sends `CheapSeats-Audit`. An MCP
  client keeps working at the same URL; its server is now named `cheapseats-<brand>`.

## Backups

`cheapseats backup <file>` takes a consistent copy of the live database (SQLite's `VACUUM INTO`); copying
`cheapseats.db` while Cheap Seats runs can miss recent writes still in its write-ahead log.

- Docker: `docker compose exec cheapseats /app/cheapseats backup /data/backup.db`, then
  `docker compose cp cheapseats:/data/backup.db .`
- Kubernetes: `kubectl exec deploy/cheapseats -- /app/cheapseats backup /data/backup.db`; the image has no
  shell or `tar`, so copy it off the volume with a pod that mounts the claim.
- exe.dev: `deploy/exe-dev-ops.sh` sets up nightly copies (see [exe-dev.md](exe-dev.md)).

Cheap Seats also copies the database to `cheapseats.db.bak-v<N>` before any schema change.

## Releasing (maintainers)

Push a tag `vX.Y.Z` on `main`. The release workflow tests, builds the image for linux/amd64 and
linux/arm64, pushes it as `X.Y.Z` and `X.Y`, pushes the chart at `X.Y.Z`, and drafts a GitHub
release with the chart attached; edit the notes (call out anything that changes how answers are
graded, since that restarts trends) and publish. Run the workflow by hand to build without
publishing. `deploy/kind-smoke.sh` runs the chart on a local kind cluster; CI runs it on every
push.
