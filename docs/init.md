# Starting a config repo: cheapseats init

```bash
cheapseats init https://yoursite.com                  # writes ./<brand>-cheapseats-config
cheapseats init yoursite.com ../acme-config -dry-run  # prints what it found and would write
cheapseats init yoursite.com -name "Acme" -competitors "Asana,Trello" -suggest
```

| flag | |
| --- | --- |
| `-name X` | the brand's name, instead of what the site calls itself |
| `-competitors a,b` | competitors, comma-separated: `[[competitors]]` entries, alternatives prompts and head-to-heads |
| `-suggest [-n 10]` | add suggested prompts: one analyst call through the `claude` CLI, using your subscription |
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
`audience` before the first run; changing them restarts every trend.

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
