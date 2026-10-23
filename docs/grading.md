# Grading, and the golden set

Every answer Cheap Seats collects is read once by the grader (`internal/grade`), a Claude model
under the prompt `grade.System`, and what it says is the product: the rates, ranks, trends and
fix lists are all computed from grades. This page is what the grader decides, what counts as
changing it, and how a change is measured before it ships.

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

**An echoed name is not visibility.** Questions that name the brand ("Cheap Seats or X for a
small team?") get answers that repeat the name only to disclaim it: "I couldn't find a product
called Cheap Seats", "I can't verify its pricing", then advice on how to evaluate it, or
hypotheticals about what it might offer. The brand is in the question, not in the assistant's
knowledge, so those answers are `absent`. They become `mentioned` only when the answer states
something about the brand itself, a feature, a price, a quality or a fit, even hedged. Before
this rule the grader split such answers between `absent` and `mentioned` from run to run, and
the head-to-heads set read 84% visible for a brand no assistant knew.

The grader reads the brand (name, aliases, about), the set's competitors, the question and the
answer's text (`grade.Input`). It never sees the answer's citations or searches. Two rules apply
after the model: an answer that never contains the brand's name or an alias is `absent` whatever
the model said, and an `other_product` grade is `absent`.

## A method change

A run's **method** (`run.Method`) is how its answers were asked and graded: the answer prompt,
the search policy, the grader prompt and model, and the brand as the grader knows it. Runs
trend together only within one method. The grading half of it is `run.GradeInputs`: the grader
prompt and model, the brand's name, aliases and about, and the competitors' names and aliases.
Changing any of it is a **method change**: earlier runs become non-comparable, and open fix
attempts can't be measured until their runs are [re-graded](regrading.md). So a method change

- passes the golden set below,
- says in its PR body that it is a method change and what the release notes should say,
- is followed by `cheapseats regrade` of the sets that have open attempts.

## The golden set

The golden set is labeled answers the grader is measured against: real stored answers, each with
what grading it *should* say. It's the only check that grading quality has; the rest of the
tests use a fake model. It lives in `internal/grade/testdata/golden/*.jsonl`, one file per
prompt set, one JSON record per line:

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

The committed records are about the sample brand (Cheap Seats itself, from
`examples/cheapseats-config`); `TestGoldenFixturesAreWellFormedAndBrandNeutral` refuses any
other brand, because a deployment's answers are its own. A deployment can keep its own golden
set in its config repo and point the test at it with `CHEAPSEATS_CONFIG`.

### Exporting

```sh
cheapseats backup data/snapshots/today.db                    # a consistent copy; never export from the live file
cheapseats golden export data/snapshots/today.db -n 80       # [-set head-to-heads] [-o internal/grade/testdata/golden]
```

The export samples graded answers from the snapshot, drawing one at a time from each set in
turn and within a set from each verdict in turn (`top pick`, `recommended`, `mentioned`, `other
product`, `absent`), so the rare verdicts are there before the common one is there twice. The
sample is deterministic for a given snapshot. Each record's `expected` is the **current grader's
verdict**, with `labeled_by: "claude"`: a starting point for a person to check, not a truth.
Records already in the directory are kept exactly as they are and their answers aren't written
again, so exporting again adds to the set without touching labels. The brand and competitors are
today's, from the config, as a re-grade would use them. An answer naming a name the brand has
since retired (one some run in the snapshot knew it by that today's config doesn't) is left out.

### Labeling

Open the set's `.jsonl` file, find the record (its `prompt`, `answer` and `evidence` are all
there), and edit `expected` to what the answer actually says: `"verdict"` to `recommended`,
`mentioned` or `absent`; `"top_pick"` true only when the brand is the one pick (then the verdict
is `recommended`); `"other_product"` true only when the name meant something else (then
`absent`); `"tone"` one of `positive`, `neutral`, `mixed`, `negative`, or leave it out for an
absent brand. Set `"labeled_by": "human"` whether or not you changed anything, and say why in
`note` when the call was close. Leave `id` and `answer` alone: the id is the answer's hash and
`go test` checks it. Twenty records is a useful afternoon: start with every `top pick`,
`recommended` and `other product` record, then a sample of the rest.

```sh
# the records still waiting for a person
grep -h '"labeled_by":"claude"' internal/grade/testdata/golden/*.jsonl | python3 -c '
import sys, json
for l in sys.stdin:
    r = json.loads(l); print(r["id"], r["set"], r["expected"]["verdict"], "|", r["prompt"][:70])'
```

### Running

```sh
CHEAPSEATS_GOLDEN=1 go test ./internal/grade/... -run Golden -v [-golden.threshold 0.9] [-golden.set head-to-heads]
```

`TestGolden` is skipped unless `CHEAPSEATS_GOLDEN=1`, because it grades every record with the
real grader over the network: the grader in `CHEAPSEATS_CONFIG` (else the sample config's), with
`ANTHROPIC_API_KEY` or `CHEAPSEATS_ANTHROPIC_BASE_URL` from the environment the way the app
finds them (the test doesn't read `.env`). On `provider = "claude-code"` it runs on the
subscription. With `CHEAPSEATS_GOLDEN=1` and no key it fails rather than passes. It prints, before
grading, the grader, a digest of its prompt and today's `run.Method`, so a result is attributable
to a grader version, and the estimated cost from `[pricing]` (80 records on the sample config's
grader is a few cents). Then, for each scored field, the share of records the grader agreed with
the labels on, a verdict confusion table (rows are labels, columns the grader), and each record
that disagreed with both sides' evidence and the grader's reasoning.

It **fails** when `verdict`, `top_pick` or `other_product` agrees on fewer than
`-golden.threshold` of the records (default 0.95). `tone` is reported, not gated: it's the most
subjective field. The threshold is below 1.0 because the grader is a language model and doesn't
grade the same answer the same way every time. The default comes from the first pilots on
2026-10-23: under the old rubric the grader agreed with its own earlier verdicts on 90.0% of 80
records, every miss an echoed-name answer; with the echoed-name rule, 98.8% on verdict, 100% on
top pick, 98.8% on other product (one record each way), 96.2% on tone. Re-check it the same way
after a method change (run the test twice on the same labels: the agreement between the two runs
is the noise floor, and the threshold belongs a little under it). Each record is graded
once per run; grading them several times would cost more and only sharpen a number the threshold
already allows for.

In GitHub, the manual workflow **golden** (`.github/workflows/golden.yml`, *Run workflow*) runs
the same with the repository secret `ANTHROPIC_API_KEY`; without the secret it fails with a
message, never passes. It is deliberately not part of `ci.yml`, which has no network.
