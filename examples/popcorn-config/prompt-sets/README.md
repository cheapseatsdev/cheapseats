# Prompt sets

Each file is one category of buyer questions. Popcorn only reads them, so change one with a pull
request, after `popcorn check`.

```toml
id = "ai-search-visibility"       # never change it: runs and rankings key on it
name = "Finding out whether AI assistants recommend us"
tier = "core"                     # core: daily, full samples. plg: every tiers.plg.every_days, one sample
description = """What the category is, who is competing for it, and what the brand claims."""

[[prompts]]
id = "find-whether-chatgpt-recommends-product"
text = "How can I find out whether ChatGPT recommends my product when people ask about my category?"

# Products the grader folds onto one canonical name, so share of voice counts "Peec" as Peec AI.
[[competitors]]
name = "Peec AI"
aliases = ["Peec"]
```

**Prompt ids are permanent.** Results, trends and fix lists follow a prompt by its id, so:

- **Same id, same question.** Fixing a typo or rewording keeps the id: the prompt's history
  carries on, marked as reworded that day. Asking something different gets a new id.
- **Ids are unique across every set** and never reused. Moving a prompt to another set keeps its
  id, and its history moves with it. Once removed, an id stays retired.
- Order in the file is only display order.

**Changing a set doesn't restart its trend.** Rankings carry on through added, removed and
reworded prompts, new competitors and new answerer models, and each day says what changed since
the day before. Comparisons with the day before only count the prompts both days asked, worded the
same. Editing a set's name, description or tier changes nothing. A new trend starts only when how
answers are asked or graded changes: the brand's name, aliases, about or audience, the grader, or a
Popcorn release that changes its prompts. The full list:
[what restarts a trend](https://popcorn.zone/docs/prompt-sets/#what-restarts-a-trend).

**Ask the way a buyer would.** Only `head-to-heads` names the brand; in every other set it has to be
named on its own merits (`popcorn check` enforces this, matching the brand name and aliases as whole
words). Write each question the way someone who has never heard of the brand would type it, and keep
each set at 15–20 prompts so a rate means something.

Sets written before prompts had ids (`prompts = ["...", "..."]`) are converted by
`popcorn migrate-config <dir>`, which makes an id from each prompt's words; review them before
committing.

Prompts are sent to every answerer's provider on each run. Don't put anything in them that isn't public.
