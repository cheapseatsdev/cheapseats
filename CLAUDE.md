# Cheap Seats: how we work in this repo

Cheap Seats asks AI assistants a brand's buyers' questions, grades whether the answers recommend
the brand, and builds a ranked fix list. Go, SQLite, server-rendered HTML, an MCP server. The
docs in `docs/` are the spec: when behavior changes, the doc changes in the same PR.

## Names

- The project is Cheap Seats and always was. Do not introduce, restore or reference any other
  name for it, in code, fixtures, docs, commit messages or PR text.
- The app is brand-neutral. Anything about a particular deployment or customer belongs in that
  deployment's private config repo, never here. Do not name customers anywhere in this repo.

## Before you push

The `.claude/hooks/pre-push.sh` hook runs these when a `git push` is about to happen; run them
yourself if you push some other way:

```sh
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...
bash -n deploy/*.sh .claude/hooks/*.sh
```

`internal/server` takes about eight seconds; the rest is under a second each.

The hook and the weekly-cut fleet trust the checked-out branch: they run its tests, scripts and
agent files with your credentials. Run them only on branches you or this repo's agents wrote,
never on a branch from someone else's fork or PR.

## Pull requests

- One idea per PR. The title is a sentence in plain words, the way the git log reads.
- The body states the claim and how to check it. A cut states the net line count. A refactor
  states what stays the same and what proved it (tests, or a compare on a data copy).
- Branches are short kebab-case (`prompt-review`, `sqlite-immediate`). PRs are squash-merged, so
  rebase with `git rebase --onto origin/main <base-sha>`, never onto a merged branch.
- In parallel work, prefix test helpers with the feature name (`reviewFakeJev`, not `fakeJev`);
  the package is shared and `go vet` fails on the second definition.
- Do not edit shared lists (README feature list, docs index) unless the PR is about them; every
  parallel PR that touches them conflicts on rebase.

## Don't break people's data

Deployments keep months of runs in SQLite. Migrations are append-only and back up first; the
store refuses a newer database. Stored JSON decodes leniently so old shapes keep working. Any
schema or stored-format change extends `internal/app/upgrade_test.go`, and before merging you
compare the old and new builds on a copy of a real database (same quick wins, same ranks).

Never write a guess that becomes permanent: a backfill writes only confident matches.

## Grading is the product

Any change to the grader prompt (`grade.System`), the answer prompt, the search policy or
anything else `run.GradeInputs` covers is a **method change**. A method change makes earlier
runs non-comparable: trends break and open fix attempts cannot be measured until their runs are
regraded. So a method change:

- passes the grader golden set (`go test ./internal/grade/... -run Golden`, see docs/grading.md),
- says in its PR body that it is a method change and what the release notes should say,
- is followed by a regrade of the sets that have open attempts.

## Tests

- Tests talk to fake providers (`fakeAnthropic`, `fakeJev`, `fakeExa` in their packages) over
  `httptest`; nothing in `go test` reaches the network.
- No `time.Sleep` in tests; wait on a channel or poll a condition with a deadline.
- A test earns its place by failing when the code is wrong. If you cannot say what bug a test
  would catch, delete it or strengthen it. Table-driven only when it makes the file shorter.
- The three `t.Skip` tests are external integrations and stay skipped without keys.

## Shell scripts

`deploy/*.sh` run on macOS (bash 3.2, BSD tools) and Linux. No `timeout`, no `sed -i` without
`''`, no `grep -P`, no `readlink -f`, no `date -d`. `bash -n` every script you touch. Tailscale
SSH to a Mac does not propagate exit status; branch on captured output, not `$?`.

## The weekly cut

Sundays the fleet in `.claude/agents/` runs via `/weekly-cut` (`.claude/workflows/weekly-cut.js`):
retro first, then dead-surface, DRY, hotspot, test audit, product ledger, docs-as-spec and a
dogfood walk, each ending in one PR for a human to merge. The agents read this file; keep it
current when a rule changes.
