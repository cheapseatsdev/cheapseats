# Site audit, citation check and AI crawlers

Three checks beside the runs. None of them calls a model: they're plain HTTP and your own logs, so
they cost nothing. The one exception is optional: with `[exa]` configured and `contents_fallback`
on, a page that is empty without JavaScript, or served as a bot challenge, is read through Exa's
contents API instead, a tenth of a cent a page, at most once a day, and marked `via: exa`
([optional providers](optional-providers.md)).

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
link-local (cloud metadata included), CGNAT, ULA and multicast addresses are refused, and so is
every other range in IANA's special-purpose registries that is not globally reachable
(documentation, benchmarking, 6to4, NAT64, Teredo, site-local and the like), checked on
the address dialed, so a redirect or a DNS name pointing at one is refused too. At most 5
redirects, http and https only, never through a proxy.

For each site it fetches robots.txt, the home page, `/llms.txt`, the sitemap and up to 10 pages
from the sitemap (spread through it), as `Mozilla/5.0 (compatible; CheapSeats-Audit/<version>;
+https://cheapseats.dev)`, 4 requests at a time (and 16 across every audit and citation check
running at once), 15 s timeout, 2 MB cap. It reads the raw HTML, as a crawler that runs no
JavaScript does. A host/path site (`github.com/example`) gets its robots.txt rules checked for that
path, but no llms.txt or sitemap check, since those belong to the host. Sites of kind `code` or
`community` (your GitHub, a forum) raise no robots.txt, firewall, llms.txt, sitemap or markup
findings: none of that is yours to fix. Which crawlers the host blocks
is still recorded on the site's `blocked` line, so you know it, without a fix you can't apply.

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
| `page.needs-js`, `page.challenge` with `via: exa` | info | with the Exa fallback on, the page was read through Exa and has content; the empty HTML, or the challenge, is still what a crawler that runs no JavaScript gets |
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
cheapseats check-citations -n 50 <run-id>   # at most 100
```

Also on the **Site audit** page ("Check citations", for any finished run),
`POST`/`GET /api/runs/<id>/citation-check`, and the MCP tool `check_citations`.

It takes the run's third-party pages, most cited first, then most surfaced in searches, and fetches
each one as Cheap Seats (where its robots.txt allows `CheapSeats-Audit`; once per page per day, kept a
month). It reads the page's visible text and says whether it names the brand or an alias, and which
of the set's competitors. A page that names competitors but not you is marked **outreach**: the
answers read it, and it leaves you out. Matching is by whole word, ignoring case: stricter than the
grader's own check, so a short alias doesn't match inside another word.

A page that is empty without JavaScript (under 200 characters of text and a `<script>`), or one
that serves a bot challenge, names nobody over plain HTTP. With `[exa]` configured and
`contents_fallback` on it is read through Exa instead, once a day, and the row carries `via: exa`
(`(via Exa)` on the command line, a chip in the web UI). Without it, the row reads as it always
did.

### Cited pages that changed

A run whose citation check can be compared with the check of the newest earlier run of the same
prompt set that has one lists, under **Sources** on the run page, each page both checks read whose
reading changed, with a chip per change: **now names you**, **stopped naming you**, **now names
_competitor_**, **stopped naming _competitor_**. A page either check couldn't read, or only one
check fetched, is never a change; a page that says the same is not listed. The flip is computed
from the two stored checks when the run is shown: nothing more is fetched or stored.

The same flags are on `GET /api/runs/<id>` as `cited_page_changes` (`url`, `now_names_brand`,
`stopped_naming_brand`, `now_names`, `stopped_naming`, and `since_run`, the run compared with) and
in the MCP `get_run` digest, the first ten. A run with no check, or none before it, has neither.

### Did the fix land

A fix marked with an attempt (proposed or accepted) whose target is on one of the brand's sites
shows **On the page now** under its **Done when**, on the run page and on Quick wins: whether the
target page names the brand and has each of the fix's claim terms, by the same whole-word match.
The claim terms are the claim's quoted phrases and numbers (any word with a digit: `$49`, `99.9%`,
`30-day`); a claim with neither uses its words of four letters or more, less common ones. **landed**
means it names the brand and has every term; **not landed** lists the missing terms; a page that
couldn't be read (robots.txt, an error, not text) says **could not read it** and why, and nothing
else.

The page is fetched with the citation check's fetcher, under the same rules: robots.txt, public
addresses only, the 200 KB text cap and the same day cache. Showing the fix reads the newest cached
reading (with its day) and, when there is none from today, fetches the page in the background, so
the next view has it; a page is fetched at most once a day however often the fix is shown. It is
never read through Exa. This is evidence beside the fix's done-when, not a grade: it counts toward
no [outcome](statistics.md#fix-outcomes).

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
counted request so importing the same log, or one that overlaps it, counts nothing twice. Counts
and hashes are kept 400 days, and lines older than that aren't counted; nor are lines dated after
tomorrow (UTC), which a wrong clock or a forged line would be. A request answered 4xx is counted
under one row per host, `/(errors)`, rather than under its path, so a stream of made-up URLs sent
with a crawler's user agent adds no rows of its own; that row is never flagged. A 5xx stays on its
page, so a crawler's server error on a real page still shows there. One
import counts at most a million new requests; importing the same log again counts the rest. A
line over 1 MB counts as unreadable and the import carries on.

The page lists each page's fetches by class and AI referrals beside how often the latest finished
run of each set cited and surfaced it, and flags:

- **crawled, never cited**: AI crawlers fetched it, but no answer in the latest runs cited it.
- **cited, never crawled by search bots**: answers cite it, but no assistant or AI search crawler
  fetched it in the window (only for hosts the logs cover).
