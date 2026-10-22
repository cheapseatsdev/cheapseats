# Popcorn

Popcorn is a free, open-source (MIT) AI visibility tracker, inspired by tools like
[Gauge](https://withgauge.com), Profound and Peec AI: the 20% of what they do that you'll use 80% of
the time, running on the Claude and ChatGPT subscriptions you already have. It asks your buyers'
questions, grades whether AI assistants recommend your brand, and builds a ranked list of fixes.

Start here at no cost. If you outgrow it, your prompts are plain text in your own repo, ready to
take to whichever of those tools you move to. Site and docs: [popcorn.zone](https://popcorn.zone/docs/).

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/dashboard-dark.webp">
  <img alt="The dashboard: each prompt set with its named, recommended and rank, and the fixes to make first" src="docs/images/dashboard-light.webp">
</picture>

- Asks each buyer question of each assistant (Claude, ChatGPT) on two tracks:
  - **search**: it must search the web first, so you see what the web says today
  - **recall**: no tools, so you see what the model already believes
- Grades every answer:
  - `recommended`: the top pick, or one of ≤3 primary recommendations it endorses
  - `mentioned`: named, but as one option among many, in passing, or with reservations
  - `absent`: not named at all
- Records your position, the top pick, every product named (share of voice), "why not #1", objections,
  and every search result and citation.
- Ends each run with **Fix these first**: the exact page to create or change, what it must say, the
  prompts it should flip, effort, priority, the site it belongs on and its owner, plus a prompt to
  hand Claude Code.
- **Quick wins** ranks fixes across all sets by prompts flipped per day of work. Mark a fix with
  the commit or PR you submitted (proposed, then accepted) and it leaves the open list; if it
  wasn't enough, the next run's fix for the same page comes back open, with the attempt and what
  changed for its prompts, and the fix list is written knowing what was tried.
- **Rankings** trend each set week to week, through prompt and model changes.
- Web UI, CLI, JSON API and MCP server.
- Self-hosted, MIT licensed: Docker, Kubernetes (Helm chart) or an exe.dev VM. Your prompts live
  in a git repo you own.


## What a run gives you

A run's scores per track, and every prompt against every assistant:

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/run-dark.webp">
  <img alt="A run page: search and recall scores, per-answerer rates, and a prompt-by-answerer grid of verdicts" src="docs/images/run-light.webp">
</picture>

Quick wins: every fix across your sets, filterable by site, gap and set:

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/quickwins-dark.webp">
  <img alt="Quick wins: ranked fixes with priority, gap, effort and the site each belongs on" src="docs/images/quickwins-light.webp">
</picture>

## Quick start

On your subscriptions, with the `claude` and `codex` CLIs installed and logged in: no API key, no
API spend.

```bash
POPCORN_CONFIG=examples/popcorn-config/popcorn.subscriptions.toml go run ./cmd/popcorn serve
```

Or through the APIs:

```bash
cp .env.example .env          # set ANTHROPIC_API_KEY
go run ./cmd/popcorn serve    # the example config, at http://127.0.0.1:3000
```

To measure your own brand, start a config repo from the
[template](https://github.com/metcalfc/popcorn-config-template), then:

```bash
POPCORN_CONFIG=../your-config/popcorn.toml go run ./cmd/popcorn check
POPCORN_CONFIG=../your-config/popcorn.toml go run ./cmd/popcorn serve
```

## Configuration

A config repo holds `popcorn.toml` and `prompt-sets/*.toml`.
[`examples/popcorn-config`](examples/popcorn-config) is a complete one, used when `POPCORN_CONFIG`
is unset.

- `[brand]`: name, aliases, `about`, `audience`, public `facts`, and `[[brand.sites]]` (url, kind, owner)
- `[[answerers]]`: who answers, by provider:

  | provider | through | billed |
  | --- | --- | --- |
  | `anthropic` | the API (`ANTHROPIC_API_KEY`) | per token |
  | `openai` | the API (`OPENAI_API_KEY`) | per token |
  | `claude-code` | the local `claude` CLI | your Claude subscription |
  | `codex` | the local `codex` CLI | your ChatGPT subscription |

- `[grader]`, `[analyst]`: Claude models, through the API (`ANTHROPIC_API_KEY`, or a proxy that
  adds it in `POPCORN_ANTHROPIC_BASE_URL`), or with `provider = "claude-code"` through the local
  `claude` CLI on your subscription. With subscription answerers too, a laptop needs no API key.
- `[budget]`: a daily cap on API spend
- `[schedule]`, `[tiers]`: when sets run (weekly is usually enough)
- Prompt sets: one file per category of buyer question; every prompt has a permanent id
  ([format](examples/popcorn-config/prompt-sets/README.md))

Check a change with `popcorn check`, and see what it does to trends with `popcorn plan`.

## CLI

```
popcorn serve [-addr 127.0.0.1:3000]   web UI, JSON API, MCP and the schedule
popcorn sets                           list prompt sets
popcorn run <set-id>                   run one set (costs money)
popcorn report <run-id> [-json]        rates, share of voice and top pages
popcorn fixes <run-id> [-rebuild]      the fix list as markdown
popcorn check                          validate the config
popcorn plan <before> [<after>]        what a config change does to trends
popcorn migrate-config [dir]           give prompts ids (sets from before ids)
popcorn version                        version and database schema
```

## API and MCP

- JSON API under `/api/` (status, sets, runs, fixes, quick wins, fix attempts, rankings, citations, search probe):
  [reference](https://popcorn.zone/docs/reference/).
- MCP server at `/api/mcp` (Streamable HTTP):

  ```bash
  claude mcp add --transport http popcorn https://<host>/api/mcp --header "Authorization: Bearer <token>"
  ```

## Access

- Unset `POPCORN_TOKENS`: listens on 127.0.0.1 only, open.
- `POPCORN_TOKENS`: bearer tokens for the API and MCP; the UI asks for one once.
- `POPCORN_AUTH_HEADER` (+ `POPCORN_ALLOWED_EMAILS`): trust a signing-in proxy's email header.
- `POPCORN_HOSTS`: host names a proxy on the same machine serves Popcorn as; MCP needs them.

## Deploying

- [docs/deploying.md](docs/deploying.md): Docker, Kubernetes ([Helm chart](charts/popcorn)), releases
- [docs/exe-dev.md](docs/exe-dev.md): an exe.dev VM behind its email sign-in
- One always-on instance per database. Runs resume after a restart.
- Upgrades back up the database first and keep your history.

## Development

```bash
go test -race ./...
go vet ./...
deploy/kind-smoke.sh    # the Helm chart on a local kind cluster
```

Tests use fake providers and never make a paid call. Data outlives releases, so:

- Migrations are append-only; `internal/app/upgrade_test.go` upgrades the oldest schema.
- Stored JSON decodes leniently; old shapes keep working.
- `run.Method` changes only when grading changes; other changes are annotated, not new trends.
- Unknown config keys are errors; format changes ship with `migrate-config`.

Icons: [Lucide](https://lucide.dev) (ISC). License: [MIT](LICENSE).
