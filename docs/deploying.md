# Deploying Cheap Seats

Start with the [config template](https://github.com/cheapseatsdev/cheapseats-config-template): its
workflows check and plan pull requests and deploy merges. Its
[deployment guides](https://github.com/cheapseatsdev/cheapseats-config-template/blob/main/deploy/README.md)
cover Docker hosts and Kubernetes.

Cheap Seats releases an image, `ghcr.io/cheapseatsdev/cheapseats:<version>`, and a matching Helm chart,
`oci://ghcr.io/cheapseatsdev/charts/cheapseats`. Build your config into a deployment image:

```dockerfile
FROM ghcr.io/cheapseatsdev/cheapseats:0.1.0
COPY cheapseats.toml /config/cheapseats.toml
COPY prompt-sets /config/prompt-sets
ARG REVISION=""
ENV CHEAPSEATS_CONFIG_REV=$REVISION
```

Use one tag per config commit on either host. Roll back by deploying the previous tag; upgrade
Cheap Seats by reviewing the `FROM` change. You can also mount a checkout at `/config`, as in the
README.

## What any deployment needs

- **One instance, one volume.** Run progress lives in the process and the database is SQLite on
  `/data`; never run two against the same data. The Helm chart runs one replica and replaces it
  rather than rolling.
- **Secrets in the environment:** `CHEAPSEATS_TOKENS`, `ANTHROPIC_API_KEY` (grading),
  `OPENAI_API_KEY` for OpenAI answerers, and `TYPESAFE_API_KEY` or `EXA_API_KEY` only with
  `[typesafe]` or `[exa]` in the config. Key-adding `CHEAPSEATS_*_BASE_URL` proxies can replace provider
  keys; see [Optional providers](optional-providers.md). The image has no Claude Code or Codex CLI;
  `cheapseats check` fails on a config that needs one. A writable server refuses to listen beyond
  localhost without tokens or a trusted signing-in proxy (`CHEAPSEATS_AUTH_HEADER`;
  [exe.dev setup](exe-dev.md)).
- **Time to stop.** On SIGTERM Cheap Seats marks runs in progress as interrupted, and resumes them on
  the next start; give it 30 seconds.
- **Probes:** `GET /healthz` answers `ok` without a token.
- **Request limits:** one minute to send a request; two to start the next on an idle connection.
  Bodies are at most 1 MiB (4 KiB for `/login`); a fix attempt's link is at most 2048 bytes and its
  note 4096. Responses have no timeout: allow more than ten minutes at the proxy for MCP's
  `wait_for_run`, and support its open event stream.
- **TLS in front.** Tokens travel as bearer headers, so serve Cheap Seats over HTTPS only. The
  Helm chart refuses `ingress.enabled` without `ingress.tls`, unless `ingress.externalTLS: true`
  says TLS ends before the Ingress (a cloud load balancer or CDN). It also refuses a key or token
  as a literal value, in `env` or `extraEnv`: those come from a Secret.
- **Share links:** `GET /share/<token>` answers without a token, serving only that link's run
  ([sharing](sharing.md)). Set `CHEAPSEATS_PUBLIC_URL` to the address people reach Cheap Seats at, so the
  links point there; a proxy that signs people in must let `/share/` through for them to work.
- **TLS at the proxy:** the `/login` cookie contains the token. Set `CHEAPSEATS_PUBLIC_URL` to HTTPS
  or send `X-Forwarded-Proto: https` so the cookie is `Secure` (HTTPS only).

## Backups

`cheapseats backup <file>` takes a consistent copy of the live database (SQLite's `VACUUM INTO`); copying
`cheapseats.db` while Cheap Seats runs can miss recent writes still in its write-ahead log.

- Docker: `docker compose exec cheapseats /app/cheapseats backup /data/backup.db`, then
  `docker compose cp cheapseats:/data/backup.db .`
- Kubernetes: `kubectl exec deploy/cheapseats -- /app/cheapseats backup /data/backup.db`; the image has no
  shell or `tar`, so copy it off the volume with a pod that mounts the claim.
- exe.dev: `deploy/exe-dev-ops.sh` sets up nightly copies (see [exe-dev.md](exe-dev.md)).

Cheap Seats also copies the database to `cheapseats.db.bak-v<N>` before any schema change.

The database holds answers, fixes and share-link records. It is private to the service user:
the data directory is `0700`; the database, its `-wal` and `-shm`, the `bak-v<N>` copies and
`cheapseats backup` files are `0600`. A data directory or database left more open by an earlier
release, or copied in, is tightened at start. Symlinks are left alone; a mode it can't change
(a mount owned by another user) is logged, not fatal.

## Releasing (maintainers)

Push a tag `vX.Y.Z` on `main`. The release workflow tests, builds the image for linux/amd64 and
linux/arm64, pushes it as `X.Y.Z` and `X.Y`, pushes the chart at `X.Y.Z`, and drafts a GitHub
release with the chart attached; edit the notes (call out anything that changes how answers are
graded, since that restarts trends) and publish. Run the workflow by hand to build without
publishing. `deploy/kind-smoke.sh` runs the chart on a local kind cluster; CI runs it on every
push.
