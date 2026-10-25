# Site audit, citation check and AI crawlers

Audit your sites, check cited pages or import crawler logs without model calls. Plain HTTP and
log processing have no provider charge. Optional Exa contents fallback can charge for shells and
bot challenges; successful page text is cached for 24 hours and marked `via: exa`. Failed or empty
reads can be retried. [Setup, pricing and limits](optional-providers.md#reading-a-page-through-exa-exa).

## Site audit

```bash
cheapseats audit                 # every site in [[brand.sites]] (and brand.domains)
cheapseats audit example.com     # just one, which needn't be yours (not kept)
cheapseats audit -pages 20 -info # more sitemap pages; list info findings too
```

Use **Site audit** (`/audit`, "Audit now"), `POST /api/audit`, `GET /api/audit` (latest) or MCP
`audit_sites` for your brand's sites. Auditing another site requires the CLI and is not saved.

Every fetch, here and in the citation check, goes to public addresses only: loopback, private,
link-local (cloud metadata included), CGNAT, ULA and multicast addresses are refused, and so is
every other range in IANA's special-purpose registries that is not globally reachable
(documentation, benchmarking, 6to4, NAT64, Teredo, site-local and the like), checked on
the address dialed, so a redirect or a DNS name pointing at one is refused too. At most 5
redirects, http and https only, never through a proxy.

Each site is checked through robots.txt, its home page, `/llms.txt`, sitemap and up to 10 spread-out
sitemap pages. Fetches use `Mozilla/5.0 (compatible; CheapSeats-Audit/<version>; +https://cheapseats.dev)`,
four concurrent requests per check, 16 across the process, a 15-second timeout and a 2 MiB body
cap. Checks inspect raw HTML without executing JavaScript.

Host/path sites (`github.com/example`) get robots rules for that path, but no host-level llms.txt
or sitemap check. Sites of kind `code` or `community` raise no robots, firewall, llms.txt, sitemap
or markup findings; their `blocked` line still records crawler blocks.

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

Third-party pages are fetched most-cited first, then most-returned in searches, where robots.txt
allows `CheapSeats-Audit`. HTTP readings are cached per URL and UTC day for 30 days; matching uses
at most 200,000 bytes of visible text. Whole-word, case-insensitive matches find the brand or
aliases and the set's competitors. A page naming competitors but not the brand is **outreach**.
This is a page-content lead, not proof the assistant read the full page or an outreach success
prediction. A short alias does not match inside another word; a common-word name can still match
unrelated prose.

An HTML shell has under 200 characters of visible text and a `<script>` or `<noscript>`. Without
fallback, its remaining text is matched; a bot challenge is skipped. With Exa fallback enabled,
successful alternative reads carry `via: exa` (`(via Exa)` in the CLI, a chip in the UI).

### Cited pages that changed

**Sources** compares the run's citation check with the newest earlier checked run of the same set.
Pages read by both checks get chips for changed matches: **now names you**, **stopped naming you**,
**now names _competitor_**, **stopped naming _competitor_**. Unreadable, unshared and unchanged pages
are excluded. This compares stored readings; it fetches nothing and does not establish when the
page changed or whether a fix caused it.

The same flags are on `GET /api/runs/<id>` as `cited_page_changes` (`url`, `now_names_brand`,
`stopped_naming_brand`, `now_names`, `stopped_naming`, and `since_run`, the run compared with) and
in the MCP `get_run` digest, the first ten. A run with no check, or none before it, has neither.

### Did the fix land

A fix marked with an attempt (proposed or accepted) whose target is on one of the brand's sites
shows **On the page now** under its **Done when**, on the run page and on Quick wins: whether the
target page names the brand and has each of the fix's claim terms, by the same whole-word match.
The claim terms are its quoted phrases and numbers (any word with a digit: `$49`, `99.9%`,
`30-day`); a claim with neither uses non-common words of at least four letters. Terms are lowercased,
deduplicated and capped at 12. **landed**
means it names the brand and has every term; **not landed** lists the missing terms; a page that
couldn't be read (robots.txt, an error, not text) says **could not read it** and why, and nothing
else.

The page uses the citation fetcher's robots and public-address rules, 200,000-byte text cap and
UTC-day cache. Showing the fix reads the newest cached
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
- **visit**: any other person: a user agent that starts `Mozilla/` and carries none of the words
  tools and bots send (`bot`, `crawl`, `spider`, `http`, `compatible;`, `headless`, `curl` and the
  like), asking for a page (a path with no extension, or `.html`, `.php` and the other page types).
  Redirects other than 304 are left out: the page they lead to counts when the browser gets there.

**Visits** are the AI referrals plus these: every person's page view, so `AI referrals of N visits`
says what share of the people reading came from an assistant. It's a count from logs, not
analytics: no sessions, unique visitors, time on page or trends, and a bot dressed as a browser
counts as a visit. Visits show only once an import has counted some; counts from an import by an
earlier release had none, and importing the same logs again adds them without counting anything
else twice. Visit counts can be private where crawler counts aren't: a copy of the database you
publish can drop them with `DELETE FROM crawl_daily WHERE class = 'visit'`.

**Unverified (UA only):** anyone can send a crawler's or a browser's user agent, and nothing here
checks the IP against the operator's published ranges.

Stored data is daily counts per host, page, class and bot, plus request hashes for deduplication;
raw log lines are not kept. Counts and hashes expire after 120 days, enough for the 90-day view;
older lines and dates after tomorrow (UTC) are ignored. All 4xx requests share the host's `/(errors)` row, which is never
flagged; 5xx requests stay on their page. Each import accepts at most a million new requests;
repeat it to process the remainder. Lines over 1 MiB are marked unreadable without stopping import.

The page lists each page's fetches by class, AI referrals and visits beside how often the latest finished
run of each set cited and surfaced it, and flags:

- **crawled, never cited**: AI crawlers fetched it, but no answer in the latest runs cited it.
- **cited, never crawled by search bots**: answers cite it, but no assistant or AI search crawler
  fetched it in the window (only for hosts the logs cover).
