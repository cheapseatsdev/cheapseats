# Prompt review

Prompt review finds flat, noisy, off-target, duplicate and branded questions, search rewrites,
and coverage gaps. It uses stored results, grades, searches and citations without model calls;
Jev judgments and proposed edits are optional calls. Review monthly. No review runs on a schedule.

- Page: `/sets/<id>/review`, linked from the set page. `?days=30` (the default), `60` or `90`.
- JSON: `GET /api/sets/<id>/review?days=30`, every prompt's numbers.
- MCP: `review_prompts` (`set`, optional `days`, `suggest`), with counts and the top five of each
  flag, numbers rounded. `get_started` suggests a review after four weeks of runs if none was
  marked in 30 days. **Mark reviewed**, the CLI and MCP record a review; opening the page or
  JSON does not. The page shows the last recorded review.
- CLI: `cheapseats review <set-id> [-days 30] [-json] [-suggest] [-jev]`.
- With `[typesafe]` configured, a "Jev" section (JSON `jev`) lists what Jev makes of the prompts'
  wording, read from storage: see [Jev's flags](#jevs-flags) below.

For each prompt, the review walks backward through finished runs until its wording changes.
Intent or method changes do not stop pooling; the row flags them. If config wording changed
since the last run, the review uses and identifies the last-run wording.

A prompt needs **3 runs and 6 graded answers** in the window. Otherwise it is listed under
"not enough data yet", with no flags except branded prompts, detectable from the first run.
These are eligibility gates for the checks, not guarantees of precision or prompt quality.

## Flags

Each flag comes with the numbers behind it and a one-line suggestion. Thresholds are constants in
`internal/app/review.go`. The table describes the current generated suggestions; review them
against the buying situation before changing a prompt. A flag is evidence to investigate, not
an instruction to make the brand win.

| Flag | Rule | Current generated suggestion |
|---|---|---|
| **Flat**, always absent | No answer named the brand, and no fix attempt made in the window moved this prompt's own named or recommended rate beyond its 95% margin. Another prompt in the same fix moving doesn't count. | Either reword or retire an unwinnable question, or keep it as an important gap and work its fixes. |
| **Flat**, always recommended | Every answer recommended the brand (for an info prompt, named it). | At the ceiling, it only tells you when something breaks: reword it harder, or retire it to save answers. |
| **Noisy**, drift | For answerer × track cells with two or more samples: a χ² homogeneity test, Σ (h − n·p̂)² / (n·p̂(1−p̂)) over runs, with runs − 1 degrees of freedom per cell, summed over cells, gives **p < 0.01**; between-run variance D has standard deviation at least **15 points**. | Specify who is asking and for what; more samples don't steady drift. |
| **Noisy**, disagreement | A single answer's within-run variance S is at least **0.20** (answers in one run split about 30/70 or closer) and the per-run sampling error sqrt(S/n) is still at least **15 points**. | Reword it to be more specific, or add samples. |
| **Off target** | Any of: with 3 or more competitors configured and at least **5** product mentions, under **30%** of them are the brand or a configured competitor; one product that's neither is named in at least **50%** of the answers and more than the brand and every competitor; at least **25%** of the answers are graded as using the brand's name for another product (`other_product`). The top unexpected products are listed with counts. | Add context to the prompt (the category, who is asking), or add the product to the competitors if it is one. |
| **Duplicate** | Two prompts of the same intent whose answers name nearly the same products, each product weighted by the share of answers naming it (weighted Jaccard, Σmin/Σmax, at least **0.75**), and whose verdict shares (recommended, mentioned, absent) differ by at most **15 points** (total variation). | Merge them to save answers: keep one, retire the other. |
| **Rewritten by the assistant** | Over at least **5** searches, both from prompt (the share of a search's words in the prompt) and prompt kept (the share of the prompt's words in the search) average under **25%** ([how words are compared](searches.md)). | The assistants search for different words (the top ones it adds are listed); consider phrasing the prompt that way. |
| **Branded leak** | Outside `head-to-heads`: the prompt names the brand or an alias as a whole word (`cheapseats check` refuses these, so it's for prompts from before), or at least **2** of its searches and **20%** of them add the brand's name. | Reword it as a buyer who hasn't heard of the brand would ask. For searches, check the prompt doesn't point at the brand (a term only it uses); if not, the assistants found it on their own. |

The noise split matches [sample advice](statistics.md#a-sets-prompts): per cell, S is mean
within-run variance n/(n-1)·p(1-p); D is run-rate variance left after S/n. Only complete planned
samples count, excluding adaptive extras and cells short of their usual count. A run with no
graded answer for the prompt does not count. One sample per cell cannot identify the split, so
noise is not flagged. The χ² test is an approximation, not a guarantee about false flags.
Products named repeatedly or through aliases count once per answer.

A prompt with no flag and enough data is listed as healthy. That means none of these checks
flagged its stored results; it does not establish buyer demand, neutrality, coverage or stability.
In particular, one sample per answerer and track cannot separate drift from disagreement, so
neither noise flag appears even when results vary.

Keep a stable core of useful buyer questions for trends, alongside exploratory questions for
new needs. Always-absent questions can expose important gaps or genuine product limitations;
ceiling questions can catch regressions. Keep them when they serve those purposes. Do not
automatically retire them, make them harder, or rewrite them toward the brand's capabilities.
Likewise, similar answers make a duplicate candidate: check whether different constraints change
the buying decision before merging. Record the reason for a change rather than treating a flag
as that reason.

## Jev's flags

With `[typesafe]` configured ([optional providers](optional-providers.md)), Jev judges wording
without waiting for runs. Each question is a separate request, cached in `prompt_judgments`
by its inputs and wording; changes re-ask only affected questions. Flags require probability
at least `typesafe.min_confidence` (default 0.7). Changing the threshold reads stored judgments
without calls.

| Flag | Question | Suggestion |
|---|---|---|
| **duplicate** | Over each pair of prompts of the same intent that share a meaningful word: one good answer would serve both. | Merge them to save answers: keep one, retire the other. |
| **off_target** | Per prompt, with `brand.category` and `brand.audience` in state: the prompt asks about something other than the category. Not asked without `brand.category`: against the product description in `brand.about` it flags every prompt that isn't about this one product. | Add context to the prompt (the category, who is asking), or retire it. |
| **intent** | Per prompt, a Choice over `selection` (the useful answer points to a category product, including how-to jobs the category serves even when no product was requested) and `info` (an explanation without products); flagged when it reads as the other intent from the one it's set to. | Set or drop `intent = "info"`. |
| **branded** | Per prompt outside `head-to-heads`, with the brand's name, aliases, description and sites in state: the prompt names the brand, a product of its maker or its domain in any spelling ("cheapseats.dev", "cheap-seats"). | Reword it as a buyer who hasn't heard of the brand would ask. |

The current Jev selection criterion is broad: it can treat a category's how-to job as selection
without an explicit request to choose a product. Review ambiguous cases yourself. Ask whether
a complete, useful answer requires choosing, ranking or comparing products. If an explanation
can fully satisfy the buyer without one, use `intent = "info"`; an optional product example
does not make the question selection. Correct the label instead of adding a request for a tool
solely to fit recommendation scoring. Named rates include info answers; recommended and top-pick
rates exclude them. Changing intent keeps the id but changes those denominators, so comparisons
must exclude that prompt across the change.

`cheapseats check` judges every set after validation, printing `suggestion: set <id>: ...`
with probabilities. Suggestions never fail validation; missing credentials print a note;
`-no-jev` skips judgments. The post-run `prompt-review` pass judges that set;
`cheapseats judge <run-id>` or `cheapseats review -jev <set-id>` asks on demand.

The page, JSON, MCP `review_prompts` and CLI review without `-jev` read stored judgments only.
Without `[typesafe]`, there is no Jev section.

## Gaps

What the set's answers and searches keep coming back to that no prompt names, at most five of each:

- **Competitor**: a configured competitor named in at least **20%** of the set's answers, or added
  by the assistants to at least **3** searches, that no prompt names. Shown with its answers,
  searches and the answers citing a page on its site.
- **Product**: a product named in at least 20% of the answers, in two or more prompts' answers,
  that is neither the brand nor a configured competitor and isn't in any prompt. Comes with a
  `[[competitors]]` entry as well as a prompt.
- **Topic**: a word the searches add to their prompt in at least **5** searches across **2** or
  more prompts that no prompt uses, with the most-run search that has it.

Each gap includes a buyer-worded `[[prompts]]` entry with no search operators and an unused id:
"What are the best alternatives to X?" for products, or the search as a question for topics.
Review the wording before committing; ids are permanent.
These are gaps in the observed answer/search coverage, not independent evidence that buyers ask
the proposed questions. Validate the need and its weighting against buyer research when available.
A named competitor shortlist can be useful research, but omission of a third brand there means
something different from omission in open category discovery; interpret those questions separately.

## Proposed edits (optional, one analyst call)

The page's button, `cheapseats review -suggest <set-id>` or MCP `suggest: true` sends flags and
numbers to `[analyst]`, returning a diff against the set's file. Calls use the daily admission
budget; `claude-code` records $0 API spend on the subscription. Web-derived names and search
words are fenced as data, quoted on one line and capped at 60 characters.

- **Rewrites** replace a prompt's `text` and keep its `id`, so its history reads on as a reword.
- **Retirements** are comments: delete the table by hand to stop asking it; the id stays retired.
- **Additions** are new `[[prompts]]` tables with ids that don't collide with any set's.

Keep an id for a wording clarification that preserves the buyer need. A different buying question
needs a new id, even if a proposed rewrite reused the old one. Assess proposed changes against the
stable core and measurement goal before applying them; generated edits do not establish demand or
replace a review of intent.

Proposals are dropped, with reasons, for nonexistent targets, unchanged rewrites, duplicate text
across sets, search operators, quotes, links, text over 300 characters, brand names outside
`head-to-heads` or missing brand names inside it. A copy of the edited sets must also pass
validation. Nothing is written; apply reviewed changes through a config-repo pull request.
