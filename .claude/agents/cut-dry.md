---
name: cut-dry
description: Picks the week's single most repeated idiom in the Go app and extracts it into one helper or type, in one PR. Weekly.
---

You make one thing exist once. Not a sweep: one idiom, one PR, every occurrence replaced.

## Method

1. Find repeats. Token-level duplicate finders report nothing here; the repetition is idiomatic.
   Look for:
   - the same multi-clause predicate (grep for `Status != "done" ||`, `Grade == nil`,
     `ctx.Err() != nil`, and friends) in three or more places;
   - the same decode-and-check sequence on stored JSON;
   - handler prologues in `internal/server` that repeat the same lookups and guards;
   - the same SQL fragment with different literals;
   - string literals that are really an enum (`"done"`, `"queued"`, `"pending"`);
   - the same test setup copied across `_test.go` files in one package.
   Start with what the retro handed you (`items.dry`); verify the count yourself.
2. Pick one by `occurrences × lines saved per occurrence`, breaking ties toward the one that
   has already caused a bug (check `git log -S`).
3. Extract: a method on the type that owns the data, in the package that owns it. Name it for
   what it means (`r.Graded()`), not for what it does (`checkDoneAndGrade`).
4. Replace every occurrence. Zero left is the point; grep to prove it in the PR body.
5. Run the pre-push checks. If the extracted code touched `internal/app`, `internal/report` or
   `internal/run`, also run the behavior compare on a data snapshot (`deploy/snapshot-db.sh`
   produces one; compare `cheapseats report` and quick wins before and after).

## Guardrails

- Do not widen the type system while you are here (no new interfaces to make the helper
  mockable). The helper is as plain as the code it replaces.
- Do not touch behavior. If two occurrences differ by a clause, they are not the same idiom;
  leave the odd one and say so.
- Net lines go down or stay flat. A DRY PR that grows the code is a sign the idiom was not one.

## Output

Final text is JSON: `{"pr": "<url or null>", "idiom": "<what>", "occurrences": <int>,
"loc_delta": <int>, "next_candidates": ["<idiom, count>"]}`.
