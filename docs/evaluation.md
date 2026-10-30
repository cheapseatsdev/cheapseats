# Evaluation

Evaluation keeps saved answer evidence, imported model labels and explicit human
decisions separate from production runs. Importing records and reviewing them
does not contact a model or change historical grades.

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

Drafts autosave without advancing the queue. **Save and next** commits a validated
judgment; **Leave unresolved** needs an issue and explanation; **End session** saves
the unfinished draft and resumes at the same case. Failed or stale saves preserve
typed fields. A local backup stays in that browser tab's session storage until
confirmed saved or discarded. Restoring it is explicit, and cannot silently
replace a newer server revision. Live dashboard refresh is disabled on review pages.

Before the first short session, the import's grouping plan is stored. The same
normalized buyer question, exact answers and answer bigrams with at least 90%
Jaccard overlap stay together. This is a conservative lexical heuristic, not
semantic deduplication. A label-independent digest assigns whole groups to
development or held-out partitions. The first pilot draws only development
cases; later review can annotate either partition. Held-out evidence must not
inform candidate prompt tuning. The queue does not select on proposed verdicts.

The workbench uses database schema 15. Opening an existing database
backs it up before migration; older binaries refuse newer schemas. Rollback
requires the pre-migration backup, not merely the older binary. Evaluation tables
are private operational data; a read-only copy is not a redaction of its database.
