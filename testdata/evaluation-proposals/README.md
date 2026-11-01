# Evaluation challenge cases

These synthetic cases are starting points for human review. They are not trusted
answers, a measured improvement, or additional golden fixtures. Codex generated
every case and its first proposed label. A maintainer reviewed all of them on
2026-10-31, with the proposals visible: 33 proposals were accepted unchanged and
the three open grader cases were decided (see "Maintainer decisions"). Because
the review was not blind, treat these as development cases, not a held-out set. Names,
features, page contents and `.test` domains are fictional.

| File | Purpose | Current status |
| --- | --- | --- |
| `grader-challenges.json` | 22 examples at grading boundaries | Human labels (`human_expected`) |
| `advice-challenges.json` | 11 examples of useful or unsupported Next steps | Human dispositions (`human_disposition`); no benchmark scorer implemented |
| `candidate-instructions/` | Two instruction hypotheses to test separately | Unvalidated appendices to the production grader prompt |

## Review before measuring

1. Review the complete question, answer, intended brand and current rubric before
   revealing the proposed decision. Record a human judgment or leave the case
   unresolved with a specific reason. Review advice against all supplied artifacts,
   including read errors, excerpts and the advisory.
2. Resolve disagreements explicitly. Confirm that each quote supports the actual
   claim, not merely that its text occurs in the source. Do not infer an absence
   from an unread or incomplete source. The four scored grader fields are verdict,
   top pick, namesake-only and tone. Other annotations describe failure modes;
   they do not silently add scored criteria.
3. Keep related examples together using `family`, in addition to the workbench's
   existing question/answer grouping. These cases and instruction hypotheses have
   already been inspected together: use them for development only. A lexical
   grouping guard cannot enforce semantic family independence.
4. Collect a separate, representative set of naturally occurring saved answers.
   Review and group it, then freeze fresh held-out membership before tuning.
   Challenge-case performance alone does not estimate production accuracy. Ensure
   enough reviewed examples of each relevant class before interpreting its rates.
5. Compare the fixed production control with one complete candidate prompt, using
   repeated assessments, the same model and a fixed admission budget. Inspect
   regressions, class coverage, quote failures and repeat variation. A passing
   review gate is a prerequisite for a human decision, not automatic deployment
   or proof of statistical reliability.

The challenge JSON is **not accepted by the current golden JSONL importer**. Do not
rename it to `.jsonl` or copy these labels into `labeled_by: human` by hand. An
explicit conversion must preserve the original evidence and real
review provenance through the workbench's existing record contract. A separate
review/import contract is still needed for advice artifacts and gate decisions.

No production prompt, saved grade or existing golden label changes here. The
workbench workflow and comparison commands are documented in
[Evaluation](../../docs/evaluation.md).

## What the grader cases isolate

The examples distinguish ignorance from a hedged feature claim, the intended
brand from a namesake, endorsement from tone, three primary choices from the
unsettled four-choice boundary, and a preliminary favorite from the final choice.
Formatting variants expose the difference between a model's interpretation and
lexical postprocessing. A prompt edit cannot be assumed to repair a code guard.

`evidence` contains human-review support, so it can retain multiple literal spans
or an echo/namesake passage when the verdict is absent. The production
grader's single `evidence` field remains empty for absence under its current
contract. No-reference cases correctly have no supporting span: the reviewer must
read the complete answer. Separate retrieval URLs are annotations, outside the
answer being graded.

### Maintainer decisions

`grader-011`, `grader-017` and `grader-019` were decided by a maintainer on
2026-10-31. Each records the decided rule in `human_adjudication`:

- `grader-011`: four equal endorsements exceed the small-set limit of three, so the
  brand is mentioned with a positive tone, not recommended.
- `grader-017`: a configured domain alias identifies the intended brand, so an
  endorsement through the bare domain is a recommendation and here a top pick.
- `grader-019`: a cut-off answer is graded on the text present. Favourable
  description followed by a truncated caveat is a mixed mention.

These rules are not yet in the production rubric.

## What the advice cases isolate

The decisions use the verifier's six existing gates: `existing_work`,
`remaining_change`, `capability`, `strategy`, `eligibility` and `completion`.
`gates_to_examine` identifies likely problem areas; it is not a complete gold
checklist. Each passing gate still needs an exact source, quote and explanation.
The positive owned-page example supplies sources for all six gates.
Page body hashes pin the synthetic snapshots; hashes do not establish truth.

`advice-001` needs investigation: the complete page gives init and serve commands,
but no installation command. Complete coverage does not mean every requested
step is present. Its disposition is not a verified action or an
already-done result.

The cases exercise missing installation instructions, intentional boundaries, failed reads,
irrelevant proof, contradictory tails, missing content inventories, publisher
control and team-controlled completion. The excerpt case intentionally leaves its
tail unknown. Rejected actions become investigations only when a bounded check
could change a useful decision. These are cases for a future advice benchmark,
not evidence that the current verifier passes it.

## Try the instruction hypotheses

In **Create grader comparison**, paste the complete current production grader
prompt followed by exactly one appendix. Keep the production control fixed. The
text files are not standalone prompts and contain no adjudicated few-shot labels.
Test each hypothesis separately before considering a combination.

`decision-order.txt` makes the existing decision sequence explicit.
`whole-answer-check.txt` asks for a final consistency and evidence check. Neither
changes the rubric or output schema intentionally; a human still needs to confirm
that the wording preserves their meaning. No provider comparison was run for
these cases. Format, provenance, literal quotes and snapshot hashes were
checked offline; that validation cannot establish label correctness.
