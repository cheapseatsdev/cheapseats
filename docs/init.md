# Starting a config repo: popcorn init

```bash
popcorn init https://yoursite.com                  # writes ./<brand>-popcorn-config
popcorn init yoursite.com ../acme-config -dry-run  # prints what it found and would write
popcorn init yoursite.com -name "Acme" -competitors "Asana,Trello" -suggest
```

| flag | |
| --- | --- |
| `-name X` | the brand's name, instead of what the site calls itself |
| `-competitors a,b` | competitors, comma-separated: `[[competitors]]` entries, alternatives prompts and head-to-heads |
| `-suggest [-n 10]` | add suggested prompts to the starter sets: one analyst call on the claude CLI, so nothing on a subscription |
| `-dry-run` | print what it found and every file it would write; write nothing |

It refuses to overwrite a file that exists, and writes nothing if one does.

## What it reads

Three requests, through the same guarded fetcher as the [site audit](audit.md) (public addresses
only): the home page, `/llms.txt` and `/sitemap.xml` (an index is followed to a few of its
sitemaps). No model is called unless you pass `-suggest`.

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
<brand>-popcorn-config/
  popcorn.toml                   brand, sites, competitors; claude-code answerer, grader and
                                 analyst (codex commented out); budget; schedule off
  prompt-sets/
    discovery.toml      core     "What's the best <category> for a small team?" ...
    use-cases.toml      plg      the jobs buyers hire it for (a TODO: use theirs)
    alternatives.toml   plg      "What are good alternatives to <competitor>?" ...
    pricing.toml        plg      cost and value
    head-to-heads.toml  plg      "<brand> vs <competitor>: which should I pick?": the only set
                                 that names the brand
```

The generated files pass `popcorn check` before they're written. Review every TODO
(`grep -rn TODO`): `audience` and `facts` are left for you, and `about` and the category are
guesses. Settle `about` and `audience` before the first run, since changing them restarts every
trend. Then `popcorn check`, `popcorn audit`, `popcorn run discovery`, and commit the directory as
its own git repo.
