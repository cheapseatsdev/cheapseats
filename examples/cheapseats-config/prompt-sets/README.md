# Prompt sets

Each file holds one category of buyer questions. Change it in your config repo, run
`cheapseats check`, then submit a pull request. Cheap Seats does not edit these files.

```toml
id = "ai-search-visibility"       # never change it: runs and rankings key on it
name = "Finding out whether AI assistants recommend us"
tier = "core"                   # interval: tiers.core.every_days; uses full configured samples
description = """What the category is, who is competing for it, and what the brand claims."""

[[prompts]]
id = "find-whether-chatgpt-recommends-product"
text = "How can I find out whether ChatGPT recommends my product when people ask about my category?"

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
Don't turn an explanation into "recommend a tool" just to score it; that is another question.
Intent changes keep history and are annotated like rewording. Use [prompt review](../../../docs/prompt-review.md)
to check wording and coverage.

**Set edits do not start a new method.** Rankings annotate added, removed and reworded prompts,
intent changes, competitors and answerers. Day-over-day comparisons use only shared prompts,
worded the same, and shared answerers. A set's name, description or tier does not change the
method. A new method starts when answer or search instructions, grader prompt/model, or brand
name/aliases/`about` change; audience is part of the answer prompt. The full list:
[what restarts a trend](https://cheapseats.dev/docs/prompt-sets/#what-restarts-a-trend).

**Ask the way a buyer would.** Only `head-to-heads` names the brand; `cheapseats check` enforces
this with whole-word name and alias matches. Elsewhere, ask as someone who has never heard of
the brand. Start with 15–20 useful prompts per set; this is a planning guide, not a precision
guarantee. Read the rates' intervals.

Convert legacy `prompts = ["...", "..."]` files with `cheapseats migrate-config <dir>`.
It derives ids from prompt words; review them before committing.

Prompts are sent to every answerer's provider on each run. Don't put anything in them that isn't public.
