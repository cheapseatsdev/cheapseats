# Site audit, citation check and AI crawlers

Three checks beside the runs. None of them calls a model: they're plain HTTP and your own logs, so
they cost nothing.

## Site audit

```bash
cheapseats audit                 # every site in [[brand.sites]] (and brand.domains)
cheapseats audit example.com     # just one, which needn't be yours (not kept)
cheapseats audit -pages 20 -info # more sitemap pages; list info findings too
```

Also **Site audit** in the web UI (`/audit`, "Audit now"), `POST /api/audit` and `GET /api/audit`
(the latest), and the MCP tool `audit_sites`. The web UI, API and MCP audit only the brand's own
sites; another site takes the CLI.

Every fetch, here and in the citation check, goes to public addresses only: loopback, private,
link-local (cloud metadata included), CGNAT, ULA and multicast addresses are refused, checked on
the address dialed, so a redirect or a DNS name pointing at one is refused too. At most 5
redirects, http and https only, never through a proxy.

For each site it fetches robots.txt, the home page, `/llms.txt`, the sitemap and up to 10 pages
from the sitemap (spread through it), as `Mozilla/5.0 (compatible; CheapSeats-Audit/<version>;
+https://cheapseats.dev)`, 4 requests at a time, 15 s timeout, 2 MB cap. It reads the raw HTML, as
a crawler that runs no JavaScript does. A host/path site (`github.com/example`) gets its
robots.txt rules checked for that path, but no llms.txt or sitemap check, since those belong to the
host. Sites of kind `code` or `community` don't get their markup checked: it isn't yours to fix.

The newest audit of the brand's own sites is kept. If it's less than 30 days old, its errors and
warnings go to the analyst with the next fix list as more evidence, so a fix can say "this page is
empty without JavaScript" rather than only "this page wasn't cited".

| Check | Severity | What it means |
|---|---|---|
| `robots.blocks-ai-search` | error | robots.txt blocks an assistant's search or user fetcher (OAI-SearchBot, ChatGPT-User, Claude-SearchBot, Claude-User, PerplexityBot, Perplexity-User) from the site |
| `robots.blocks-search-engine` | error | robots.txt blocks Googlebot or Bingbot, whose indexes Gemini, ChatGPT and Copilot search |
| `robots.blocks-training` | info | robots.txt blocks a training crawler (GPTBot, ClaudeBot, anthropic-ai, Google-Extended, Applebot-Extended, CCBot, Meta-ExternalAgent, Bytespider). A choice, not a problem |
| `robots.server-error` | error | robots.txt answers 5xx, which crawlers read as "fetch nothing" |
| `robots.unreachable` | warning | robots.txt couldn't be fetched at all |
| `robots.missing` | info | no robots.txt: everything is allowed |
| `waf.blocks-ai-bot` | error | the home page answers 401, 403 or 429, or a challenge page, to an AI fetcher's user agent while a browser's gets 200 |
| `llms.missing` | warning | no `/llms.txt` (or a catch-all route serving HTML there) |
| `llms.content-type` | warning | llms.txt isn't served as text/plain or text/markdown |
| `llms.heading` | warning | llms.txt doesn't start with `# ` |
| `llms.no-links` | warning | llms.txt has no markdown links |
| `sitemap.missing` | warning | no sitemap in robots.txt and none at `/sitemap.xml` |
| `sitemap.invalid` | error | the sitemap doesn't parse |
| `sitemap.empty` | warning | it lists no pages |
| `sitemap.stale` | warning | its newest `<lastmod>` is over 180 days old |
| `sitemap.no-lastmod` | info | it gives no `<lastmod>` dates |
| `page.unreachable`, `page.status` | error | a page couldn't be fetched, or didn't answer 200 |
| `page.challenge` | error | a page served a bot challenge instead of its content |
| `page.needs-js` | error | under 200 characters of text in the HTML and an app mount point (`#root`, `#app`, `#__next`...): empty without JavaScript |
| `page.thin` | warning | under 200 characters of text in the HTML |
| `page.noindex` | error | a robots meta tag or `X-Robots-Tag` says noindex |
| `page.jsonld-invalid` | error | a JSON-LD block doesn't parse |
| `page.jsonld-missing` | warning | no JSON-LD (the page table lists the `@type`s found) |
| `page.title-missing`, `page.title-long` | warning | no `<title>`, or one over 60 characters |
| `page.description-missing`, `page.description-length` | warning | no meta description, or one outside 50-160 characters |
| `page.h1` | warning | not exactly one `<h1>` |
| `page.lang` | warning | no `lang` on `<html>` |
| `page.canonical` | warning | no canonical link |
| `page.open-graph` | warning | no `og:title` or `og:description` |

