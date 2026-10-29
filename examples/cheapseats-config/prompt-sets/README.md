# Prompt sets

The active library has **11 questions in two sets**, focused on small teams and startups
getting started. The eight unbranded questions run weekly; the three known-brand questions
run every 28 days. Every retained question names a learning and a possible action in comments.
Review the core after eight runs and fit checks after three; review dates do not stop the scheduler.
Stop or reduce questions whose answers no longer change a decision, while preserving relevant losses.

Nine existing questions retain their text and IDs. Two new selection questions cover starter-tool
choice and replacement of manual spreadsheet checks. The cheap-start question is now `info`:
a manual method is a complete answer, so its named presence remains useful without treating every
product-free explanation as a lost recommendation. With both configured answerers available,
weekly core runs plan 48 answers and fit runs plan 12 every 28 days: 51 answers per week averaged
over four weeks, plus grading and other passes. This is configured volume, not measured usage or cost.

Thirteen parked questions, including the event-driven cost audit, live in
[`../research-prompts/`](../research-prompts/README.md) outside the active loader. They have no
standing run budget. The [retirement registry](retired-ids.txt) reserves the 56 cut IDs and four
previously retired IDs. Do not repurpose historical IDs. The audience and product description now
reflect the starter market, so this refresh starts a new method baseline; earlier rates are not
comparable. Prompt moves preserve the original IDs and stored runs retain their original sets.

Each file holds one category of buyer questions. Change it in your config repo, run
`cheapseats check -no-jev`, then submit a pull request. Cheap Seats does not edit these files.
The flag skips optional model judgments; the config and prompt validation still run.

```toml
id = "choosing-ai-visibility-tool" # never change it: stored runs key on it
name = "Getting started with AI visibility"
tier = "core"                    # weekly; uses full configured samples
description = """Small teams deciding how to make their first useful checks."""

[[prompts]]
id = "starter-ai-visibility-tools-small-startup"
text = "Which AI visibility tools are a good starting point for a small startup with a limited budget?"

[[prompts]]
id = "cheap-start-measuring-ai-visibility"
text = "What's a cheap way to start measuring AI visibility for a small startup?"
intent = "info"                  # a complete manual method can satisfy the question

# Competitors every set shares go in cheapseats.toml; add one here only for this set.
[[competitors]]
name = "Peec AI"
aliases = ["Peec"]
domains = ["peec.ai"]
```

`core` and `plg` intervals come from `[tiers.core]` and `[tiers.plg]`; PLG samples are capped by
`tiers.plg.max_samples` (default 1). Scheduling also needs `[schedule]` configured.

**Prompt ids are permanent.** Results, trends and fix lists identify questions by id:

- **Same id, same question.** Fixing a typo or rewording keeps the id: the prompt's history
  carries on, marked as reworded that day. Asking something different gets a new id.
- **Ids are unique across every set** and never reused. Keep the id when moving a question to
  another set; stored runs retain their original set and prompt ids. Removed ids stay retired.
- Order in the file is only display order.

