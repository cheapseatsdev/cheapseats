# Evaluation

Evaluation keeps saved answer evidence, imported model labels and explicit human
decisions separate from production runs. Importing records and reviewing them
does not contact a model or change historical grades.

## Try the review workflow locally

From the app checkout, use a separate test database and the example config,
whose daily schedule is disabled:

```sh
CHEAPSEATS_EVALUATION=1 \
CHEAPSEATS_CONFIG="$PWD/examples/cheapseats-config/cheapseats.toml" \
CHEAPSEATS_DATA_DIR="$PWD/data/evaluation" \
go run ./cmd/cheapseats serve -addr 127.0.0.1:3000
```

Open `http://127.0.0.1:3000/evaluation`, import the saved review records and start
a short session. Try saving a judgment, leaving a case unresolved, and ending and
resuming a draft. Then return to the queue to preview and approve a dataset
version. Give an exclusion reason for every case you have not finished reviewing.
The version page downloads the approved JSONL and full manifest. This workflow
needs no provider keys and makes no model calls. Use this separate database for testing; it does not change your normal dashboard's
data. Reuse the same directory when restarting so your saved reviews remain
available. If you already reviewed cases in a temporary directory, keep that
directory or use the original build's backup command to preserve it before moving
the database; changing directories does not transfer reviews automatically.

## Import saved evidence

```sh
cheapseats evaluation import                   # the bundled public golden fixtures
cheapseats evaluation import records.jsonl     # a private JSONL export
```

The import uses the same record contract as [the golden set](grading.md). Each
nonblank row remains accounted for: invalid JSON or an invalid record is retained
with its line number and repair reason. Repeating the same source and contents
is idempotent. Duplicate evidence does not add another case. Identical answer
text under different questions or brand contexts remains separate evidence;
the answer-only golden ID is preserved but is not the case's database key.

Imports accept UTF-8 JSONL up to 16 MiB and 10,000 nonblank rows. Exceeding a bound
rejects the whole batch. Existing snapshots are never overwritten by later model
proposals. Importing a label marked `human` does not create a human review event.

## Decisions and provenance

A draft is saved work, not a reviewed answer. A committed judgment records the
reviewer, the rubric digest, supporting spans and a monotonically increasing
revision. An unresolved decision retains its issue and explanation without
inventing a verdict. Earlier decisions remain in history. A stale revision from
another tab is rejected rather than overwriting its saved work.

Visible brands need a verdict, tone and supporting passage; a recommended brand
also needs an explicit top-pick decision. Absence distinguishes no qualifying
reference, an echoed name and a namesake-only answer. No-reference absence needs
whole-answer confirmation; echo and namesake decisions need a supporting passage.
Evidence matching restores the original source span after harmless formatting
differences. It verifies provenance, not whether the passage logically supports
the reviewer's interpretation. Known incomplete answers remain unresolved.

## Review in the dashboard

Enable the internal workbench with `CHEAPSEATS_EVALUATION=1`, then open
**Checks → Evaluation**. It is off by default and unavailable in read-only mode.
The existing token or trusted sign-in proxy authenticates all routes; an open
loopback instance trusts every local user, as the rest of the app does. Review
sessions are scoped to that authenticated reviewer (or the local reviewer).

Import the bundled records, then start a session of up to ten cases. Each case
shows the complete saved answer, its buyer question, intended brand identity and
grading rules. The imported model verdict, supporting quote and note are omitted
from the first-review response entirely. They become available for comparison
only after an explicit human judgment. A draft never unlocks that comparison.

Hover over a field for a short explanation, or open its help disclosure with a
click, tap or keyboard. Evidence guidance follows the selected verdict and
absence reason. For no reference to the intended brand, leave supporting passages
blank and confirm that you read the complete answer. For an echoed name or an
unrelated namesake, quote the wording that supports that interpretation. The
unfinished-review reason and note are required only for **Leave unresolved**;
leave the reason blank when you can finish the judgment.

Opening **Review answer** from the queue also offers **Save and next** as the
primary action. It saves the current judgment, then starts or resumes a short
session in the same evidence import. If that session is still on the just-saved
answer, continuing acknowledges it and advances once. **Save and stay** keeps the
individual answer open for editing. **Leave unresolved and next** records the
unfinished decision before continuing. If no eligible first-review cases remain,
the overview confirms the save and shows the next available actions. This uses
the same development/held-out selection rules as starting a session normally.

Drafts autosave without advancing the queue. **Save and next** commits a validated
judgment; **Leave unresolved** needs an issue and explanation; **End session** saves
the unfinished draft and resumes at the same case. Failed or stale saves preserve
typed fields. A local backup stays in that browser tab's session storage until
confirmed saved or discarded. Restoring it is explicit, and cannot silently
replace a newer server revision. Live dashboard refresh is disabled on review pages.

