# Prompt review

Runs grade the brand. The prompt review grades the prompts: which ones tell you nothing, which move
for no reason, which are answered about something else, which ask the same thing twice, which the
assistants search for in other words, and what no prompt asks about. It's worked out from what
Cheap Seats already stores (results, grades, searches, citations); it calls no model and costs
nothing, so it runs when you open it. Review monthly: a month of runs is what the flags need.
Nothing runs on a schedule.

- Page: `/sets/<id>/review`, linked from the set page. `?days=30` (the default), `60` or `90`.
- JSON: `GET /api/sets/<id>/review?days=30`, every prompt's numbers.
- MCP: `review_prompts` (`set`, optional `days`, `suggest`), the counts and the top five of each
  flag, numbers rounded. `get_started` suggests it once a set has four weeks of runs and hasn't
  been reviewed in 30 days. The page's **Mark reviewed** button, the CLI and the MCP tool count as a
  review; opening the page and the JSON don't, so a link from another site can't quiet the
  reminder. The page shows when the set was last marked reviewed.
- CLI: `cheapseats review <set-id> [-days 30] [-json] [-suggest] [-jev]`.
- With `[typesafe]` configured, a "Jev" section (JSON `jev`) lists what Jev makes of the prompts'
  wording, read from storage: see [Jev's flags](#jevs-flags) below.

The review reads the set's finished runs in the window, and each prompt under the wording it was
last asked in: walking back from the newest run, it stops at the first run that asked it in other
words (a different question, so a different measurement). A change of intent or of method (how
answers are asked and graded) in the window doesn't stop it; the prompt's row says so, since the
rates pool both sides. A prompt reworded in the config since its last run is read under the old
wording, and says that too.

A prompt needs **3 runs and 6 graded answers** in the window to be reviewed. Fewer, and it's
listed under "not enough data yet" with no flags, except a branded prompt, which is worth knowing
about from the first run.

## Flags

Each flag comes with the numbers behind it and a one-line suggestion. Thresholds are constants in
`internal/app/review.go`.

| Flag | Rule | Suggestion |
|---|---|---|
| **Flat**, always absent | Not one answer in the window named the brand, and no fix attempt made in the window moved this prompt (its own named or recommended rate up beyond its 95% margin; another prompt in the same fix moving doesn't count). | Tells you little as it stands: either the brand can't win this question (reword it toward what the brand does, or retire it), or it's the most important gap in the set (keep it and work its fixes). Both are said; you know which. |
| **Flat**, always recommended | Every answer recommended the brand (for an info prompt, named it). | At the ceiling, it only tells you when something breaks: reword it harder, or retire it to save answers. |
| **Noisy**, drift | Over the prompt's answerer x track cells with two or more samples, the run rates differ more than one binomial rate per cell explains (a χ² test of homogeneity, Σ (h − n·p̂)² / (n·p̂(1−p̂)) over runs with runs − 1 degrees of freedom, summed over cells, at **p < 0.01**), and the between-run variance D has a standard deviation of at least **15 points**. Pure sampling noise trips it under 1% of the time. | Reword it to be more specific (who is asking, for what): more samples don't steady drift. |
| **Noisy**, disagreement | A single answer's within-run variance S is at least **0.20** (answers in one run split about 30/70 or closer) and the per-run sampling error sqrt(S/n) is still at least **15 points**. | Reword it to be more specific, or add samples. |
| **Off target** | Any of: with 3 or more competitors configured and at least **5** product mentions, under **30%** of them are the brand or a configured competitor; one product that's neither is named in at least **50%** of the answers and more than the brand and every competitor; at least **25%** of the answers are graded as using the brand's name for another product (`other_product`). The top unexpected products are listed with counts. | Add context to the prompt (the category, who is asking), or add the product to the competitors if it is one. |
| **Duplicate** | Two prompts of the same intent whose answers name nearly the same products, each product weighted by the share of answers naming it (weighted Jaccard, Σmin/Σmax, at least **0.75**), and whose verdict shares (recommended, mentioned, absent) differ by at most **15 points** (total variation). | Merge them to save answers: keep one, retire the other. |
| **Rewritten by the assistant** | Over at least **5** searches, both from prompt (the share of a search's words in the prompt) and prompt kept (the share of the prompt's words in the search) average under **25%** ([how words are compared](searches.md)). | The assistants search for different words (the top ones it adds are listed); consider phrasing the prompt that way. |
| **Branded leak** | Outside `head-to-heads`: the prompt names the brand or an alias as a whole word (`cheapseats check` refuses these, so it's for prompts from before), or at least **2** of its searches and **20%** of them add the brand's name. | Reword it as a buyer who hasn't heard of the brand would ask. For searches, check the prompt doesn't point at the brand (a term only it uses); if not, the assistants found it on their own. |

The noise split is the one the set page's sample advice uses ([statistics](statistics.md)): per
cell, S is the mean within-run variance n/(n-1)·p(1-p) and D what's left of the variance of the
run rates after S/n. Only planned samples count, in the run rates as in the split (adaptive sampling's extras would give a cell a
different count each run), and a run where a cell is short of its usual count is left out of that
cell; a run where every answer to the prompt errored doesn't count as a run. With one sample per
cell, noise can't be split and isn't flagged. A product named twice in one answer (by its name and
an alias) counts once.

A prompt with no flag and enough data is listed as healthy.

## Jev's flags

The flags above need a month of answers. With `[typesafe]` configured
([optional providers](optional-providers.md)), the prompts' wording alone is put to Jev, one
question per request, and the answers are stored in `prompt_judgments` under a hash of what each
question saw and how it was worded, so a prompt is asked about once per wording and a reworded
prompt, or a reworded question, re-asks only its own questions. A flag shows when Jev's probability is at least `typesafe.min_confidence`
(default 0.7); changing the threshold re-reads the stored answers and asks nothing.

| Flag | Question | Suggestion |
|---|---|---|
| **duplicate** | Over each pair of prompts of the same intent that share a meaningful word: one good answer would serve both. | Merge them to save answers: keep one, retire the other. |
| **off_target** | Per prompt, with `brand.category` and `brand.audience` in state: the prompt asks about something other than the category. Not asked without `brand.category`: against the product description in `brand.about` it flags every prompt that isn't about this one product. | Add context to the prompt (the category, who is asking), or retire it. |
| **intent** | Per prompt, a Choice over `selection` (the answer that serves the buyer names a product in the category, including a how-do-I question about a job those products do) and `info` (it names none: what something is, how it works, whether a practice is worth doing); flagged when it reads as the other intent from the one it's set to. | Set or drop `intent = "info"`. |
| **branded** | Per prompt outside `head-to-heads`, with the brand's name, aliases, description and sites in state: the prompt names the brand, a product of its maker or its domain in any spelling ("cheapseats.dev", "cheap-seats"). | Reword it as a buyer who hasn't heard of the brand would ask. |

Who asks: `cheapseats check`, after its own checks, judges every set and prints one
`suggestion: set <id>: ...` line per flag with the probability; suggestions never fail `check`,
without a key or proxy it prints one note and continues, and `-no-jev` skips it. After every run
the `prompt-review` pass judges the run's set, so a new prompt is judged at its first run
(`cheapseats judge <run-id>` runs it on demand). `cheapseats review -jev` asks before reviewing.
The page, the JSON, `review_prompts` and `cheapseats review` only read what's stored: opening the
review never calls Jev, and a deployment without `[typesafe]` has no Jev section.

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

Each comes with a `[[prompts]]` entry to paste, in a buyer's words and without search operators,
with an id that isn't used in any set ("What are the best alternatives to X?" for a competitor or
product; the search itself as a question for a topic). Reword it before you commit it: ids are
permanent.

## Proposed edits (optional, one analyst call)

The page's button, `cheapseats review -suggest` or `review_prompts` with `suggest: true` send the
flags and their numbers to the analyst, fenced as data (product names and search words come from
the web, so they go quoted, on one line, at most 60 characters), (the `[analyst]` model, within the daily budget like
`suggest-prompts`; nothing on the `claude-code` provider) and print its proposals as a diff
against the set's file:

- **Rewrites** replace a prompt's `text` and keep its `id`, so its history reads on as a reword.
- **Retirements** are comments: delete the table by hand to stop asking it; the id stays retired.
- **Additions** are new `[[prompts]]` tables with ids that don't collide with any set's.

Dropped, each with its reason: rewrites or retirements of prompts the set doesn't have, rewrites
that change nothing, text that repeats a prompt in any set, has search operators, quotes or a
link, or runs over 300 characters, text naming the brand outside `head-to-heads` (or not naming
it in `head-to-heads`), and any edit `cheapseats check` would refuse once applied to a copy of the
sets. Nothing is
written: the set changes only by pull request in the config repo.
