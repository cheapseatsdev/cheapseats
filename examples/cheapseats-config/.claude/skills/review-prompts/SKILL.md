---
name: review-prompts
description: Review Cheap Seats buyer-question prompt sets for valid measurement - intent, buyer realism, neutrality, coverage and interpretation. Use when writing, adding, rewording or auditing prompt-sets/*.toml.
---

Review buyer questions as measurement instruments. Do not answer them, run buyer searches,
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
- Other sets: nearby questions, buying situations and competitor comparisons already covered.
- When assessing precision: answerers, samples per track, tier caps, schedule and any supplied
  completed-run counts. Keep planned counts separate from observed counts.
- Any supplied evidence of buyer demand, such as customer questions or query logs.

Check whether the goal and scoring agree. State a working interpretation if the goal is unclear.
Use the documentation below to check measurement definitions. Report conflicts between docs,
config and implementation; an automated intent suggestion is evidence to examine, not a verdict.

## Assess each prompt

### 1. Goal fit

What evidence could a natural answer provide toward the goal?

### 2. Intent and expected answer shape

Classify the intent first:

- selection: the buyer wants products chosen, ranked or compared ("what
  should I use for...", "best X for...", "X vs Y for..."). Product selection
  is part of satisfying the request, rather than an optional example.
- info: the buyer wants to understand or do something ("how does...",
  "how do I...", "what is..."). A good answer may name no product. Its named rate
  measures substantive brand presence while the topic is explained.
- known-brand: the question names the brand, so it cannot measure unprompted
  discovery. Use it for accuracy, sentiment or comparative fit; move it to another
  goal unless that is intended. An answer that only echoes the name to disclaim
  knowledge is still absent. Named rate alone does not establish factual accuracy.

Give the answer shape: product discovery, comparison, explanation, implementation
or another specific type. Do not generate an answer. Known-brand is a review label,
not a valid `intent` value; classify selection/info separately when editing TOML.
Only `head-to-heads` may name the brand; `cheapseats check` enforces this.

Judge what a complete, useful answer requires, not the question's opening words.
A how-to is not selection solely because the brand does that job. Conversely, a how-to
can ask for tool selection. Explain genuinely ambiguous cases instead of automatically
tagging every how-to info or treating every category task as selection. For example,
"What should I compare when evaluating these tools?" asks for criteria; "Which tool
should I choose for this task?" asks for selection.

Separately identify open category discovery, competitor-aware alternatives, closed named
shortlists and known-brand evaluation. "A vs B" can be useful competitor research, but
omitting a third brand does not establish failed open discovery. These are review labels,
not new TOML intent values. Recommend separate interpretation or grouping when needed.

### 3. Buyer realism

Is the wording plausible for this audience and buying stage?
Flag missing context only when it materially changes the buying decision.
Check technical, budget and operating-capacity assumptions against the stated audience.
Answerers receive the question and `brand.audience`, not the set description or brand facts;
put necessary buyer context in the question rather than relying on that metadata.
Distinguish supplied evidence of demand from plausible but unvalidated scenarios. Pages
written to answer a prompt do not independently establish that buyers ask it.

### 4. Neutrality

Flag brand hints, leading premises, and combinations of requirements that
appear tailored to the brand.
Specific requirements are valid when grounded in a real buyer need.
Open-source, self-hosting, budget or API requirements are not inherently biased. Check their
prevalence and weighting before describing niche results as representative of the market.
Passing the brand-name check alone does not establish neutrality.

### 5. Interpretability

Does scoring match intent? An info prompt scored on "recommended" counts correct,
product-free answers as losses unrelated to brand consideration.
Could a strong answer omit all products?
Would the brand's absence indicate lost consideration, a capability mismatch,
or simply that the question did not call for a product?
Keep needs for unsupported engines, agency workflows or other product limitations when
they are genuine buyer questions. An unfavorable answer is not evidence of a bad question.

### 6. Contribution to the set

Flag semantic duplicates, overrepresented needs, and missing buying situations.
Do not treat rewordings as independent coverage.
Treat wording overlap as a duplicate candidate, not proof. Check whether different constraints
change the buying decision; use supplied stored-answer evidence when available.

## Assess the set and sampling

Report total distinct questions and selection/info counts separately. Recommendations and
top picks use the selection subset; a mostly informational set can still report that subset's
rate, but its full question count does not describe the recommendation evidence.

The docs' 15–20 distinct buyer questions are a planning guide, not a quota or precision guarantee.
Do not pad smaller coherent sets or retire useful questions just to reach that range.
Separate question breadth, repeated samples and runs over time: extra answers to one question
do not add buyer scenarios, and same-run repetition cannot remove between-run drift.

When discussing precision, use actual answerer availability, track, tier sample caps, cadence,
comparison window and eligible counts. Keep search and recall separate. Distinguish uncertainty
on a reading from uncertainty on a change; do not transfer a worked example's margins to a
different schedule or claim that more samples establish representative demand or causation.
With one sample per answerer and track, automated review cannot separate drift and disagreement;
the absence of those flags does not establish stability.

Recommend a stable core for trend measurement alongside exploratory questions when useful.
Review flat or noisy prompts in context: always-absent questions may expose important gaps,
and ceiling questions can detect regressions. Flags warrant investigation, not automatic
retirement or harder wording. Distinguish static design concerns from observed findings;
do not claim noise, duplicates or precision were measured without supporting run data.

## Return

For each prompt, by id:

- Decision: keep, keep as info, revise, move to another measurement goal,
  or remove.
- Intent: selection / info; known-brand: yes/no.
- Main reason: one specific sentence.
- Measurement value: buying situation/cohort, what it can and cannot tell us, and any intent ambiguity.
- Suggested revision: only when it improves fit without changing the buyer need.
- Meaning changed: yes/no; explain if the revision asks a different question.

Report the set's selection/info mix, three most consequential weaknesses and targeted changes.
State evidence limits and distinguish corrections to scoring from proposed new buyer needs.
Discuss counts and sampling only to the extent supported by the supplied configuration or data.

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
  when the scoring was wrong, not to improve a number. Intent changes are annotated;
  comparisons must exclude that prompt across the change.
- Preserve useful questions that expose genuine product weaknesses.
- Distinguish wording problems from goal/scoring mismatches.
- Do not invent buyer research or claim wording represents real demand.
- Treat brand claims as context, not independently verified facts.
- Prefer concrete findings over a numerical "goodness" score.
- Review only. Edit prompt files only when asked, and then run
  `cheapseats check` and `cheapseats plan` to show the effect. Use `check -no-jev`
  for validation without optional provider calls. Static review needs no additional provider calls.

## Measurement references

Consult the relevant current docs when definitions or limits affect a finding; use local
copies when available. Keep review recommendations distinct from existing product behavior.

- [Prompt sets](https://cheapseats.dev/docs/prompt-sets/): intent, permanent ids and comparable changes.
- [Metric definitions](https://cheapseats.dev/docs/metrics/): denominators, branded prompts and citation evidence.
- [Noise and confidence](https://cheapseats.dev/docs/noise-and-confidence/): prompts versus samples, cadence and uncertainty.
- [Prompt review](https://cheapseats.dev/docs/prompt-review/): empirical flags, data requirements and their limits.
- [Benchmarking](https://cheapseats.dev/guides/benchmark-against-competitors/): open questions versus supplied brand names.
