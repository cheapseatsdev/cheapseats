# Cheap Seats

Cheap Seats is an open-source (MIT) AI visibility tracker I wrote to make my own product better. It
asks your buyers' questions of AI assistants, grades whether they recommend you, and builds a ranked
list of fixes, running on the Claude subscription you already have (ChatGPT optional).

It draws on a lot of inspiration, [Gauge](https://withgauge.com) first among them, along with
Profound and Peec AI. It's free, and always will be: no hosted tier, no upsell. I hope you find it
useful. If you outgrow it, do whatever suits you; I'd suggest Gauge, that team is great. Your prompts
are plain text in your own repo, so they come with you. Site and docs: [cheapseats.dev](https://cheapseats.dev/docs/).

Cheap Seats was called Popcorn until October 2026, renamed so it isn't confused with an unrelated
paid product. Upgrading from Popcorn needs no changes: see
[Upgrading from Popcorn](docs/deploying.md#upgrading-from-popcorn).

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
  the strengths it credits you with, its tone about you (positive, neutral, mixed or negative), the
  sentence that backs the verdict (checked to be in the answer, and highlighted there), and every
  search result and citation, with each cited page's type (listicle, comparison, review, docs,
  forum, repo, article, product).
- Sums up what the answers say about you: **net sentiment** (the share of answers naming you that are
  positive, minus the share negative), the praise and objections heard most, and a few named
  **themes** with the competitor each one helps and a quote, from one cheap grader call a run.
- Ends each run with **Fix these first**: the exact page to create or change, what it must say, the
  prompts it should flip, effort, priority, the site it belongs on and its owner, plus a prompt to
  hand Claude Code.
- **Quick wins** ranks fixes across all sets by expected gain: prompts flipped per day of work,
  each weighted by what it has left to win. Mark a fix with the commit or PR you submitted
  (proposed, then accepted) and it leaves the open list. If it wasn't enough, the next run's fix
  for the same page comes back open, with the attempt and what changed for its prompts (moved,
  worse, or no detectable change, tested against the noise of a few answers a prompt), and the
  fix list is written knowing what was tried.
- **Rankings** trend each set week to week, through prompt and model changes: 7-, 14- or 28-day
  averages with 95% bands, day-over-day changes greyed out when they're within noise, and fixes
  marked on the chart. Each set also gets a **citation stability** score: how much the hosts its
  answers cite hold their share from run to run. How it's worked out:
  [docs/statistics.md](docs/statistics.md). After a grading change, [re-grade](docs/regrading.md)
  the answers you already have to carry the trend on (free on the claude CLI).
- Free checks beside the runs, no model calls ([docs](docs/audit.md)): a **site audit** of what AI
  crawlers can read (robots.txt and firewall blocks per AI bot, llms.txt, sitemap, and pages that
  are empty without JavaScript), a **citation check** of which pages the answers cite name you and
  which name only competitors, and **AI crawler** counts from your access logs.
- Web UI, CLI, JSON API and MCP server.
- [Share a run](docs/sharing.md) with someone without a sign-in: a read-only link to its
  headline rates, share of voice, strongest and weakest prompts and top fixes, revocable, expiring.
- Self-hosted, MIT licensed: Docker, Kubernetes (Helm chart) or an exe.dev VM. Your prompts live
  in a git repo you own.


## What a run gives you

A run's scores per track, and every prompt against every assistant:

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/run-dark.webp">
  <img alt="A run page: search and recall scores, per-answerer rates, and a prompt-by-answerer grid of verdicts" src="docs/images/run-light.webp">
</picture>

Below them: share of voice (named rate, true share, position), head to head against each
competitor, the objections answers raise, and where the answers' sources come from, with the
third-party hosts cited without you. [Reading a run](docs/reading-a-run.md) defines every number.

Quick wins: every fix across your sets, filterable by site, gap and set:

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/quickwins-dark.webp">
  <img alt="Quick wins: ranked fixes with priority, gap, effort and the site each belongs on" src="docs/images/quickwins-light.webp">
</picture>

[What the assistants searched](docs/searches.md): the queries behind each answer, the names they
added that the prompt didn't have, and the searches where competitors show up and you don't.

## Quick start

On your subscriptions, with the `claude` and `codex` CLIs installed and logged in: no API key, no
API spend.

```bash
CHEAPSEATS_CONFIG=examples/cheapseats-config/cheapseats.subscriptions.toml go run ./cmd/cheapseats serve
```

Or through the APIs:

```bash
cp .env.example .env          # set ANTHROPIC_API_KEY
go run ./cmd/cheapseats serve    # the example config, at http://127.0.0.1:3000
```

To measure your own brand, start a config repo from its website:

```bash
go run ./cmd/cheapseats init https://yoursite.com     # -dry-run to see it first
```

That reads the home page, `/llms.txt` and the sitemap for the brand's name, description, docs,
blog and GitHub, and writes `./<brand>-cheapseats-config` with cheapseats.toml (on your Claude
subscription) and starter prompt sets, marking with TODOs what you must decide
([details](docs/init.md)). Or start from the
[template](https://github.com/cheapseatsdev/cheapseats-config-template). Then:

```bash
CHEAPSEATS_CONFIG=../your-config/cheapseats.toml go run ./cmd/cheapseats check
CHEAPSEATS_CONFIG=../your-config/cheapseats.toml go run ./cmd/cheapseats serve
```

## Configuration

A config repo holds `cheapseats.toml` and `prompt-sets/*.toml`.
[`examples/cheapseats-config`](examples/cheapseats-config) is a complete one, used when `CHEAPSEATS_CONFIG`
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
  adds it in `CHEAPSEATS_ANTHROPIC_BASE_URL`), or with `provider = "claude-code"` through the local
  `claude` CLI on your subscription. With subscription answerers too, a laptop needs no API key.
  The theme pass is one extra grader call per run (about $0.003 on Haiku, nothing on `claude-code`);
  `[grader] themes = false` turns it off.
- `[[competitors]]`: names (and aliases) the grader folds onto one, shared by every set, and their
  `domains`, so their pages count as theirs
- `[sources]`: hosts to move between the kinds of source the run page sorts pages into
  ([reading a run](docs/reading-a-run.md#configuring-it))
- `[sampling]`: `adaptive = true` and `max_extra = N` let a run ask up to N extra answers, decided
  before it starts: the prompts whose named rate has been least certain over the last 28 days are
  asked twice over, every answerer and track alike, within today's budget. Off by default; it never
  starts a trend over, and extras that fail are dropped rather than holding the run back. A prompt's
  rate means the same with or without them; totals pooled over all of a run's answers weigh a
  doubled prompt twice.
- `[budget]`: a daily cap on API spend
- `[schedule]`, `[tiers]`: when sets run (weekly is usually enough)
- Prompt sets: one file per category of buyer question; every prompt has a permanent id
  ([format](examples/cheapseats-config/prompt-sets/README.md))

Check a change with `cheapseats check`, and see what it does to trends with `cheapseats plan`.

## CLI

```
cheapseats init <url> [dir]               a new config repo from the brand's website (free;
                                       -dry-run, -name, -competitors a,b, -suggest)
cheapseats serve [-addr 127.0.0.1:3000]   web UI, JSON API, MCP and the schedule
cheapseats sets                           list prompt sets
cheapseats run <set-id>                   run one set (costs money)
cheapseats report <run-id> [-json]        rates, share of voice and top pages
cheapseats fixes <run-id> [-rebuild]      the fix list as markdown
cheapseats audit [site...]                what AI crawlers can read on your sites (free)
cheapseats check-citations <run-id>       which cited pages name you, and which competitors
cheapseats crawl import <logfile>...      AI crawler fetches and AI referrals from access logs
cheapseats crawl report [-days 30]        those counts per page, beside how often runs cite it
cheapseats themes <run-id> [-rebuild]     net sentiment and the run's themes
cheapseats suggest-prompts <set-id> [-n 10]  new buyer questions as TOML to paste (one analyst call)
cheapseats regrade <set-id|run-id> [-since 28d] [-dry-run=false]
                                       grade stored answers again with today's grader, brand and
                                       competitors; a dry run with its cost unless -dry-run=false
cheapseats check                          validate the config
cheapseats plan <before> [<after>]        what a config change does to trends
cheapseats migrate-config [dir]           give prompts ids (sets from before ids)
cheapseats version                        version and database schema
```

## API and MCP

- JSON API under `/api/` (status, sets, runs, fixes, quick wins, fix attempts, rankings, citations, searches, search probe, site audit, citation check, crawlers):
  [reference](https://cheapseats.dev/docs/reference/).
- MCP server at `/api/mcp` (Streamable HTTP). `get_run` includes net sentiment, themes and page
  types; `get_result` the grade's evidence quote; `get_citation_stability` has a set's run-to-run
  citation stability; `suggest_prompts` drafts new prompts for a set (one analyst call); `regrade`
  re-grades stored answers ([re-grading](docs/regrading.md)), a dry run unless `dry_run` is false:

  ```bash
  claude mcp add --transport http cheapseats https://<host>/api/mcp --header "Authorization: Bearer <token>"
  ```

  - `get_started` first: the brand and its sites, each set's last run and status, today's spend
    against the budget, which answerers and the grader can run here, the latest audit's errors
    and warnings, open and urgent fixes, attempts awaiting an outcome, `data_state` (`no_runs`,
    `running`, `stale` or `ready`) and `next_steps`, the tool calls worth making now.
  - `start_run` with `dry_run: true` plans a run without starting it: answers per answerer and
    track, answerers skipped, estimated cost from each answerer's recent answers and their grading
    (priced by `[pricing]`; re-grades count toward grading, so it errs high) and
    today's remaining budget. `POST /api/runs` takes `"dry_run": true` too.
  - `share_run` makes a [share link](docs/sharing.md) for a run, or a set's newest finished run.
  - Prompts: `weekly_pulse`, `competitor_radar` and `why_not_recommended` (for one prompt), each
    telling the model which tools to call.

## Access

- Unset `CHEAPSEATS_TOKENS`: listens on 127.0.0.1 only, open.
- `CHEAPSEATS_TOKENS`: bearer tokens for the API and MCP; the UI asks for one once.
- `CHEAPSEATS_AUTH_HEADER` (+ `CHEAPSEATS_ALLOWED_EMAILS`): trust a signing-in proxy's email header.
- `CHEAPSEATS_HOSTS`: host names a proxy on the same machine serves Cheap Seats as; MCP needs them.
- `CHEAPSEATS_PUBLIC_URL`: the address share links use (`https://cheapseats.example.com`). Unset, they
  use the first of `CHEAPSEATS_HOSTS` over https, else the host the request came in on
  (`X-Forwarded-Host` first).
- `/share/<token>` is open without a sign-in; it serves only that link's run ([docs](docs/sharing.md)).

## Deploying

- [docs/deploying.md](docs/deploying.md): Docker, Kubernetes ([Helm chart](charts/cheapseats)), releases
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
  `cheapseats regrade` brings older runs onto a new method without asking again ([how](docs/regrading.md)).
- Unknown config keys are errors; format changes ship with `migrate-config`.

Icons: [Lucide](https://lucide.dev) (ISC). License: [MIT](LICENSE).
