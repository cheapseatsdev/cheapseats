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
  opposed to what an assistant found. A search is about $4 per thousand; page contents cost extra
  and aren't fetched.

## Getting a key

- **Exa**: sign up at [dashboard.exa.ai](https://dashboard.exa.ai) and make a key under API keys.
  As of October 2026 the free plan gives $10 of credits every month, plus a $10 onboarding bonus,
  with no payment method. At $4 per thousand searches that is 2,500 searches a month; the coverage
  check uses one search per prompt per run, so five sets of 15 to 20 prompts run weekly is about
  350 searches a month, well inside the free plan. Most people will never pay for this.
- **TypeSafe**: make a key in the [console](https://console.typesafe.ai/keys). Input tokens are
  $0.042 per million and a run's whole folding pass is a few thousand tokens, so a month of runs
  is cents.

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
at a tie, the shorter, so "Scrunch" is asked about "Scrunch AI" and not the reverse. Up to eight
names go in one request; a run's whole pass is usually one call and a fraction of a cent.

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
wording had given 0.53, 0.67 and 0.58. Give it state, and say what counts; it reads literally. The first production pass decided 28 names in four requests for half a cent:
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
5. Fix de-duplication across sets: a Score for "the same page" over fix pairs, so three sets
   asking for one cost page show one card.
6. Page type and source class for hosts the rule lists call "other" or "editorial" by default.
7. Evidence sentence selection: a Choice over the answer's sentences, as a check on the grader's
   quoted evidence.

Not Jev: anything that produces a list or a sentence. The named-products list, reasoning,
why-not-top, objections as text and the fix list stay on a generative model.

**Exa, web evidence the run doesn't have**

1. Outreach discovery beyond what the answers cited: find-similar from the pages that shaped
   answers, or a search for the category's roundups, scored by Jev for "in our category" and
   "names us". Turns the outreach list from reactive to prospecting.
2. A neutral retrieval baseline: each prompt as a plain search, compared with what the
   assistants searched and read. A competitor topping neutral retrieval is a content problem; one
   that only shows in the assistants' searches is a query problem.
3. Competitor discovery: find-similar from competitors' home pages, as suggestions for
   `[[competitors]]`, never an automatic edit.
4. Contents as a fetch fallback for the site audit and citation check when a page is empty
   without JavaScript.
5. A second engine for the search probe.

The strongest pairing is Exa finding candidate pages and Jev deciding whether each is a roundup
in the category that names the brand, with code ranking the result: the citation-check pattern
applied to outreach.