**Set intent by what a good answer requires.** Selection questions ("what should I use for...",
"X vs Y") score recommendations and top picks. An explanation ("what is...", or a how-to that
needs no product) gets `intent = "info"`: it counts toward named only, not recommended or top-pick
rates. Citations do not make the brand named; the grader reads answer text, not citations.
Judge the answer needed, not the opening words: a how-to is not selection just because a
visibility tool can do the job. A complete manual or API workflow can satisfy an info question.
Don't turn an explanation into "recommend a tool" just to score it; that is another question.
Intent changes keep history and are annotated like rewording; comparisons exclude that prompt
across the change. Use [prompt review](https://cheapseats.dev/docs/prompt-review/)
to check wording and coverage.

**Keep measurement goals clear.** The shipped sets separate these situations:

| set | what it measures |
| --- | --- |
| `choosing-ai-visibility-tool` | Eight unbranded starter questions: seven selection and one informational. Affordable entry, manual-work replacement, DIY, open-source, practical setup, evidence and useful fixes. Weekly core tier. |
| `head-to-heads` | Three known-brand fit questions: two selection and one informational. Small-team alternatives and operating objections, every 28 days. Named presence alone does not establish accuracy or independent discovery. |

Selection/info and whether the question supplies a brand or shortlist are separate distinctions.
These descriptions do not add new TOML intent values. The `core` tier controls scheduling and
samples; it does not by itself identify a frozen benchmark.

Keep closed comparisons between other products outside this scored library. Every set still
grades the configured brand and generates fixes for it; moving such questions to a separate
set does not make their expected brand absence or missing own pages an actionable gap.
Open requests for alternatives can measure discovery when they serve the target buyer and
an unresolved decision; the Profound-alternatives question is currently parked for research.
The closed-comparison ids `peec-ai-vs-profound` and `otterly-vs-peec-ai-budget` are retired;
do not reuse them. Stored runs and their original ids remain in the database.

**Set edits do not start a new method.** Rankings annotate added, removed and reworded prompts,
intent changes, competitors and answerers. Day-over-day comparisons use only shared prompts,
worded the same, and shared answerers. A set's name, description or tier does not change the
method. A new method starts when answer or search instructions, grader prompt/model, or brand
name/aliases/`about` change; audience is part of the answer prompt. The full list:
[what restarts a trend](https://cheapseats.dev/docs/prompt-sets/#what-restarts-a-trend).

**Ask the way a buyer would.** Only `head-to-heads` names the brand; `cheapseats check` enforces
this with whole-word name and alias matches. Elsewhere, ask as someone who has never heard of
the brand. Specific budget, deployment and engine needs can be legitimate even when this product
cannot meet them. Keep a hard question when it serves the intended buyer and an unresolved
decision; a known exclusion outside the target market does not earn recurring spend just because
it is a real category need. Answerers receive the prompt text and `brand.audience`, not this
README or the set description, so put necessary buyer context in the question.

**Make every recurring question earn its cost.** Use the user's current target market and goals
to challenge the existing set descriptions. For each question, name the buyer, what remains to
learn, what business or messaging decision the answer could change, and why another observation
helps. State whether answer evidence could support a specific positioning change, quick win or
suggestion; say when no useful action is identifiable. A missing capability is not a copy fix,
and an analyst-generated page suggestion does not establish that the page should exist.
Correct `info` scoring, category relevance or an existing article is not enough. Keep
relevant competitive losses. Move questions with no actionable learning out of automatic runs;
use bounded experiments with a review date or run cap for uncertain needs. Another loaded set
still consumes calls and generates fixes. The [review skill](../.claude/skills/review-prompts/SKILL.md)
applies these checks before wording and intent corrections.

**Validate coverage, not a quota.** Start with 15–20 distinct buyer questions as a planning guide.
Do not pad a smaller coherent set. Report the choice/info mix: a 20-question set with 19 info
prompts has one question behind its recommendation rate. Extra samples repeat those questions;
they do not add buyer scenarios. Use the actual tier caps, available answerers and cadence when
interpreting precision. A weekly run does not deliver a worked example's seven daily runs.
See [Noise and confidence](https://cheapseats.dev/docs/noise-and-confidence/).

**Record what supports a question.** These examples are curated scenarios, not evidence of
search volume or representative buyer demand. The parked research questions are
exploratory hypotheses. Before expanding or weighting a set, use actual customer questions,
support, sales or public query evidence. Source notes can be TOML comments; unrecognized fields
are errors. Writing a page for a prompt does not independently validate demand for it.

**Keep a stable benchmark and explore deliberately.** Review monthly, using recorded answers
where available. Always-absent questions can preserve important gaps; ceiling questions can
detect regressions. Similar wording alone does not prove redundancy. Investigate flags and
explain retirements instead of changing questions until the brand wins. Keep stable questions
alongside clearly identified exploratory additions and inspect `cheapseats plan` before merging.

The older template-only ids `developer-tool-ai-recommends-it` and
`engineering-team-measure-llm-recommendations` were already retired from the canonical sample.
They remain retired here; do not reuse them. Existing deployments that keep those questions
should treat them as informational methods questions, not product selection.

Convert legacy `prompts = ["...", "..."]` files with `cheapseats migrate-config <dir>`.
It derives ids from prompt words; review them before committing.

Prompts are sent to every answerer's provider on each run. Don't put anything in them that isn't public.
