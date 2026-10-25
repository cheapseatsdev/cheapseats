---
name: review-prompts
description: Review Cheap Seats prompt sets as measurement instruments - goal fit, intent (selection vs info), buyer realism, neutrality, interpretability, and coverage. Use when writing, adding, rewording, or auditing prompts in prompt-sets/*.toml.
---

Review buyer questions as measurement instruments. Do not answer them, run searches,
simulate assistant responses or predict whether the brand will be recommended.

## Inputs

Read these from this repo unless the user supplies them:

- Measurement goal: the set's `description` and the prompt-sets README.
- How answers will be scored: Cheap Seats scores selection prompts on
  recommended / top pick, and prompts tagged `intent = "info"` on the named
  rate. Citations are separate evidence; the grader reads answer text only.
- Target audience: `brand.audience` in cheapseats.toml; buying stage from the set.
- Brand/category context: `[brand]` in cheapseats.toml (name, about, aliases)
  and the set's competitors.
- Prompt set: the `[[prompts]]` in prompt-sets/*.toml.

Check whether the goal and scoring agree. State a working interpretation if the goal is unclear.

## Assess each prompt

### 1. Goal fit

What evidence could a natural answer provide toward the goal?

### 2. Intent and expected answer shape

Classify the intent first:

- selection: the buyer wants something chosen or ranked for them ("what
  should I use for...", "best X for...", "X vs Y for..."). A good answer
  names products, so absence can indicate lost consideration or a capability mismatch.
- info: the buyer wants to understand or do something ("how does...",
  "how do I...", "what is..."). A good answer may name no product. Its named rate
  measures substantive brand presence while the topic is explained.
- known-brand: the question names the brand, so it cannot measure unprompted
  discovery. Use it for accuracy, sentiment or comparative fit; move it to another
  goal unless that is intended. An answer that only echoes the name to disclaim
  knowledge is still absent.

Give the answer shape: product discovery, comparison, explanation, implementation
or another specific type. Do not generate an answer. Known-brand is a review label,
not a valid `intent` value; classify selection/info separately when editing TOML.
Only `head-to-heads` may name the brand; `cheapseats check` enforces this.

### 3. Buyer realism

Is the wording plausible for this audience and buying stage?
Flag missing context only when it materially changes the buying decision.

### 4. Neutrality

Flag brand hints, leading premises, and combinations of requirements that
appear tailored to the brand.
Specific requirements are valid when grounded in a real buyer need.

### 5. Interpretability

Does scoring match intent? An info prompt scored on "recommended" counts correct,
product-free answers as losses unrelated to brand consideration.
Could a strong answer omit all products?
Would the brand's absence indicate lost consideration, a capability mismatch,
or simply that the question did not call for a product?

### 6. Contribution to the set

Flag semantic duplicates, overrepresented needs, and missing buying situations.
Do not treat rewordings as independent coverage.

## Return

For each prompt, by id:

- Decision: keep, keep as info, revise, move to another measurement goal,
  or remove.
- Intent: selection / info; known-brand: yes/no.
- Main reason: one specific sentence.
- Measurement value: what this prompt can and cannot tell us.
- Suggested revision: only when it improves fit without changing the buyer need.
- Meaning changed: yes/no; explain if the revision asks a different question.

Report the set's selection/info mix, three most consequential weaknesses and targeted changes.

## Rules

- Judge measurement quality, not the likelihood of a favorable brand result.
- Do not turn an info question into a selection question by adding
  "recommend a tool". Tag it `intent = "info"` for the named rate. Reword it
  only if the real buyer question asks for a selection.
- Ids are permanent and carry the trend. A revision that keeps the buyer need
  keeps its id (the day is marked as reworded); a revision that asks a
  different question needs a new id. Say which for each suggested revision.
- Changing a prompt's intent keeps its id, but changes which rates it contributes to:
  info answers are excluded from recommended and top-pick rates. Recommend it
  when the scoring was wrong, not to improve a number.
- A set that is mostly info prompts cannot support a "share of
  recommendations" claim; say so.
- Preserve useful questions that expose genuine product weaknesses.
- Distinguish wording problems from goal/scoring mismatches.
- Do not invent buyer research or claim wording represents real demand.
- Treat brand claims as context, not independently verified facts.
- Prefer concrete findings over a numerical "goodness" score.
- Review only. Edit prompt files only when asked, and then run
  `cheapseats check` and `cheapseats plan` to show the effect.
