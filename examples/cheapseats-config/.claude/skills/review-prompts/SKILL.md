---
name: review-prompts
description: Review Cheap Seats buyer-question prompt sets for target-market fit, actionable learning, recurring cost and valid measurement. Use when writing, adding, rewording or auditing prompt-sets/*.toml.
---

Review whether each buyer question earns its measurement cost, then whether it measures the
intended thing. Do not answer the questions, run buyer searches, simulate assistant responses
or predict whether the brand will be recommended.

## Inputs

Read these from this repo unless the user supplies them:

- Business goal: the user's current target customers, jobs, positioning, exclusions and decisions
  they want the measurement to inform. Use product documentation and config as supporting context.
- Advisory curation context: optional [brand.advisory] positioning, goals and accepted boundaries.
  Use it for selecting questions and advising on findings, separately from public facts. Never
  copy it into answerer instructions, grader criteria or measurement outcomes.
- Declared measurement goal: the set's `description` and the prompt-sets README. Challenge these
  when they preserve questions that do not serve the business goal.
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

The user's current direction takes precedence over stale config audience, set descriptions and
previous review decisions. State a short working brief: who we want to serve, their buying job,
what the product currently does, which markets or requirements are outside scope, and which
business or messaging decisions remain open. Do not silently rewrite config to match the brief.
If context is incomplete, state reasonable assumptions and identify what would change the review.

Check whether the business goal, declared measurement goal and scoring agree. A broad category
description does not justify tracking the whole category. Do not accept "coverage" as its own goal.
Use the documentation below to check measurement definitions. Report conflicts between docs,
config and implementation; an automated intent suggestion is evidence to examine, not a verdict.

## Assess each prompt

### 1. Target buyer and decision value

Before correcting wording or intent, establish:

- **Buyer fit:** which intended customer and realistic buying or workflow decision this serves.
- **Learning:** what an answer could reveal about consideration, actual alternatives, objections,
  product understanding or language we should use. Identify what remains uncertain.
- **Action:** which concrete positioning, content, onboarding, competitive or product decision
  could change because of that learning. "Improve visibility" or "write a page" alone is not enough.
- **Quick win or suggestion:** could evidence from the answer justify a specific change to how
  the product is described, who it is for, its comparison with alternatives, an adoption obstacle,
  or the product itself? Name the evidence needed and the action it could support. If none is
  credible, say so. Do not invent an assistant answer or promise a generated fix during static review.
- **Repeat value:** why another observation is worth its cost. What changing signal needs tracking,
  and what would make us stop, reduce cadence or investigate?

Category relevance, a plausible question, an existing article, a known coverage gap, or the chance
of a brand mention does not by itself satisfy this gate. When no specific learning and action
can be identified, recommend taking the question out of recurring measurement. A hypothetical
research interest can justify a bounded experiment, not an indefinite slot in the benchmark.

Be candid with the person proposing a question. Say "This does not help position the product"
or "No actionable quick win identified" when that is the finding, and explain why. Do not soften
a rejection into "useful awareness" without a specific decision. Ask what the team could describe
more clearly or do differently after seeing an answer. An analyst's ability to generate a page
suggestion does not establish that the page should exist. A missing capability is not a copy fix;
"add a page to win" is inappropriate when the buyer needs a product the business does not offer.

Not every useful question must produce a quick win. A product or market decision may justify it
instead, but name that decision and report the lack of a content action honestly. Reject vague
possibilities that could be attached to any question; require a credible path from evidence to action.

Distinguish a loss we want to understand from a market we have chosen not to serve. Preserve hard
questions and unfavorable results for the intended buyer. A known unsupported requirement outside
that market usually belongs outside the recurring suite; repeat it only when an explicit product
or positioning decision depends on new evidence. Do not retain it merely as an honest "fit test".

For example, for a starter tool limited to Claude and OpenAI models:

- A cheap first measurement for a small startup directly tests the intended market.
- Enterprise SOC 2, agency white-label reporting or required Google/Gemini coverage do not earn
  recurring spend merely because they are legitimate needs elsewhere in the category.
- A free-tracker question can test a relevant entry point; examine free software versus free
  operation, setup effort and which alternatives are recommended before retaining or narrowing it.
- Industry pricing mechanics or reliability explanations may be useful documentation. Repeated
  named presence needs a separate business justification; accurate info scoring does not supply it.

These examples illustrate the gate, not permanent inclusion or exclusion rules for every brand.

### 2. Intent and expected answer shape

Classify intent separately from whether the question deserves tracking:

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

An informational question can be valid and still have little recurring value. Keep it when
its answers inform a stated decision, such as a live adoption objection or a positioning claim
the business wants to test. Do not assume the brand should be named in every useful explanation,
or adopt educational authority across the category as a goal without the user's direction.

Separately identify open category discovery, competitor-aware alternatives, closed named
shortlists and known-brand evaluation. "A vs B" can be useful competitor research, but
when both options exclude the tracked brand, keep it outside the scored prompt library.
Every set still grades that brand and generates fixes for it; another set does not make
its expected absence or missing own pages actionable. Open "alternatives to A" questions
can discover other products and remain valid. A comparison including the tracked brand
can measure known-brand fit. These are review labels, not new TOML intent values.

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
Assess capability gaps against the intended buyer and the decision-value gate. A genuine buyer
question elsewhere in the category is not automatically a useful question for this business.
An unfavorable answer within the intended market is not evidence of a bad question.

### 6. Contribution to the set

Flag semantic duplicates, overrepresented needs, and missing buying situations.
Include alternatives the target buyer might actually choose: manual checks, a spreadsheet,
scripts, an existing subscription or doing nothing yet, as relevant. Do not let the configured
competitor list substitute for understanding the buyer's alternatives or validate demand for
every named comparison. Feature availability alone does not warrant a prompt for each feature.
Do not treat rewordings as independent coverage.
Treat wording overlap as a duplicate candidate, not proof. Check whether different constraints
change the buying decision; use supplied stored-answer evidence when available.
Apply the same measurement review to suggestions from assistant search queries: a frequent
closed comparison between other brands is not thereby a suitable scored buyer question.

## Assess the set and sampling

Recommend an allocation of measurement effort against the business goal, not just a coverage
inventory. Separate recurring trend questions, bounded experiments and unscheduled research.
For experiments, state the learning hypothesis, proposed run cap or review date, and what would
promote, stop or change the experiment. For recurring questions, identify what ongoing decision
needs the trend. Prefer a smaller useful suite to a broad suite whose expected absences are known.

These are review dispositions, not TOML fields or scheduling features. A set still grades the
brand and generates fixes; moving an out-of-market question to another loaded set does not
remove its spend or make the resulting fixes useful. Unscheduled research must remain outside
the active loader or be handled separately. Do not claim such changes were applied during review.

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

When recommending cuts or cadence changes, compare configured answer volume using the actual
samples, tier caps, answerers and intervals where available. Distinguish answer calls from web
searches, grading, analysis and optional passes; fewer prompts alone does not specify token or
dollar savings. Existing content is not a reason to preserve recurring calls after learning stops.

Recommend a stable core for trend measurement alongside exploratory questions when useful.
Review flat or noisy prompts in context: always-absent questions may expose important gaps,
and ceiling questions can detect regressions. Flags warrant investigation, not automatic
retirement or harder wording. Distinguish static design concerns from observed findings;
do not claim noise, duplicates or precision were measured without supporting run data.

## Return

Lead with the working business brief and any conflict with the existing library. For each prompt,
by id, provide compact fields or a table:

- Decision: keep, keep as info, revise, move to another measurement goal,
  or remove.
- Intent: selection / info; known-brand: yes/no.
- Measurement allocation: recurring, bounded experiment, unscheduled research, or retire.
- Main reason: one specific sentence.
- Buyer and learning: the target situation, unresolved question, what it can and cannot tell us,
  and any intent ambiguity.
- Decision and repeat value: what the business could change, why another observation helps,
  and the proposed cadence or experiment stopping/review condition. Say "none identified" when so.
- Expected quick win or suggestion: the specific action and evidence trigger, or "none identified".
  Distinguish a positioning/content action from a product or market decision and from an actual
  generated recommendation; static review has not verified that the analyst will produce it.
- Suggested revision: only when it improves fit without changing the buyer need.
- Meaning changed: yes/no; explain if the revision asks a different question.

Report the set's selection/info mix, three most consequential weaknesses and targeted changes.
Show which questions earn the recurring budget and which stop consuming it, with reasons that
do not depend on whether the brand currently wins. Prioritize additions by learning value;
do not replace every retired question or preserve the old total.
State evidence limits and distinguish corrections to scoring from proposed new buyer needs.
Discuss counts and sampling only to the extent supported by the supplied configuration or data.

## Rules

- Judge target-market relevance, decision value and measurement quality, not the likelihood of
  a favorable brand result. Preserve relevant losses; remove irrelevant spend.
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
- Preserve useful questions that expose genuine weaknesses for the intended buyer. Do not keep
  known out-of-market exclusions without an unresolved decision or rewrite them merely to win.
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
