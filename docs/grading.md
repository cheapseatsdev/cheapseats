# Grading, and the golden set

The grader (`internal/grade`) reads each collected answer with a Claude model under
`grade.System`. Its grades drive rates, ranks, trends and fix lists. Before shipping a change
to grading, measure it against the [golden set](#the-golden-set).

## What the grader decides

For one answer, for the brand on the `Brand:` line and only that brand:

- **verdict**: `recommended` (the brand is the answer's top pick or one of at most three primary
  recommendations it actively endorses), `mentioned` (the answer says something about the brand,
  but only as one option among many, in passing, as a comparison point or with reservations) or
  `absent` (not named, aliases included, or named only to say the answer can't find, verify or
  doesn't know it). The definitions are `grade.Definitions`, word for word what the grader is
  told.
- **position**: where the brand comes in the order products are first named, or null.
- **top_pick**: the one product the answer most strongly recommends, or null if it doesn't
  commit. A brand that is the top pick is always `recommended` (`results.top_pick`).
- **named**: every product named, folded onto the canonical names the set's competitors give,
  for share of voice.
- **other_product**: the brand's name was used only for some other product or company that
  shares it, told apart by `brand.about`; the verdict is then `absent`.
- **tone**, **praise**, **objections**, **why_not_top**, **reasoning**: how the answer talks
  about the brand and why it preferred what it preferred, for themes and the fix list.
- **evidence**: the one sentence of the answer that most supports the verdict, checked against
  the answer (`grade.Quote`) so it's only ever the answer's own words.

**An echoed name is not visibility.** "I couldn't find a product called Cheap Seats", followed
by evaluation advice or hypothetical features, is `absent`. It becomes `mentioned` only when
the answer states something about the brand itself—a feature, price, quality or fit, even
hedged. Before this rule, the grader split such disclaimers between `absent` and `mentioned`;
the head-to-heads set read 84% visible for a brand no assistant knew.

The grader reads the brand (name, aliases, about), the set's competitors, the question and the
answer's text (`grade.Input`). It never sees the answer's citations or searches. Two rules apply
after the model: an answer that never contains the brand's name or an alias is `absent` whatever
the model said, and an `other_product` grade is `absent`. The name check ignores case, spacing,
Markdown emphasis and curly quotes, as the evidence check does, so `**Cheap** **Seats**` and a
name broken across a line both count. Older grades missed those and could read `absent`;
regrading corrects them.

## A method change

A run's **method** (`run.Method`) covers the answer prompt, search policy, the arguments
`claude-code` and `codex` answerers are called with, grader prompt/model and the brand's name,
aliases and `about`. Runs trend together only within one method.
`run.GradeInputs` fingerprints the grading inputs, including competitors' names and aliases.
Competitor changes affect that fingerprint without themselves changing `Method`.

A method change breaks comparability with earlier runs. [Re-grading](regrading.md) can bring
stored answers onto today's grading method, retaining replaced grades, but cannot repair a
different answer prompt or search policy. Before shipping a method change:

- Pass the golden set below.
- State the method change and proposed release notes in the PR body.
- Re-grade sets with open attempts when answers were asked alike. This is a manual step, not
  an automatic migration. Attempts asked differently remain not comparable.

## The golden set

The golden set measures grading quality against labeled, stored answers; ordinary tests use
fake models. It lives in `internal/grade/testdata/golden/*.jsonl`, one file per prompt set,
one JSON record per line:

| field | what |
|---|---|
| `id` | `g-` and 12 hex digits of the SHA-256 of the answer text; the same answer always gets the same id, so a label survives re-exports |
| `set`, `prompt_id`, `prompt`, `intent` | the question as the set has it (`intent` is `info` for prompts scored on being named) |
| `brand` | `name`, `aliases`, `about`: exactly what the grader receives |
| `competitors` | the set's competitors with their aliases, as the grader folds names onto them |
| `answer` | the assistant's answer text, all the grader reads of it |
| `expected` | the scored fields: `verdict`, `top_pick` (true when the brand is the answer's top pick), `other_product`, `tone` |
| `evidence` | the quote the grader gave when the record was exported, for the person reading it; not scored |
| `labeled_by` | `claude` for a label the grader wrote, `human` once a person has checked it |
| `note` | why the label is what it is, when it needs saying |
| `source` | run, result, answerer, track, grader and method the answer came from |

Committed records use Cheap Seats itself, from `examples/cheapseats-config`;
`TestGoldenFixturesAreWellFormedAndBrandNeutral` refuses other brands. Keep deployment exports
private. The test reads this fixture directory; `CHEAPSEATS_CONFIG` selects its grader and
pricing, not another record directory. Each record supplies its own brand and competitors.

The current 80 committed labels are all model proposals (`labeled_by: "claude"`),
not an independently human-labeled benchmark. Their proposed verdicts are 74 absent,
four recommended and two mentioned. An always-absent grader reaches 74/80 (92.5%)
verdict agreement, so the aggregate gate alone cannot establish grading quality.
Inspect class coverage, errors on rare classes and label provenance before drawing
a conclusion. Historical pilot agreement below is agreement with earlier model
labels.

The unreleased [Evaluation workbench](evaluation.md) provides blind initial human
review, batch independent checks, frozen dataset versions and paired prompt
comparisons. It does not replace this test or update its fixtures automatically.

### Exporting

```sh
cheapseats backup data/snapshots/today.db                    # a consistent copy; never export from the live file
cheapseats golden export data/snapshots/today.db -n 80       # [-set head-to-heads] [-o internal/grade/testdata/golden]
```

Export rotates through sets and verdicts (`top pick`, `recommended`, `mentioned`, `other
product`, `absent`) so rare verdicts appear before common ones repeat. Sampling is deterministic
for a snapshot. Each `expected` is the **stored grader's verdict**, with `labeled_by: "claude"`:
a starting point for human review, not truth. Existing records and labels are never overwritten.

Brand and competitors come from today's config, as for a re-grade. Records whose prompt or
answer names a retired brand name absent from today's config are excluded.

### Labeling

Prefer the preview [review workflow](evaluation.md#what-to-do-next) when available:
it hides the imported proposal until you commit a judgment and keeps review history.
For manual file labeling, read the complete prompt and answer before consulting the
old label or quote, then edit `expected`:

- `verdict`: `recommended`, `mentioned` or `absent` under the definitions above.
- `top_pick`: true only for the single strongest recommendation; verdict must be `recommended`.
- `other_product`: true only for a namesake; verdict must be `absent`.
- `tone`: `positive`, `neutral`, `mixed` or `negative`; omit for an absent brand.

Set `labeled_by` to `human` only after a person has completed the judgment, even
when no label changed; explain close calls in `note`. An unresolved case needs
further review or explicit exclusion, never a guessed reference label. Leave
`id` and `answer` untouched: tests verify the answer's hash. For this small bundled
corpus, review all 80 and add fresh rare-class cases; a ten-case pilot is not a
release benchmark. A single human review is not independent human adjudication.

```sh
# the records still waiting for a person
rg --no-filename '"labeled_by":"claude"' internal/grade/testdata/golden/*.jsonl | python3 -c '
import sys, json
for l in sys.stdin:
    r = json.loads(l); print(r["id"], r["set"], r["expected"]["verdict"], "|", r["prompt"][:70])'
```

### Running

```sh
CHEAPSEATS_GOLDEN=1 go test ./internal/grade/... -run Golden -v
```

`TestGolden` is skipped unless `CHEAPSEATS_GOLDEN=1`: it calls the real grader from
`CHEAPSEATS_CONFIG` (or the sample config). It uses `ANTHROPIC_API_KEY` or
`CHEAPSEATS_ANTHROPIC_BASE_URL` from the environment, never `.env`; `claude-code` uses the
subscription. Missing credentials with the test enabled fail it.

The report identifies the grader, prompt digest, `run.Method` and estimated `[pricing]` cost
(a few cents for 80 sample-config answers). It shows field agreement, a verdict confusion
table (label rows, grader columns), and disagreements with evidence and grader reasoning.
Optional flags: `-golden.threshold 0.9` changes the gate; `-golden.set head-to-heads` selects a set.

The test **fails** if `verdict`, `top_pick` or `other_product` agreement is below
`-golden.threshold` (default **0.925**). Tone is reported, not gated. Grading varies between
calls, so perfect agreement is not required.

The default comes from pilots on **23 October 2026**, using 80 records: the old rubric agreed
with earlier verdicts on 90.0%, with every miss an echoed-name answer. Two runs of the new rule
scored 98.8% and 96.2% on verdict, 100% on top pick, 98.8% and 97.5% on other product, and 96.2%
on tone. Remaining verdict misses were echoed names still read as mentioned; the 0.925 gate
requires at least 74 agreements out of 80. After a grading change, repeat the test twice on the
same labels to inspect run-to-run disagreement before revisiting the threshold. Failed calls
can be retried; each successful grade is scored once. These pilots compare grader outputs with
earlier labels, not independent human truth.

The GitHub workflow **golden** (`.github/workflows/golden.yml`) runs this test on each release
tag and by hand, beside the [live schema check](deploying.md#the-live-schema-check). It uses
the secret `ANTHROPIC_API_KEY` (of the `golden` environment or the repository) and fails without
it. Its threshold input defaults to **0.90**, overriding the test's 0.925 default, and a tag run
uses the same default. It is separate from network-free `ci.yml` and runs in the `golden`
environment. Any reviewers required there approve before it spends, and its deployment rules
must allow the ref. Inputs reject thresholds outside 0–1 and set names that do not match
prompt-set ID syntax.