The firewall check only sends each bot's user agent, from Cheap Seats' address. A firewall that lets
verified bots through by IP may block this check and still admit the real crawler: confirm in its
log (or with the crawler counts below) before changing rules.

## Citation check

```bash
cheapseats check-citations <run-id>         # the 25 most-cited third-party pages
cheapseats check-citations -n 50 <run-id>
```

Also on the **Site audit** page ("Check citations", for any finished run),
`POST`/`GET /api/runs/<id>/citation-check`, and the MCP tool `check_citations`.

It takes the run's third-party pages, most cited first, then most surfaced in searches, and fetches
each one as Cheap Seats (where its robots.txt allows `CheapSeats-Audit`; once per page per day, kept a
month). It reads the page's visible text and says whether it names the brand or an alias, and which
of the set's competitors. A page that names competitors but not you is marked **outreach**: the
answers read it, and it leaves you out. Matching is by whole word, ignoring case: stricter than the
grader's own check, so a short alias doesn't match inside another word.

## AI crawlers

```bash
cheapseats crawl import access.log access.log.1.gz        # nginx, Apache: pass -host for the site
cheapseats crawl import -host docs.example.com docs.log
cheapseats crawl import cloudflare-*.json caddy.json -    # - reads stdin
cheapseats crawl report -days 30
```

Also **AI crawlers** in the web UI (`/crawlers`), `GET /api/crawlers?days=30`, and the MCP tool
`get_crawlers`.

It reads common and combined log format (nginx and Apache defaults, with or without a leading
virtual host), Caddy's JSON access log, and Cloudflare Logpush JSON (`ClientRequestUserAgent`,
`ClientRequestURI`, `ClientRequestHost`, `EdgeStartTimestamp`, `ClientIP`, `EdgeResponseStatus`,
`ClientRequestReferer`). Lines with no host (common and combined) count under `-host`, or the
brand's first site. Requests for assets (CSS, scripts, images, fonts) are skipped.

Each request is classed by its user agent:

- **assistant**: fetched because someone asked (ChatGPT-User, Claude-User, Perplexity-User, MistralAI-User)
- **AI search**: an assistant's search index (OAI-SearchBot, Claude-SearchBot, PerplexityBot, DuckAssistBot)
- **training**: GPTBot, ClaudeBot, anthropic-ai, CCBot, Meta-ExternalAgent, Bytespider, Amazonbot, cohere-ai
- **engine**: Googlebot, Bingbot
- **AI referral**: a person, with a Referer from chatgpt.com, chat.openai.com, claude.ai,
  perplexity.ai, copilot.microsoft.com or gemini.google.com, or `utm_source=chatgpt.com` (or another of those hosts)

**Unverified (UA only):** anyone can send a crawler's user agent, and nothing here checks the IP
against the operator's published ranges.

Only daily counts per host, page, class and bot are kept, not log lines, plus a hash of each
counted request so importing the same log, or one that overlaps it, counts nothing twice. Hashes
are kept 400 days, and lines older than that aren't counted. A line over 1 MB counts as unreadable
and the import carries on.

The page lists each page's fetches by class and AI referrals beside how often the latest finished
run of each set cited and surfaced it, and flags:

- **crawled, never cited**: AI crawlers fetched it, but no answer in the latest runs cited it.
- **cited, never crawled by search bots**: answers cite it, but no assistant or AI search crawler
  fetched it in the window (only for hosts the logs cover).
