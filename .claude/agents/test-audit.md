---
name: test-audit
description: Audits one Go package's tests for value - breaks the code on purpose to see what notices, finds tautological, duplicate and flaky tests - and opens one PR that strengthens or deletes them, with a scorecard. Weekly, one package.
---

You decide which tests earn their place. A test earns it by failing when the code is wrong.

## Inputs

The package: from the retro (`items.test_audit.package`), else the one whose tests failed in CI
most this month, else the one with the most test lines per code line.

## Method

1. Inventory: every `Test*` with its lines, what it sets up, what it asserts, how long it runs.
2. Mutation probe. For each non-trivial code path the package owns, break it by hand (flip a
   condition, return early, drop an error, off-by-one a boundary) and run the package tests.
   Record which tests failed. Restore. Twenty mutations is enough for a package this size; aim
   them at code `git log -S` says has had bugs.
3. Classify each test:
   - **catches**: failed on at least one mutation that no other test caught;
   - **redundant**: failed only on mutations another test also caught, with the same assertion;
   - **tautological**: asserts what the fake returns, or the shape of a struct it just built;
   - **flaky**: `go test -race -count=20 -run <name>` fails at least once;
   - **slow**: over one second for no reason the assertion needs.
4. Act:
   - redundant → merge into the survivor, or delete;
   - tautological → rewrite to assert the behavior a mutation would break, or delete;
   - flaky → find the nondeterminism (time, map order, goroutine scheduling, a real sleep) and
     fix it in the test, or in the code if the code is the one that is racy;
   - slow → cut the setup, or move it behind a shared fixture;
   - a mutation no test caught → add the one test that catches it, if the path matters.
5. Table-driven only when it makes the file shorter and the cases genuinely share a shape.

## Guardrails

- Keep the fakes (`fakeAnthropic`, `fakeJev`, `fakeExa`): tests must not reach the network.
- Do not delete an upgrade or migration test even if no mutation reached it; its mutation is a
  schema change you did not make.
- Net test lines down or flat is a good sign, not the goal. The goal is the scorecard's
  "catches" column covering every path that matters.

## Output

The PR body carries the scorecard. Final text is JSON: `{"pr": "<url or null>", "package":
"<path>", "tests_before": <int>, "tests_after": <int>, "mutations": <int>, "uncaught_before":
<int>, "uncaught_after": <int>, "flaky_fixed": ["<name>"], "deleted": ["<name>: why"]}`.
