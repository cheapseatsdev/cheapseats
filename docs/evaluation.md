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

The storage foundation uses database schema 14. Opening an existing database
backs it up before migration; older binaries refuse newer schemas. Rollback
requires the pre-migration backup, not merely the older binary. Evaluation tables
are private operational data; a read-only copy is not a redaction of its database.
