# Optional providers: TypeSafe's Jev and Exa

Cheap Seats runs on Claude (and optionally OpenAI) and nothing else has to be configured. Two
more services can be switched on for jobs the core models do badly or not at all. Both are
optional in the strict sense: with neither table in `cheapseats.toml`, nothing about them is
called, and switching one off later leaves every page as it was.

- **[TypeSafe](https://typesafe.ai)'s Jev** returns typed judgments, not text: a probability that
  a statement holds, a choice among options you define, a score on levels you describe, each with
  a calibrated probability and a confidence. It cannot generate, count or do arithmetic, and it
  reads instructions literally. Input costs $0.042 per million tokens; output is free.
- **[Exa](https://exa.ai)** is a web search API: a neutral index to ask what the web has, as
  opposed to what an assistant found. A search is about $4 per thousand; page contents cost extra,
  about $1 per thousand pages, and are fetched only as a fallback for a page plain HTTP can't read.

## Getting a key

- **Exa**: sign up at [dashboard.exa.ai](https://dashboard.exa.ai) and make a key under API keys.
  As of October 2026 the free plan gives $10 of credits every month, plus a $10 onboarding bonus,
  with no payment method. At $4 per thousand searches that is 2,500 searches a month; the coverage
  check uses one search per prompt per run, so five sets of 15 to 20 prompts run weekly is about
  350 searches a month, well inside the free plan. Most people will never pay for this.
- **TypeSafe**: make a key in the [console](https://console.typesafe.ai/keys). As of October 2026
  the free tier is $5 of usage a month. Input tokens are $0.042 per million, so that is about 119
  million tokens; a run's whole folding pass is a few thousand, so the free tier covers this use
  many thousands of times over.

On exe.dev, add each as an integration instead and point `CHEAPSEATS_TYPESAFE_BASE_URL` and
`CHEAPSEATS_EXA_BASE_URL` at it; the VM then holds no key.

## The shape that keeps them optional

Every use of either provider follows the same rules, and anything added later should too.

1. **A table each.** `[typesafe]` and `[exa]`; absent means off. Each feature in a table has a
   switch that defaults to on when the table is present.
2. **A key, or a key-adding proxy.** `TYPESAFE_API_KEY` and `EXA_API_KEY` from the environment,
   or `CHEAPSEATS_TYPESAFE_BASE_URL` and `CHEAPSEATS_EXA_BASE_URL` pointing at a proxy that adds
   the key (exe.dev's integrations), exactly as `CHEAPSEATS_ANTHROPIC_BASE_URL` works. Locally
   that's the key; on a VM the proxy, so the VM holds nothing. Either satisfies `cheapseats check`
   and the readiness report; a table without either is a note at check time and a skipped pass
   at run time, like an answerer whose key is missing.
3. **Never called from a page.** Each use runs after a run finishes, or on demand, and writes its
   result to SQLite. Report pages, the API and MCP read the tables, so a deployment without the
   providers renders the same pages at the same speed, and a provider outage changes no report.
4. **Priced through the ledger.** Each call reserves and settles against the daily budget under a
   `[pricing]` entry: `[pricing."jev-1.14.0"] input = 0.042`, `[pricing.exa-search]
   search_per_1k = 4`. No price, no call. Exa's own cost estimate is used when it returns one.
5. **Degrades to the existing rule.** Each feature supplements a heuristic the code already had,
   and that heuristic stays as the floor.
6. **Decisions carry provenance and a threshold read at use.** A stored decision says who made it
   (`jev`, `manual`) and with what confidence. The confidence a decision needs to take effect is
   read from the config each time, so tightening it never asks anything again.

## How a pass is wired

Each use of a provider is a `Pass` (`internal/app/passes.go`), registered from `init()` in its
own file: a name for the ledger, a CLI command that runs it on one finished run, an order, the
provider it needs, the config switch that turns it on, and the function that does the work and
returns how many items it decided. After a run, `App` runs every pass the config switches on, in
order: those under 100 before the fix list is built, so the list sees their results, and the rest
after. A pass whose provider isn't callable is skipped with a log line. The tables all passes
write live in `internal/store/011_judgments.sql`; `typesafe.Chosen` is the probability to gate
on; `exa.Client` has `Search`, `FindSimilar` and `Contents`, the last priced per page under
`[pricing.exa-contents] page_per_1k`.

## Shipped

### Folding product names (Jev)

The grader writes product names as the answers do, so one product can appear on the share-of-
voice board twice ("Scrunch", "Scrunch AI"). Configured competitor aliases fold the names you
thought of; Jev folds the ones you didn't.

After each run, every name the grader wrote that isn't the brand, a competitor or an alias, and
hasn't been decided before, is put to Jev once: a Choice over the known products it plausibly
matches (shares a meaningful word, or one name contains the other), with a `none` option, given
the category from `brand.about` and the sentence of an answer that named each product. Between two
unconfigured names, only the less established one is asked about, the one in fewer answers or,
at a tie, the shorter, so "Scrunch" is asked about "Scrunch AI" and not the reverse. Each name is
its own request, with only its candidates in the state; a run's pass is a few dozen calls and
under a cent.

Decisions land in `name_decisions`, each with Jev's probability for the option it chose: for a
match, the probability that the two names are one product. `App.Aliases` reads them back, with the
competitors' configured aliases, at `typesafe.min_confidence` (default 0.7), and
`report.Options.Aliases` folds names and top picks on the board. A variant decided `none` is a
product of its own and isn't asked again. `cheapseats fold <run-id>` runs the pass on a finished
run and prints the matches with their probabilities.

Gate on the chosen option's probability, not Jev's `confidence`: that statistic measures how
concentrated the whole distribution is and shrinks with every extra candidate, so on a two-option
question a 0.77 probability reads as 0.54. The first production pass gated on confidence and
folded nothing.

```toml
[typesafe]
model = "jev-1.14.0"     # pin a version: aliases like jev-latest move
# fold_names = true
# min_confidence = 0.7   # Jev's probability that two names are one product

[pricing."jev-1.14.0"]
input = 0.042
```

What the first live calls taught us: given only two bare names, Jev said 0.47 for "Scrunch" and
"Scrunch AI", a shrug. With the category and a sentence of context per name it said 0.77. Given
the instruction that spacing, punctuation, a domain suffix, a company prefix or an "AI" suffix
don't make a different product, it said 0.88 for "Otterly AI" and "Otterly.AI", 0.85 for
"Microsoft Copilot" and "Copilot", 0.78 for "AI Mode" and "Google AI Mode", where the vaguer
wording had given 0.53, 0.67 and 0.58. Give it state, and say what counts; it reads literally. And keep each request's state to the one
question: eight names batched into one request scored the same pairs 0.15 to 0.3 lower than one
name per request (Otterly AI: 0.59 batched, 0.88 alone). The first production pass decided 28 names in four requests for half a cent:
25 were products of their own, three matched (Scrunch to Scrunch AI, Microsoft Copilot to Copilot,
AI Mode to Google AI Mode).

### The coverage check (Exa)

A miss has two very different causes: no page of yours answers the question, or one does and
the assistants never read it. The run alone can't tell them apart. After each run, with `[exa]`
configured, every prompt is searched once on Exa restricted to the brand's sites
(`[[brand.sites]]` and `brand.domains`, hosts or host/path prefixes), and the pages found are
stored per run and prompt in `coverage`. The run page shows them under each prompt with a status
against that prompt's answers: `cited`, `retrieved` (a search returned it, no answer cited it) or
`unseen`. `cheapseats coverage <run-id>` runs the pass on a finished run, searching only prompts
not yet checked. `GET /api/runs/{id}` carries them as `coverage`, and MCP `get_run` as `own_pages` per
prompt. Fifteen prompts is fifteen searches, about six cents.

```toml
[exa]
# coverage = true
# results = 3

[pricing.exa-search]
search_per_1k = 4
```

The first production pass searched all 11 prompts of the head-to-heads set for 4.4 cents and found
no cheapseats.dev page for any of them: a neutral index hadn't picked up the new domain at all.
That is the check doing its job, and the thing to fix is indexing, not writing.

### Competitor discovery (Exa, Jev optional)

Competitors are configured by hand, and the prompt review's gaps only know names the answers
already used. `cheapseats discover` asks Exa what resembles each configured competitor's home
page (its first domain) and the brand's first site, with `excludeSourceDomain` and every
configured domain excluded, `discover_results` (default 10) pages each, and gathers the results
by host: which home pages led there (`seeds`), a name cleaned from the page title, and the best
page on the host. Each host is cross-referenced with the share-of-voice board: a host whose name
matches a product the latest run of any set named, and that isn't configured, carries that count
as `on_board`. With `[typesafe]` on, each new host is put to Jev once, a yes/no over `{url, title,
category}`: is this the site of a product or service that competes in the category? The rows land
in `competitor_suggestions`; running it again refreshes the seeds and board counts, asks Jev about
no host already judged, drops hosts Exa no longer returns, and keeps every dismissal.

It runs on demand only, never after a run. The suggestions are read in order of seeds, then
answers on the board, then Jev's probability, with hosts under `typesafe.min_confidence` hidden
where Jev judged them (and every host listed when it didn't: the floor is "Exa found it similar"),
and printed as `[[competitors]]` entries to paste, each with its reason as a comment:

```toml
[[competitors]]
name = "Acme"
domains = ["acme.com"]   # similar to 2 competitors' sites, named in 7 answers, in the category at 0.91
```

`cheapseats discover -json` prints the same as JSON. `GET /api/competitors/suggestions` reads the
table (`suggestions` and `toml`) and `POST /api/competitors/suggestions/{host}/dismiss` hides a host
for good; neither calls a provider. MCP `suggest_competitors` runs the discovery and returns the
top 10 with reasons and the TOML. Nothing edits the config: a suggestion becomes a competitor by
pull request, like a prompt.

```toml
[exa]
# discover_results = 10

[pricing.exa-search]
search_per_1k = 4
```

Cost per run of the command: (competitors + 1) × $0.004 for Exa, so a brand with five
competitors spends 2.4 cents, plus a fraction of a cent of Jev for the new hosts.

### A second engine for the search probe (Exa)

The search probe (`/search`, `POST /api/search-probe`, MCP `search_probe`) runs one query through
the web search tool the answerers use and shows the links as the model receives them. One engine
can't say whether a result is the assistant's or the web's. With `[exa]` configured, the probe
takes an engine: `claude` (the default, as before), `exa`, which runs the same query on Exa's
neutral index with the same allowed or blocked domains and returns the links in Exa's order under
the model name `exa-search`, or `both`, which runs the two at once and returns each result with
`overlap`, `only_claude` and `only_exa`: the URLs by `report.NormalizeURL`, so a trailing slash or
a fragment doesn't make a different page. The page gets an engine selector, shown only when `[exa]`
is present; the API and MCP take `engine`; `cheapseats probe [-engine exa|claude|both] [-domains
a,b] [-exclude a,b] "<query>"` does the same from the shell. Without `[exa]`, the page is as it
was and `engine: exa` is the error `no [exa] in the config`.

It is the one use of Exa that runs on demand from a page rather than after a run, because the
probe always has: someone asks a question and pays for the answer, and nothing is stored. It has
no switch; it is available when `[exa]` is configured and `exa-search` is priced. An Exa probe asks
for ten results, what Claude's tool returns at most, so the two sides of a comparison are the same
size, and costs under half a cent; `both` costs that plus the Claude probe's few cents.

```toml
[exa]

[pricing.exa-search]
search_per_1k = 4
```

### Reading a page through Exa (Exa)

The site audit and the citation check read pages with plain HTTP, the way a crawler that runs no
JavaScript does. That is the point of the audit, and it is also how both checks get fooled: a page
that is an empty app shell without JavaScript reads as `page.needs-js` when the content is there
once rendered, and a cited page that is a shell, or that serves a bot challenge to anything that
isn't a browser, reads as naming nobody. Both are findings about a page the assistants read fine.

With `[exa]` configured and `contents_fallback` on, such a page is read once more through Exa's
contents endpoint, which renders it. The trigger is narrow: a 200 HTML page with under 200
characters of visible text and a `<script>` or `<noscript>` in it, or a challenge page. The text
Exa returns is kept in `exa_contents` for a day, so a page costs at most one read a day whichever
check asks, and it stands in for the page's text. The citation row says whether the page names the
brand and which competitors, with `via: "exa"`. On the audit, the page's text count is what Exa
read, and the `page.needs-js` or `page.challenge` finding becomes an info one carrying
`via: "exa"`: the content exists, and crawlers that run no JavaScript still see the empty HTML, so
it stays on the page without going to the analyst as an error. The web UI and MCP show `via Exa`,
and the CLI marks the page `(via Exa)`. Everything else is as before: a page plain HTTP reads is
never sent to Exa, Exa failing to read a page leaves what plain HTTP got, and with the switch off,
no key or no price, nothing is called and the checks behave exactly as they did.

This is not a pass: it runs inside the two checks, which run on demand, and nothing reads Exa
from a page. `audit` takes the reader as a function in its options (`audit.Options.Reader`,
`audit.CiteOptions.Reader`), wired by `app` (`internal/app/contents.go`), so the package stays
free of providers.

```toml
[exa]
# contents_fallback = true

[pricing.exa-contents]
page_per_1k = 1
```

A citation check of 25 pages where three are shells reads three pages, a third of a cent, once a
day; an audit of a site built as a single-page app reads its home page and up to ten sitemap
pages, about a cent.

### One card for one page (Jev)

Quick wins list the latest fix list of every set, so three sets that each want a pricing page
showed three cards. After a run's fix list is built, with `[typesafe]` configured, every pair of
fixes from different sets' latest lists with the same type on the same site is decided once.
Targets that are the same page (`TargetKey`: scheme, `www.`, query string and trailing slash
aside) match by rule at 1.0. Pairs whose target paths or titles share a meaningful word are put
to Jev as one yes/no question each, with both fixes' title, target, claim and done-when in the
state and the category from `brand.about`: do they describe work on the same page, so that one
page would satisfy both? Pairs that share nothing are never asked about. Each decision is keyed
by a hash of the two fixes' signatures (type, target page, title), so rebuilt lists with the
same wording ask nothing.

Decisions land in `fix_matches` with the probability of yes and who decided (`rule` or `jev`).
`QuickWins` reads them at `typesafe.min_confidence`, groups the matched fixes (a chain of pairs
is one group) and keeps the highest-ranked card, with the others under `also_in`: the set, run
and rank of each, so `mark_fix` can still target any of them. The quick-wins page, `GET
/api/quick-wins` and MCP `get_quick_wins` say "also asked for by" with the other sets, and
filtering by set includes a card that set is also asking for. `cheapseats match-fixes <run-id>`
runs the pass over every set's latest list and prints the groups with their probabilities, and
the pairs decided below the threshold.

```toml
[typesafe]
# fix_matches = true
# min_confidence = 0.7   # Jev's probability that two fixes are one page
```

One request per candidate pair, a few hundred tokens each: five sets of ten fixes leave a few
dozen pairs after the type, site and shared-word filters, well under a cent per run.

### Page types and source classes for the defaults (Jev)

The run page sorts every page the answers drew on by what it does (`page_type`: listicle,
comparison, review, docs, forum, repo, article, product) and every host by whose site it is
(`class`: own, competitor, ugc, review, reference, institutional, editorial). Both come from rule
lists and URL words, and both have a default for what the lists don't know: `other` for a page,
`editorial` for a host. On a real run that is where a good share of the citations land, and it
says nothing about them.

After each run, each page whose rule type is `other` and that has a title is put to Jev once: a
Choice over every page type, with the definitions from the rule file as the criteria and the URL,
the title and the category from `brand.about` as the state. Each host whose rule class is
`editorial`, and that isn't the brand's, a competitor's or placed by `[sources]`, is put to it
once: a Choice over the five third-party classes, with up to three of its pages' titles and URLs
in the state. A page the rules already type (`/blog/…`, a docs host) and a page with no title are
never asked about. Decisions are per page and per host, not per run: once decided, a page or host
is never asked about again, whatever run cites it, so a run's pass is a few dozen requests and a
fraction of a cent.

Decisions land in `page_judgments` (`kind` `page_type` keyed by the normalized URL, or
`source_class` keyed by the host) with Jev's probability for the option it chose. The report reads
them back at `typesafe.min_confidence`: `report.Options.PageTypes` fills in a type where the rules
say `other`, and the hosts Jev classed are merged under the config's `[sources]` for the report's
classifier, so `[sources]` wins over Jev and Jev wins over the built-in default. The run page's
"Page types" and "Sources" tables, `page_types` and `classes` in `GET /api/runs/{id}` and MCP
`get_run`, change their counts; a page's type chip is marked "typed by Jev" where it was.
`cheapseats classify <run-id>` runs the pass on a finished run and prints the run's pages' and
hosts' decisions with their probabilities. With the table empty, or `[typesafe]` off, every page
renders exactly as the rules say.

```toml
[typesafe]
model = "jev-1.14.0"
# page_types = true
# min_confidence = 0.7   # Jev's probability for the type or class it chose

[sources]                # wins over Jev for any host it names
# ugc = ["forum.example.org"]
```

### The neutral retrieval baseline (Exa)

When a competitor dominates a run, the run alone can't say why: the web may agree with the
assistants (their pages answer the question better, a content problem), or the assistants' own
queries may be finding them where a neutral search wouldn't (a query problem). The two call for
different work. After each run, with `[exa]` configured, every prompt is searched once on Exa with
no domain filter, and the top `baseline_results` pages (default 10) are stored per run and prompt
in `retrieval_baseline`, the shape of `coverage`. Nothing is searched again for a prompt already
stored, so `cheapseats baseline <run-id>` on a finished run costs only the prompts not yet done.

The run page reads the table back against the run's answers. Under each prompt, beside the
coverage line, the neutral top pages carry two chips: whose the page is (ours, a competitor by
name, or its source class) and the status coverage uses, `cited`, `retrieved` (a search returned
it, no answer cited it) or `unseen`. Above the pages, a "Neutral retrieval" table has one row per
host over the prompts searched, with a competitor's configured domains as one row and the brand's
sites as one: `neutral` is the number of prompts a neutral index ranks the host for, `assistants`
the number of prompts any answer retrieved or cited it for, and the reading is `content` when
neutral is at least two and at least the assistants' count, `query` when the assistants found it
for two or more prompts and a neutral search never ranked it, and `both` otherwise. A host one
answer retrieved once, and the index never ranked, isn't a row. `GET /api/runs/{id}` carries all
of it as `baseline`; MCP `get_run` carries the top ten rows as `neutral_retrieval`.

```toml
[exa]
# baseline = true
# baseline_results = 10

[pricing.exa-search]
search_per_1k = 4
```

One search per prompt: fifteen prompts is six cents a run, the same as the coverage check.

## Next, in order

Each item below fits the six rules. They are ranked by how visible the gain is on our own data.

**Jev, judgments over data the run already has**

1. Cluster objections, praise and themes. Counting is by case-folded string, so "coverage
   unverified" and "coverage of ChatGPT and Claude unverified" are two objections. A Choice per
   new phrase over the run's existing clusters, with a none-of-these option.
2. A second opinion on verdicts. The verdict as a Choice with the brand and aliases in state,
   stored beside the grader's, with disagreements and low confidence as a regrade queue. Also
   makes grader drift measurable.
3. Prompt review at check time. Duplicates and off-target prompts are found today from a month
   of answers by overlap of named products. A Score over prompt pairs for "asks the same thing"
   and a Noul for "about something other than the category", as suggestions in `cheapseats check`.
4. Intent and branded detection at check time: suggest `intent = "info"`, catch a brand name the
   whole-word alias match misses.
6. Evidence sentence selection: a Choice over the answer's sentences, as a check on the grader's
   quoted evidence.

Not Jev: anything that produces a list or a sentence. The named-products list, reasoning,
why-not-top, objections as text and the fix list stay on a generative model.

**Exa, web evidence the run doesn't have**

1. Outreach discovery beyond what the answers cited: find-similar from the pages that shaped
   answers, or a search for the category's roundups, scored by Jev for "in our category" and
   "names us". Turns the outreach list from reactive to prospecting.

The strongest pairing is Exa finding candidate pages and Jev deciding whether each is a roundup
in the category that names the brand, with code ranking the result: the citation-check pattern
applied to outreach.
