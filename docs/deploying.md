# Deploying Cheap Seats

**Use Docker for the quickest setup with API keys. Use Go for your subscriptions.**

- **API keys:** the released Docker image needs no Go installation or app compilation.
  `ANTHROPIC_API_KEY` is required for Claude answers, grading and the fix list;
  `OPENAI_API_KEY` adds OpenAI answers.
- **Subscriptions:** run locally with Go 1.27 or later and a logged-in `claude` CLI. A logged-in
  `codex` CLI is optional. See the [quick start](../README.md#quick-start). The Docker image
  contains neither subscription CLI.

## Try Docker with API keys

Start with the [config template](https://github.com/cheapseatsdev/cheapseats-config-template).
It uses API providers and measures Cheap Seats as a working example:

```bash
git clone https://github.com/cheapseatsdev/cheapseats-config-template cheapseats-config
cd cheapseats-config
umask 077
printf 'CHEAPSEATS_TOKENS=%s\n' "$(openssl rand -hex 32)" > .env
"${EDITOR:-vi}" .env
```

Keep the generated `CHEAPSEATS_TOKENS` line and add `ANTHROPIC_API_KEY=<your key>` in the editor.
Add `OPENAI_API_KEY=<your key>` for OpenAI answers; without it, that answerer is skipped.
The template ignores `.env` in git.

```bash
docker run --rm --name cheapseats --stop-timeout 30 \
  -p 127.0.0.1:3000:3000 \
  --env-file .env \
  -v "$PWD:/config:ro" \
  -v cheapseats-data:/data \
  ghcr.io/cheapseatsdev/cheapseats:0.6.0
```

Open <http://127.0.0.1:3000> and sign in with the token from `.env`. The container listens on
`0.0.0.0`, so it requires a token even though the host port is bound to loopback. Press Ctrl+C
to stop it; the `cheapseats-data` volume keeps your results.

Edit the brand in `cheapseats.toml` and the questions in `prompt-sets/` on your machine; the
container mounts them read-only. Validate changes before restarting the container:

```bash
docker run --rm --env-file .env -v "$PWD:/config:ro" \
  ghcr.io/cheapseatsdev/cheapseats:0.6.0 check
```

API runs cost money. Check the model IDs, prices and budget in `cheapseats.toml` before running
a set. The budget admits calls against reservations; actual charges can exceed them. See
[Cost and budget](https://cheapseats.dev/docs/cost-and-budget/).

## Build a deployable image

Start with the [config template](https://github.com/cheapseatsdev/cheapseats-config-template): its
workflows check and plan pull requests and deploy merges. Its
[deployment guides](https://github.com/cheapseatsdev/cheapseats-config-template/blob/main/deploy/README.md)
cover Docker hosts and Kubernetes.

Cheap Seats releases an image, `ghcr.io/cheapseatsdev/cheapseats:<version>`, and a matching Helm chart,
`oci://ghcr.io/cheapseatsdev/charts/cheapseats`. Build your config into a deployment image:

```dockerfile
FROM ghcr.io/cheapseatsdev/cheapseats:0.6.0
COPY cheapseats.toml /config/cheapseats.toml
COPY prompt-sets /config/prompt-sets
ARG REVISION=""
ENV CHEAPSEATS_CONFIG_REV=$REVISION
```

Use one tag per config commit on either host. Roll back by deploying the previous tag; upgrade
Cheap Seats by reviewing the `FROM` change. For a local trial,
[mount the config checkout](#try-docker-with-api-keys) instead of building an image.

## What any deployment needs

- **One instance, one volume.** Run progress lives in the process and the database is SQLite on
  `/data`; never run two against the same data. The Helm chart runs one replica and replaces it
  rather than rolling.
- **Secrets in the environment:** `CHEAPSEATS_TOKENS`, `ANTHROPIC_API_KEY` for `anthropic` roles,
  `OPENAI_API_KEY` for OpenAI answerers, and `TYPESAFE_API_KEY` or `EXA_API_KEY` only with
  `[typesafe]` or `[exa]` in the config. Key-adding `CHEAPSEATS_*_BASE_URL` proxies can replace provider
  keys; see [Optional providers](optional-providers.md). The image has no Claude Code or Codex CLI;
  `cheapseats check` fails on a config that needs one. A writable server refuses to listen beyond
  localhost without tokens or a trusted signing-in proxy (`CHEAPSEATS_AUTH_HEADER`;
  [exe.dev setup](exe-dev.md)).
- **Time to stop.** On SIGTERM Cheap Seats marks runs in progress as interrupted, and resumes them on
  the next start; give it 30 seconds.
- **Probes:** `GET /healthz` answers `ok` without a token.
- **Crawlers:** `/robots.txt` disallows `/api/` and leaves pages open; a shared report says
  `noindex` itself. It is served without a token, like `/favicon.ico` and `/apple-touch-icon.png`.
  To keep crawlers off a whole deployment, answer `/robots.txt` at your proxy instead.
- **Request limits:** one minute to send a request; two to start the next on an idle connection.
  Bodies are at most 1 MiB (4 KiB for `/login`); a fix attempt's link is at most 2048 bytes and its
  note 4096. Responses have no timeout: allow more than ten minutes at the proxy for MCP's
  `wait_for_run`, and support its open event stream.
- **TLS in front for remote access.** Tokens travel as bearer headers, so serve Cheap Seats over HTTPS
  when it is reachable beyond your machine. The Helm chart refuses `ingress.enabled` without
  `ingress.tls`, unless `ingress.externalTLS: true`
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

CI uses fake providers, so it can't see the API refuse a request. The **golden** workflow
(`.github/workflows/golden.yml`) does, with the real API, on each release tag and by hand:

- **schemas** runs `cheapseats check -live` with the example config. It sends every structured
  output schema once to its grader or analyst model, with a one-line prompt and 16 output tokens.
  The API refuses a schema it can't take with an HTTP 400 before generating anything, so a run
  costs a few cents. It is capped at $0.25.
- **golden** grades the [golden set](grading.md#the-golden-set) with the real grader.

Run it on `main` before tagging, and publish the draft release only when the tag's run passes.
It needs `ANTHROPIC_API_KEY` as a secret of the `golden` environment (or of the repository), and
fails without it. The environment's required reviewers approve each run. Its deployment rules
must allow `main` and the tag pattern `v*.*.*`, or GitHub refuses the tag's run.

### The live schema check

`cheapseats check -live` sends each structured output schema the app uses (the grade, the fix
list and its review, misstatements, themes, prompt suggestions and edits) once to the model that
sends it in production, through the API with the configured key or proxy. It prints each as
accepted, `REFUSED` (the API's 400, which the check fails on) or not checked (another error,
also a failure). A role on `claude-code` sends nothing through the API and is skipped with a
note; if every role is, the check fails rather than pass having checked nothing.

It is opt-in because it costs money. The budget is checked before each request, `-live-budget`
dollars (default 0.25), so it can go over by at most one request. A model without a `[pricing]`
entry can't be budgeted and fails the check. A new schema joins the check by being declared with
`llm.RegisterSchema`. On exe.dev, `LIVE_CHECK=1 deploy/exe-dev.sh …` runs it on the VM with the
service's own environment before the new build is swapped in ([exe-dev.md](exe-dev.md#every-deploy)).
