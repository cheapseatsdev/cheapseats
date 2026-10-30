# Starting a config repo: cheapseats init

`init` starts a config for the **Go/subscription route**. Its answerer, grader and analyst use
the local `claude` CLI, so running the generated config requires that CLI to be installed and
logged in to a Claude subscription. A `codex` answerer is included but commented out.

**For Docker with API keys,** start from the [config template](https://github.com/cheapseatsdev/cheapseats-config-template)
and follow the [Docker quick start](deploying.md#try-docker-with-api-keys), which needs no Go.
The image contains neither subscription CLI; an `init` config's providers must be changed
before it can run there.

From an app checkout with Go 1.27 or later:

```bash
go run ./cmd/cheapseats init https://yoursite.com ../your-config
```

With a built `cheapseats` binary, the same command and its options are:

```bash
cheapseats init https://yoursite.com                  # writes ./<brand>-cheapseats-config
cheapseats init yoursite.com ../acme-config -dry-run  # prints what it found and would write
cheapseats init yoursite.com -name "Acme" -competitors "Asana,Trello" -suggest
```

| flag | |
| --- | --- |
| `-name X` | the brand's name, instead of what the site calls itself |
| `-competitors a,b` | competitors, comma-separated: `[[competitors]]` entries, alternatives prompts and head-to-heads |
| `-suggest [-n 10]` | request decision-backed suggestions with at most one analyst call; with no buyer/advisory context it returns none without a call |
| `-dry-run` | print what it found and every file it would write; write nothing |

If any target file exists, it writes nothing.

## What it reads

The home page, `/llms.txt` and `/sitemap.xml`, following a few child sitemaps for an index. Fetches
use the [site audit](audit.md)'s public-address guard. No model is called without `-suggest`.

- **Name**, the first of: `og:site_name`, `application-name`, the `name` of a JSON-LD
  Organization or SoftwareApplication, the part of `<title>` that spells the domain (else the
  shortest part that looks like a name, split on ` | – — · : -`), llms.txt's heading, the domain.
- **About**: the meta description, else `og:description`, the JSON-LD description, llms.txt's
  summary.
- **Aliases**: the domain, and the name without spaces.
- **Sites**: the host (marketing); `docs.`, `developers.`, `help.`, `blog.` and `community.`
  subdomains and `/docs`, `/blog`, `/community` paths linked from the home page, llms.txt or the
  sitemap; docs on readthedocs.io or gitbook.io named after the brand; up to three GitHub
  repositories (or owners) named after the brand or its domain, as `code`.
- **Category**: what buyers would call the product, from the description: the first product noun
  ("platform", "app", "database", "alternative"...) with up to three words before it, introduced
  as the brand ("is an", "the") rather than something it does or uses ("with AI agents"). With
  none, the prompts say "tool" and a TODO says to replace it.

## What it writes

```
<brand>-cheapseats-config/
  .gitignore                        .env and .env.* (keys may live there), not .env.example
  cheapseats.toml                   brand, sites, competitors; claude-code answerer, grader and
                                 analyst (codex commented out); budget; schedule off
  prompt-sets/
    discovery.toml      core     "What's the best <category> for a small team?" ...
    use-cases.toml      plg      the jobs buyers hire it for (a TODO: use theirs)
    alternatives.toml   plg      "What are good alternatives to <competitor>?" ...
    pricing.toml        plg      cost and value
    head-to-heads.toml  plg      "<brand> vs <competitor>: which should I pick?": the only set
                                 that names the brand
```

Generated files pass `cheapseats check` before writing. Review every TODO (`grep -rn TODO`): supply
`audience` and public `facts`, and check the guessed `about` and category. Settle `about` and
`audience` before the first run; changing them restarts every trend. Fill the advisory positioning, goals and
accepted boundaries separately: they guide prompt curation and advice, not answer generation or scoring.
The draft questions are examples to challenge, not demand evidence or a required coverage quota;
remove questions that serve an excluded market or no unresolved decision.

Point subsequent commands at the generated config:

```bash
export CHEAPSEATS_CONFIG=./your-config/cheapseats.toml  # use the directory init wrote
cheapseats check
cheapseats audit
cheapseats run discovery
```

`run` asks real questions; review the prompts first. Commit the directory as its own repo using
the command `init` prints. It stages only generated files, keeping a new `.env` out even before
`.gitignore` is committed.

## Reviewing suggested questions

With `-suggest`, missing audience and advisory context defer generation without a model call;
the discovery file records why. Those values are TODOs in a fresh init config. Fill them in, then
use `suggest-prompts` against the configured repo. A homepage category alone does not establish a
target buyer or business decision. Do not infer validated demand from generated wording.
Suggestions may be fewer than requested or empty. Each retained suggestion must identify its buyer,
learning, concrete decision/action and reason for repeating it, including when to review or stop.
Missing rationale is rejected. Stages organize suggestions without requiring broad funnel coverage.
Review those judgments before committing or running the files; the schedule remains disabled.
A dry run writes nothing. Generation uses the subscription allowance when enough context exists;
it is not a guarantee of valid buyer demand or a paid-answer run.
