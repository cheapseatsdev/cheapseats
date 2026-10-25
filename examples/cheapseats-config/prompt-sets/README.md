# Prompt sets

Each file holds one category of buyer questions. Change it in your config repo, run
`cheapseats check -no-jev`, then submit a pull request. Cheap Seats does not edit these files.
The flag skips optional model judgments; the config and prompt validation still run.

```toml
id = "ai-search-visibility"       # never change it: runs and rankings key on it
name = "Understanding and measuring AI visibility"
tier = "core"                   # interval: tiers.core.every_days; uses full configured samples
description = """What the category is, who is competing for it, and what the brand claims."""

[[prompts]]
id = "tools-track-often-ai-assistants"
text = "What tools track how often AI assistants like ChatGPT, Claude, Perplexity and Gemini mention a brand?"

[[prompts]]
id = "recommended-versus-mentioned-ai-answers"
text = "What's the difference between being mentioned and being recommended in an AI answer?"
intent = "info"                   # asks to explain, not to choose: scored on named, not recommended

# Products the grader folds onto one canonical name, so share of voice counts "Peec" as Peec AI.
# Competitors every set shares go once in cheapseats.toml; list one here only for this set.
[[competitors]]
name = "Peec AI"
aliases = ["Peec"]
domains = ["peec.ai"]  # pages on these sites belong to this competitor
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
| `ai-search-visibility` | Named presence while explaining awareness and measurement; one explicit tool-discovery question also measures recommendations. |
| `choosing-ai-visibility-tool` | Open tool selection and requests for alternatives, plus informational evaluation questions. |
| `fix-ai-recommendations` | Named presence while explaining diagnosis and improvement; one explicit tool-selection question also measures recommendations. |
| `head-to-heads` | Known-brand comparison and fit; naming the brand in the question prevents a discovery claim. Named presence alone does not establish factual accuracy. |
| `self-hosted-ai-visibility` | Open-source, self-hosted and DIY needs; a constrained segment, not the whole market. |

Selection/info and whether the question supplies a brand or shortlist are separate distinctions.
These descriptions do not add new TOML intent values. The `core` tier controls scheduling and
samples; it does not by itself identify a frozen benchmark.

Keep closed comparisons between other products outside this scored library. Every set still
grades the configured brand and generates fixes for it; moving such questions to a separate
set does not make their expected brand absence or missing own pages an actionable gap.
Open requests such as "What are alternatives to Profound?" remain useful discovery questions.
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
the brand. Specific budget, deployment and engine needs are legitimate even when this product
cannot meet them. Answerers receive the prompt text and `brand.audience`, not this README or
the set description, so put necessary buyer context in the question.

**Validate coverage, not a quota.** Start with 15–20 distinct buyer questions as a planning guide.
Do not pad a smaller coherent set. Report the choice/info mix: a 20-question set with 19 info
prompts has one question behind its recommendation rate. Extra samples repeat those questions;
they do not add buyer scenarios. Use the actual tier caps, available answerers and cadence when
interpreting precision. A weekly run does not deliver a worked example's seven daily runs.
See [Noise and confidence](https://cheapseats.dev/docs/noise-and-confidence/).

**Record what supports a question.** These examples are curated scenarios, not evidence of
search volume or representative buyer demand. The new open-source peer comparisons are
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