Finishing a session opens **Review session complete**, with the current number of
finished judgments, unresolved decisions and drafts among that session's cases.
Choose **Check saved judgments** to continue. The Evaluation overview also shows
this action whenever finished judgments have checks or differences to handle;
you do not need to reopen each answer to discover the next step.

Before the first short session, the import's grouping plan is stored. The same
normalized buyer question, exact answers and answer bigrams with at least 90%
Jaccard overlap stay together. This is a conservative lexical heuristic, not
semantic deduplication. A label-independent digest assigns whole groups to
development or held-out partitions. The first pilot draws only development
cases; later review can annotate either partition. Held-out evidence must not
inform candidate prompt tuning. The queue does not select on proposed verdicts.

## Independent checks and resolutions

After committing your own judgment, choose **Check saved judgments** on the
Evaluation overview or session summary, then **Preview check**. The preview shows
the configured grader, estimated allowance and admission budget. The same preview
is linked from an individual finished review as **Preview an independent check**. **Run one
independent assessment** explicitly starts the call; viewing a page never does.
The assessment receives the complete saved question/answer, brand identity,
competitors and rubric, with imported and human labels, notes and evidence quotes
removed. It uses the same transport and normalization as production grading.
This is label independence, not a claim that the model is statistically independent
of the model that produced the original proposal.

**Check saved judgments** lists finished judgments for the selected evidence
import. It shows completed, unchecked, failed and running checks for the current
grader configuration and links directly to each next action. Differences needing
a human decision take priority. A changed grader configuration needs a new check;
an old completed assessment is not counted as current. Drafts and unresolved
reviews remain in the review queue. Return with **Continue checking saved
judgments** after inspecting a result or resolving differences. Opening the list
or a preview makes no model calls. Once all current checks are complete and no
differences remain, the main action becomes **Preview dataset version**. Checks
remain optional: the list also explains how to continue manual approval when
provider access is unavailable. Approval still blocks running checks and pending
differences for included cases.

Results are cached by immutable evidence, rubric, model, transport and evaluator
source digest. The check's fixed admission budget covers HTTP retries and an
explicit retry of a failed/interrupted assessment. Every API attempt also uses
the global daily ledger and concurrency gate. Settlement can exceed reservations;
neither admission budget is a hard billing ceiling. Claude Code uses its configured
subscription rather than API charges. Missing access leaves human review usable.

One check permits at most two assessment attempts. A request lasts at most three
minutes; its four-minute lease allows explicit recovery after a process crash.
Late workers cannot replace newer results. Provider/parse failures remain failed
checks and never become absent predictions. Failed checks do not invent disputes.

Differences in verdict, top pick, namesake-only or tone, and missing/invalid model
quotes, are grouped under the case. **Resolve check differences** shows the model
assessment beside your saved judgment. **Keep my saved judgment** preserves its
fields; **Save revised judgment and resolution** validates your edited fields.
Both require a resolution explanation. Resolution edits do not autosave over the
saved human decision. A new check arriving meanwhile requires reloading before
resolving it. You can instead leave the case unresolved. Existing approved versions
remain frozen; a new version cannot include pending differences or running checks.
Model agreement never creates another human review. Resolution history records
the check, human actor, label revision, action and explanation; approved manifests
include those records while keeping provenance as a single human review.

## Approve and export a version

**Preview dataset version** shows the exact included cases, excluded cases,
blocked rows, current label revisions and grouped partition assignments. Each
omitted unique case and blocked row needs an explicit exclusion reason. Duplicate
rows remain accounted for without creating another scored member. Only a current,
committed, complete judgment under the current rubric can be included. Approval
records the approving reviewer separately from each label's reviewer.

An approved version freezes membership, full evidence and identity, core labels,
all supporting passages, label revisions, provenance, exact rules and partitions.
Changing a current review does not edit an approved version. Approve a new version
after resolving a changed label; existing comparison reports keep their original
denominator. These labels are explicitly single human reviews, not independently
adjudicated decisions.

Export from the version page or with:

```sh
cheapseats evaluation export <dataset-id> approved.jsonl
```

The command creates a new private file and refuses to overwrite an existing one.
The JSONL follows the existing golden contract: original IDs and exact answer text,
`labeled_by = "human"` only for committed human decisions, the human explanation
in `note`, and the first human supporting passage in `evidence`. Download the full
manifest to retain all passages, exclusions and grouped partitions. The legacy
golden reader uses answer-only IDs and cannot represent one answer in two contexts;
approval refuses that collision rather than silently dropping a context or changing
IDs. Use separate dataset versions until a richer runner contract is introduced.

