# Evaluation

**Preview:** this workbench is unreleased and is not in 0.6.0. The current source
is in [the Evaluation batch PR](https://github.com/cheapseatsdev/cheapseats/pull/155).
It is off by default. The instructions below describe that preview build.

Evaluation keeps saved answer evidence, imported model labels and explicit human
decisions separate from production runs. Importing records and reviewing them
does not contact a model or change historical grades.

## What to do next

| Step | Your action | What it gives you |
| --- | --- | --- |
| 1. Review | Import saved answers and judge them in sessions of up to ten, using **Save and next** | Human labels made before seeing the old model proposal |
| 2. Check | At session completion, choose **Run checks for all N**; watch progress, then compare all results together | A second model assessment, with differences to inspect |
| 3. Resolve | Keep your labels with an explanation, revise them, or leave the case unresolved | An explicit human decision for each difference; model agreement needs no action |
| 4. Approve | Choose **Preview dataset version**, explain exclusions and approve | A frozen reference dataset with labels, evidence, partitions and history |
| 5. Compare | On the approved version, create and explicitly run a grader comparison | Paired control/candidate results and repeated-run variation on the same answers |
| 6. Review a change | Inspect regressions and coverage; validate frozen candidates on untouched evidence | Evidence for a separate prompt/code PR and human release review |

Saving ten judgments is progress, not a prompt change. Independent checks do not
replace your decisions, and approval does not install labels into golden CI.
Checks are optional; running checks and pending differences must be settled before
a case can be included in an approved version.
Comparisons never change the production grader automatically. Exporting an approved
version and transferring reviewed labels into the repository's golden fixtures
requires a separate reviewed change; preserve its manifest and exclusions.

## Try the review workflow locally

From the app checkout, use a separate test database and the example config,
whose daily schedule is disabled:

```sh
CHEAPSEATS_EVALUATION=1 \
CHEAPSEATS_CONFIG="$PWD/examples/cheapseats-config/cheapseats.toml" \
CHEAPSEATS_DATA_DIR="$PWD/data/evaluation" \
go run ./cmd/cheapseats serve -addr 127.0.0.1:3000
```

Open `http://127.0.0.1:3000/evaluation`, choose **Import saved review records**, then
**Start review · up to 10 cases**. Follow the steps above. Import, manual review,
approval and export need no provider keys and make no model calls. Independent
checks and grader comparisons require access to the configured grader; API calls
cost money and subscription calls use their subscription allowance.

Use this separate database for testing. Reuse the same directory when restarting
so your saved reviews remain available. If you already reviewed cases in a
temporary directory, keep that directory or use the original build's backup
command to preserve it before moving
the database; changing directories does not transfer reviews automatically.

## How many answers to review

There is no universal target of 80 answers. Start with a short pilot to learn the
rubric, then inspect your own collection's verdicts, top picks, namesakes, tones,
close calls and distinct question/answer groups. Review the whole collection when
it is small enough. For a larger collection, select a manageable set covering
those cases, preserve grouped partitions, and keep separate untouched evidence
for validating frozen candidates. A ten-case session is a workflow convenience,
not a sufficient benchmark or a statistical sample-size rule.

### Worked example: the bundled collection

For Cheap Seats' current bundled 80 records, we recommend reviewing **all 80**, in
short sessions. Other imports will have different sizes and distributions.
Before human review, all 80 bundled labels are Claude proposals: 74 absent,
four recommended and two mentioned. They include only three positive top-pick
labels and one namesake. Those counts can change when people review them.

Even all 80 are a curated calibration sample, not production prevalence or a
statistical guarantee. Under the current grouping rule, their split is 67
development and 13 held-out cases; all 13 held-out proposals are absent. Add fresh
real answers covering recommendations, mentions, namesakes, tone and close calls
before using a held-out comparison to judge a prompt change. Do not invent a label
to meet coverage: leave incomplete or ambiguous cases unresolved and explain
exclusions. Two examples per core class is the app's minimum coverage floor, not
a recommended sample size or proof of reliability.

Human annotation of held-out cases is allowed; using their answers or disputes to
tune a candidate spends their value as untouched validation. Existing public
fixtures may already have informed prompt development outside the app. Declare
that use honestly and collect fresh validation evidence when necessary.

## Import saved evidence

```sh
cheapseats evaluation import                   # the bundled public golden fixtures
cheapseats evaluation import records.jsonl     # a private JSONL export
```

Set `CHEAPSEATS_DATA_DIR` to the workbench's directory for every CLI operation.
For the local quickstart above, import your own saved records before starting the
server with:

```sh
CHEAPSEATS_DATA_DIR="$PWD/data/evaluation" \
go run ./cmd/cheapseats evaluation import /path/to/records.jsonl
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
The page offers **Run checks for all N** finished judgments, or **Compare all N
saved assessments** when no new calls are needed. The Evaluation overview offers
the batch flow across the selected import. You do not need to reopen each answer
to discover the next step.

Before the first short session, the import's grouping plan is stored. The same
normalized buyer question, exact answers and answer bigrams with at least 90%
Jaccard overlap stay together. This is a conservative lexical heuristic, not
semantic deduplication. A label-independent digest assigns whole groups to
development or held-out partitions. The first pilot draws only development
cases; later review can annotate either partition. Held-out evidence must not
inform candidate prompt tuning. The queue does not select on proposed verdicts.

## Independent checks and resolutions

After a review session, the app asks **Run independent checks on these N
judgments?** It shows the configured grader, the number of saved assessments it
will reuse, an estimated allowance for new calls, and optional budget/retry
settings. **Run checks for all N** starts one batch. The live progress page opens
automatically, then switches to **Compare independent checks** when the sweep
finishes. No per-case previews or repeated judgment saves are required.

A session's offer selects its finished judgments. The Evaluation overview offers
all finished judgments in the selected import, up to 500 cases per batch. The
batch fixes its case membership and model/transport/evaluator/rule configuration.
Each result compares against the current saved human judgment, with an indication
when that judgment changed after the batch started. This is a working comparison,
not an immutable scored benchmark; approved dataset versions remain separate.

All answers appear on one comparison screen, with human and model labels,
reasoning, evidence, failures and unresolved differences. Agreements need no
action. **Select all differences** and **Keep selected judgments** apply one
required human explanation to the chosen cases. Every keep uses the saved
revision and exact pending check set, records its actor and explanation, and
preserves the original judgment fields. If a selected case changed meanwhile,
other successful keeps remain saved and the conflict is shown without overwriting
newer work. Remaining cases must be selected again after reviewing current labels.

Use **Change or inspect judgment** for a particular case when needed. Saving a
revision or resolution returns directly to that case's row in the combined
comparison. Ordinary case edits and individual check tools remain available;
they are secondary to the batch flow. First-review counts remain counts of
unique human-reviewed answers, separate from completed model assessments.

The worker continues when you leave the page and pauses after 30 minutes or an
explicit pause. Results are saved per case. Resume reuses completed assessments;
retrying failed or interrupted assessments requires an explicit choice and retains
each check's two-attempt limit. A four-minute renewable worker lease prevents
competing batches from overwriting progress; an interrupted process can be
recovered after its lease expires. Provider failures remain unknown, never absent,
and every selected case remains accounted for. Missing provider access still
allows viewing saved results and continuing manual review.

One admission ceiling covers the selected check operations, including their
previous recorded usage and resumes, alongside the existing per-check and daily
limits. Actual settled charges can exceed reservations; this is not a hard
provider billing ceiling. Opening an offer, progress or comparison never starts
additional calls. All new calls follow the explicit batch start or retry action.

The assessor receives the complete saved question/answer, intended brand,
competitors and rubric with imported and human labels, notes and evidence quotes
removed. It uses production grading transport and normalization. This is label
independence, not statistical independence from the model that produced the old
proposal. A human still owns each label; model agreement does not add another
human review.

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
assessment beside your saved judgment. **Keep selected judgments** preserves their
fields; saving a revised judgment validates its edited fields. Individual
resolution controls use the same rules.
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

## Database compatibility and privacy

The preview workbench uses database schema 19. Opening an existing database
backs it up before migration; older binaries refuse newer schemas. Rollback
requires the pre-migration backup, not merely the older binary. Evaluation tables
are private operational data; a read-only copy is not a redaction of its database.
