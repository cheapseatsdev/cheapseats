# Optional providers: TypeSafe's Jev and Exa

Core grading and analysis use Claude through the Anthropic API or local `claude` CLI; answerers
can also use OpenAI. Add **[TypeSafe's Jev](https://typesafe.ai)** for stored typed judgments or
**[Exa](https://exa.ai)** for independent web search and fallback page text. Neither is required;
absent `[typesafe]` and `[exa]` tables make no calls to those services.

## Getting a key

| Service | Key | Key-adding proxy alternative |
| --- | --- | --- |
| [TypeSafe console](https://console.typesafe.ai/keys) | `TYPESAFE_API_KEY` | `CHEAPSEATS_TYPESAFE_BASE_URL` |
| [Exa dashboard](https://dashboard.exa.ai) | `EXA_API_KEY` | `CHEAPSEATS_EXA_BASE_URL` |

On exe.dev, add integrations and point these base URLs at them; the VM need not hold the keys.

**October 2026 pricing examples:** Jev input was $0.042/million tokens, output free, with $5/month
free usage (about 119 million input tokens). Exa search was about $4/1,000 and contents $1/1,000
pages; its free plan offered $10/month plus a $10 onboarding credit without a payment method.
These are dated examples, not current billing guarantees. Configure prices for the services you use.
At those rates, 15 prompts cost six cents for coverage and another six for baseline, before
prospects, probes, discovery or contents reads. Free credits do not remove budget accounting.

```toml
[typesafe]
model = "jev-1.14.0"     # pin a version; jev-latest can move
# min_confidence = 0.7

[pricing."jev-1.14.0"]
input = 0.042

[exa]

[pricing.exa-search]
search_per_1k = 4

[pricing.exa-contents]
page_per_1k = 1
```

## The shape that keeps them optional

- **Configuration and readiness:** feature switches default on when their provider table exists.
  A missing key/proxy produces a check-time note and skips automatic passes. Set a switch false
  to stop its automatic pass; explicit CLI pass commands still run it. Stored results remain.
- **Stored results:** passes run after collection or on demand and write SQLite. Opening a report
  reads those results without paid calls. Search probes and explicitly requested checks are
  on-demand operations, not passive report views.
- **Budget:** calls reserve and settle against the daily ledger. No configured price means no
  call. Exa's returned cost estimate takes precedence when present. Reservations are estimates,
  not a hard provider billing ceiling.
- **Thresholds:** `typesafe.min_confidence` defaults to 0.7 and is applied when stored decisions
  are used. Changing it does not re-ask questions. Decisions retain source and probability.
  Gate on `typesafe.Chosen`: the probability of yes or the chosen option, with the response's
  `confidence` as a fallback when no option probability exists. `confidence` alone describes
  concentration of the full distribution and varies with the number of options.
- **Data sent:** Jev can receive prompts, answer text/excerpts, brand context, product names,
  fix briefs and page metadata; Exa receives queries, site filters, seed URLs and requested URLs.
  Keys/proxies authorize these transfers. Keep secrets out of public brand context and prompts.
- **Bounded replies:** successful responses are capped at 4 MiB for Jev and 32 MiB for Exa
  (including contents). Oversized replies fail without retry and settle as unknown cost; error
  messages retain at most 4 KiB. Anthropic/OpenAI responses have the same 32 MiB cap.

## How a pass is wired

`internal/app/passes.go` defines `Pass`. Each pass registers from `init()` in its own file with a
ledger name, CLI command, order, provider, switch and function returning its item count. Orders
below 100 run before the fix list so the analyst can use their results; later passes run after.
Unavailable providers are skipped; a failed pass is logged and the next continues.

Passes can use TypeSafe, Exa or `config.GraderProvider` (as the [fact check](misstatements.md)
does). Results live in provider-specific SQLite tables. `exa.Client` exposes `Search`,
`FindSimilar` and `Contents`; contents uses `[pricing.exa-contents] page_per_1k`.

## Shipped

| Use | Automatic switch | Manual command |
| --- | --- | --- |
| [Fold names](#folding-product-names-jev) | `[typesafe] fold_names` | `cheapseats fold <run-id>` |
| [Cluster phrases](#clustering-objections-and-praise-jev) | `[typesafe] cluster_phrases` | `cheapseats cluster <run-id>` |
| [Second opinions](#second-opinions-on-grades-jev) | `[typesafe] second_opinions` | `cheapseats verdicts <run-id>` |
| [Prompt judgments](#prompt-judgments-at-check-and-review-time-jev) | `[typesafe] prompt_review` | `cheapseats judge <run-id>` |
| [Page classification](#page-types-and-source-classes-for-the-defaults-jev) | `[typesafe] page_types` | `cheapseats classify <run-id>` |
| [Fix grouping](#one-card-for-one-page-jev) | `[typesafe] fix_matches` | `cheapseats match-fixes <run-id>` |
| [Own-page coverage](#the-coverage-check-exa) | `[exa] coverage` | `cheapseats coverage <run-id>` |
| [Retrieval baseline](#the-neutral-retrieval-baseline-exa) | `[exa] baseline` | `cheapseats baseline <run-id>` |
| [Outreach prospects](#outreach-prospects-exa-and-jev-when-configured) | `[exa] prospects` | `cheapseats prospect <run-id>` |
| [Competitor discovery](#competitor-discovery-exa-jev-optional) | On demand | `cheapseats discover` |
| [Search probe](#a-second-engine-for-the-search-probe-exa) | On demand | `cheapseats probe -engine both "<query>"` |
| [Page contents](#reading-a-page-through-exa-exa) | `[exa] contents_fallback`, during checks | Audit or citation check |

### Folding product names (Jev)

Jev folds unconfigured name variants onto known products on the share-of-voice board. A new name
with plausible candidates gets one Choice among those matches (shared meaningful words or
containment) and `none`, with category context and an answer sentence per product. Category uses
`brand.category`, falling back to the first sentence of `brand.about`. Brand, competitor and
configured-alias names are already known. Among unconfigured variants, the less frequent name is
asked about the more established one; at a tie, the shorter name is asked.

`name_decisions` stores the chosen match and its probability. `App.Aliases` combines eligible
stored matches with configured aliases; `report.Options.Aliases` folds board names and top picks.
A `none` decision leaves a separate product and is not asked again. Each undecided candidate takes
one request; the CLI prints matches and probabilities.

Give Jev category and answer context, define what counts as the same product, and keep one name's
candidates in each request. Bare names and unrelated batched questions can change the judgment.
Use the chosen-option probability described above, not distribution confidence alone.

### Clustering objections and praise (Jev)

Jev groups differently worded objections or praise that call for the same action. New phrases
are processed most-answers-first, shorter first at a tie. A phrase without a meaningful word in
common with an existing cluster becomes its own cluster without a call. Otherwise, one Choice
request offers up to eight plausible clusters (largest first) and `none`, with category, kind,
answer context and a sentence per candidate. Narrower wording of the same point may match;
a different feature or point does not.

`phrase_clusters` stores the decision, probability and original display wording. At the current
threshold, `App.PhraseClusters` feeds report objections, sentiment praise/objections and their
associated pages. A grouped objection shows its number of wordings; API/MCP objection rows carry
`wordings`. Already decided phrases are skipped. The CLI prints each phrase, cluster and probability.

### Second opinions on grades (Jev)

Each graded answer gets one request containing its question, answer, brand name, aliases and
`about`. Jev chooses among `recommended`, `mentioned` and `absent` using the same
`grade.Definitions` as the grader. It also chooses supporting evidence from up to 12 answer
sentences naming the brand, except when the grader marked it absent. Answers over 12,000
characters are cut at a sentence boundary.

`grade_opinions` stores Jev's verdict probability, sentence and the grader's verdict at that time.
Reports compare the stored opinion with the **current** grade: `agrees`, `disagrees`, or `unsure`
below the threshold. Evidence is `same` after case/space/punctuation folding or containment,
`differs`, or `none`. Regrading changes the comparison without re-asking Jev; opinions are not
refreshed merely because the grader changed.

The run shows counts, agreement among opinions above threshold and linked disagreements; each
answer shows both readings. API `second_opinion`, MCP `get_run` (up to ten disagreements),
`get_result`, the set's **Jev agrees** column and the CLI expose them. Changes in agreement can
prompt review, but agreement with another model is not proof of correctness or a controlled
measure of grader drift. No grade is replaced.

### The coverage check (Exa)

Each prompt is searched over the brand's `[[brand.sites]]` and `brand.domains` (hosts or host/path
prefixes). `[exa] results` defaults to 3. Results are stored per run/prompt in `coverage`;
completed prompts, including empty results, are not searched again.

Under each prompt, pages are `cited`, `retrieved` (recorded search result or fetch, without a
citation), or `unseen` in that prompt's answers. API `coverage` and MCP `own_pages` expose them.
One search per unchecked prompt costs six cents for 15 prompts at the example price.

This supplies leads for content and retrieval work. An Exa miss does not prove a page is absent
from every index; a result does not establish that it answers the question well or that an
assistant read it. Inspect the page and the assistant's evidence before choosing work.

### Competitor discovery (Exa, Jev optional)

On demand, Exa finds pages resembling each configured competitor's first domain and the brand's
first site, excluding the source and configured domains. `[exa] discover_results` defaults to 10
per seed. Results are grouped by host with seed count, a cleaned title/name, best page and
`on_board`: matching appearances in each set's latest run.

If TypeSafe is callable, each unjudged host gets one yes/no question using its URL, title and
category. `competitor_suggestions` keeps the result. Reruns refresh seeds/counts, retain prior
judgments and dismissals, and drop undismissed suggestions Exa no longer returns.

Suggestions sort by seeds, board count and Jev probability. Judged hosts below the current
threshold are hidden; unjudged hosts remain. The CLI prints TOML to review and paste;
`cheapseats discover -json` prints JSON. Nothing edits your config.

```toml
[[competitors]]
name = "Acme"
domains = ["acme.com"]   # similar to 2 seeds; named in 7 answers
```

`GET /api/competitors/suggestions` reads `suggestions` and `toml`;
`POST /api/competitors/suggestions/{host}/dismiss` retains a dismissal. Neither calls a provider.
MCP `suggest_competitors` runs discovery and returns the top ten plus TOML. Cost is one Exa search
per seed (six seeds cost 2.4 cents at the example price), plus Jev for newly judged hosts.

### A second engine for the search probe (Exa)

The probe accepts `claude` (default), `exa` or `both`. Exa uses the query with the same domain
allow/block filters and returns its own ordering as `exa-search`; `both` runs both engines and
compares normalized URLs as `overlap`, `only_claude` and `only_exa`. A trailing slash or fragment
does not create another page.

Use `/search`, `POST /api/search-probe`, MCP `search_probe`, or:

```bash
cheapseats probe -engine both -domains example.com -exclude other.example "<query>"
```

The engine selector appears when `[exa]` exists; requesting Exa without it errors. The probe has
no automatic-pass switch, stores nothing and needs configured pricing. Exa requests ten results
(not a guarantee it returns ten), costing $0.004 at the example price; `both` adds the Claude
probe's usage. Different results identify a collection difference, not which engine is right.

### Reading a page through Exa (Exa)

Audit/citation checks can request Exa contents when plain HTTP gets a 200 HTML shell with under
200 visible characters and a `<script>` or `<noscript>`, or a 200 bot-challenge page. This is not
a post-run pass. It requires `[exa] contents_fallback` (default true), a callable provider and a
positive `[pricing.exa-contents] page_per_1k`.

Successful text is normalized, clipped to 200,000 bytes and cached in `exa_contents` for 24 hours
across both checks. Empty or failed responses are not cached, so later checks may retry and incur
further charges. A normal readable page is not sent to Exa. Failure leaves the plain-HTTP result.

Citation rows use the alternative text for name matching and carry `via: "exa"`. Audit
`page.needs-js`/`page.challenge` findings become informational when alternative text is available;
plain clients still received a shell or challenge. UI/MCP show **via Exa**; CLI shows `(via Exa)`.
This establishes what Exa returned, not what an assistant or every crawler could read.

The provider-free audit package receives an `audit.Reader` through its options; app wiring lives
in `internal/app/contents.go`. At the example price, three successful page reads cost $0.003.

### One card for one page (Jev)

After the fix list, pairs from different sets' latest lists are considered only when type and
site match. Identical normalized target pages (`TargetKey`, ignoring scheme, `www.`, query and
trailing slash) match by rule at 1.0. Other pairs need a meaningful word shared by title/path
before one yes/no request asks whether one page could satisfy both. State includes both titles,
targets, claims, done-when criteria and the category context described above.

`fix_matches` stores probability and source (`rule` or `jev`), keyed by both signatures (type,
target, title). Unchanged pairs are not asked again. Above the threshold, Next steps groups
connected matches among verified actions and keeps the highest-ranked card;
`also_in` retains every other set/run/rank so `mark_fix` can target any of them.
Set filters include grouped cards requested by that set.
The UI, `/api/quick-wins`, MCP `get_quick_wins` and CLI show the groups; the CLI also lists
below-threshold decisions. Grouping does not complete the fixes.

### Page types and source classes for the defaults (Jev)

Jev fills rule defaults: pages typed `other` with a title, and third-party hosts classed
`editorial` that are not explicitly configured in `[sources]`. One page Choice uses URL, title,
category and the rule definitions; one host Choice uses up to three page titles/URLs and the
five third-party classes. Pages already typed by rules, pages without titles and configured
hosts are not asked about.

`page_judgments` stores normalized-URL `page_type` and host `source_class` decisions. At the
current threshold, page judgments fill only `other`; host judgments supplement the classifier.
`[sources]` overrides Jev, which overrides the built-in default. Decisions are reused across
runs, not refreshed automatically when a page changes.

Run/API/MCP `page_types` and `classes` reflect them; chips identify **typed by Jev**. The CLI
prints decisions and probabilities. Existing stored judgments can remain in reports after
collection switches are disabled. Definitions and override examples are in
[Reading a run](reading-a-run.md#configuring-it).

### The neutral retrieval baseline (Exa)

Each prompt is searched on Exa without domain filters; `[exa] baseline_results` defaults to 10.
`retrieval_baseline` stores pages per run/prompt and skips completed prompts, including empty
results. Pages show ownership/source class and the same cited/retrieved/unseen statuses as coverage.

The **Neutral retrieval** table counts prompts where Exa ranked a host (`neutral`) and prompts
where an answer retrieved or cited it (`assistants`), over checked prompts. Brand sites are one
row; each competitor's domains are another. Rows include hosts Exa ranked at least once, or
assistants found for at least two checked prompts:

| Reading | Rule |
| --- | --- |
| `content` | Neutral count is at least two and at least the assistants' count |
| `query` | Assistants' count is at least two and neutral count is zero |
| `both` | Other included rows |

These labels are investigative heuristics, not proof that content or query rewriting caused a
result. Exa is another index, not ground truth. API returns `baseline`; MCP keeps ten host rows
as `neutral_retrieval`. Cost is one search per unchecked prompt: six cents for 15 at the example price.

### Outreach prospects (Exa, and Jev when configured)

Exa find-similar starts from the most-cited third-party listicles/comparisons: `[exa] prospect_seeds`
defaults to 10, `prospect_results` to 5. It excludes brand/competitor domains and pages already
cited, returned or fetched in the run, then deduplicates results across seeds. These are possible
outreach pages, not pages demonstrated to influence an answer.

Callable Jev judges each new page's category from URL/title; unjudged pages retain NULL and remain
eligible. The citation checker reads them over plain HTTP with robots rules and a UTC-day cache,
without Exa contents fallback. `prospects` stores the rows. Seeds with stored results are skipped
on rerun; seeds yielding no kept pages can be searched again, so rerunning is not always free.

The run lists pages above today's Jev threshold (or unjudged), ordered by competitors named
without the brand, then probability, then seed citation count. API returns `prospects`; MCP
keeps ten (`url`, `names`, `why`); the CLI runs the pass and prints them. Cost is one Exa
find-similar per searched seed (ten cost four cents at the example price), plus Jev judgments;
plain HTTP reads have no provider charge.

### Prompt judgments at check and review time (Jev)

Jev judges wording before a set has enough answers for statistical review:

| Kind | Question and eligibility |
| --- | --- |
| `duplicate` | Could one good answer serve two same-intent prompts sharing a meaningful word? |
| `off_target` | Is the prompt outside `brand.category`, considering audience? Not asked without a category. |
| `intent` | Does the useful answer point to category products (`selection`) or explain without them (`info`)? The current criterion includes how-to jobs the category serves even when no product was requested. |
| `branded` | Does it name the brand, its maker's product or domain in any spelling? Skipped for `head-to-heads`. |

Intent judgments are suggestions. For ambiguous how-tos, check whether a complete answer
requires product selection; a useful product-free explanation belongs under `intent = "info"`.
An optional tool example does not require recommendation scoring. See
[reviewing Jev's intent flags](prompt-review.md#jevs-flags) before changing a label.

Each request asks one question. `prompt_judgments` keys decisions by prompt text (or pair), the
relevant category/audience or brand context, and question type/instructions/criteria. Rewording
one prompt invalidates only its questions; changing judgment instructions invalidates that kind.
Unchanged checks reuse stored decisions. First-use calls are roughly three per prompt plus
plausible pairs, fewer where eligibility excludes a question.

`cheapseats check` prints suggestions above today's threshold without failing validation. Missing
credentials produce a note; `-no-jev` skips these calls. Prompt review (`/sets/{id}/review`, JSON
`jev`, MCP `review_prompts`) reads stored flags without calling Jev. The pass also runs after a
run; `cheapseats review -jev <set-id>` requests fresh missing judgments before reviewing.

```toml
[typesafe]
prompt_review = false   # disable after-run and check-time judgments; explicit commands can still ask
```

## What's next

For new provider work, preserve the budget, storage and data boundaries above. Existing tables
reserve `source = 'manual'`, but no manual override workflow writes it yet. Jev supplies typed
judgments; generated product lists, reasoning, objections and fix prose remain core-model work.

## Advice verification

Coverage, neutral retrieval, citation checks and prospects supply observations, not permission to
create a page, add a feature or contact a publisher. [Next steps](next-steps.md) verifies proposed
changes against current artifacts, capability claims, advisory boundaries and any eligibility
rules. Bounded current-page excerpts are sent to the configured analyst provider for this check;
keep private information out of the advisory brief and public facts. Unreadable content and
missing metadata remain unknown. Only verified actions receive implementation handoffs.