Dataset diagnostics include always-absent and lexical-presence baselines, class
counts, reference/prediction/correct denominators and missing results. A missing
prediction is never absent and is never dropped. Zero recall for a represented
non-absent verdict, positive top pick or namesake-only class fails the critical
class check. Fewer than two reference cases in any core class is insufficient
coverage; two is a floor, not a statistical reliability claim. No diagnostic can
grant release acceptance. In particular, 74/80 agreement from always predicting
absent cannot pass behind the old 92.5% aggregate threshold.

## Compare grader prompts

Open an approved version and choose **Create a grader comparison**. Select a
partition and one or two candidate prompts. The production control comes from the
version's saved rubric. The configured model, transport, evaluator source digest,
answers, brand identity and competitors stay fixed. A candidate must preserve the
rubric's meaning; a changed measurement definition needs separate review.

The preview counts every selected case, grader and repetition, estimates an output
allowance, and shows the admission budget. Each grader runs twice, including the
control. Nothing runs until **Run remaining assessments**. API usage uses both the
global daily ledger and the comparison's fixed admission limit across resumes and
HTTP retries. Settled costs can exceed reservations; limits are not hard billing
ceilings. Subscription calls still use the configured subscription.

The web worker continues when you leave the page, and pauses after 30 minutes,
exhausted admission budget or process shutdown. Completed predictions stay saved.
**Stop this worker** cancels the worker on the current server. Refresh the report
for progress. Resume can explicitly retry failed or expired claims once; late
workers cannot overwrite newer results. A resumed run requires the original model,
transport, evaluator and rules. Missing/failed predictions retain their denominator
and are never changed to absent. Each repetition is a separate model call, not a
cached copy of the other repetition.

Creating a held-out comparison requires declaring all candidates frozen and the
held-out cases/disputes unused for tuning. Creation reserves and consumes those
groups even if no provider call finishes; resume that comparison to finish it.
Evidence used in any earlier comparison, including development, cannot become
fresh held-out validation through a new dataset version or import. The guard checks
group IDs, normalized questions and answers, and the same lexical near-duplicate
rule used for grouping. It is bounded to 10,000 previously exposed cases and does
not establish semantic independence. The declaration also covers uses outside
this software, which the system cannot observe. New candidates informed by these
results need fresh untouched validation evidence.

Reports show per-class reference, prediction and correct counts; precision/recall
and verdict macro recall appear only with sufficient class coverage. Two examples
per class is a minimum floor, not a reliability claim. Paired improvements and
regressions compare the four frozen core fields; repeated-run differences expose
variation in the control and candidates. Quote checks establish occurrence, not
logical support. Baselines and failures remain visible. Reports do not crown an
automatic winner or change the production grader.

**Review masked results** omits candidate identities and the frozen reference label
from the response until that reviewer commits a judgment. Stable per-case A/B/C
ordering is derived from the random comparison ID. It is masking on the review
page, not access control against information elsewhere. A required note explains
the judgment; unresolved reviews keep identities hidden. Each reviewer has separate
revisioned history. Comparison reviews do not edit the benchmark or count as
independent human adjudication automatically. If the reference should change,
review the current case, approve a new version and recompute the comparison.

The same execution and report are available from the CLI, using the workbench's
existing data directory:

```sh
CHEAPSEATS_DATA_DIR="$PWD/data/evaluation" \
go run ./cmd/cheapseats evaluation run-comparison <comparison-id>

CHEAPSEATS_DATA_DIR="$PWD/data/evaluation" \
go run ./cmd/cheapseats evaluation comparison-report <comparison-id>
```

Add `--retry` to `run-comparison` only when you intend to retry failed or expired
assessments.

`comparison-report <id> --gate 1` exits nonzero while the first candidate lacks
conservative release-review prerequisites: complete results, untouched held-out
use with frozen candidates, class coverage, nonzero recall in every represented core class, valid quoted
spans, an observed paired improvement, no paired regressions and no increase in
repeat variation. Candidate 2 is the second candidate. Passing is a prerequisite
for human release review, not statistical proof, automatic deployment or a
replacement for the repository's real-provider golden test. The existing golden
CI corpus still needs explicitly approved human labels before that gate can
represent this reviewed benchmark.

The workbench uses database schema 18. Opening an existing database
backs it up before migration; older binaries refuse newer schemas. Rollback
requires the pre-migration backup, not merely the older binary. Evaluation tables
are private operational data; a read-only copy is not a redaction of its database.
