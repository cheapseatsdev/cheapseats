---
name: idea-review
description: Review product ideas filed as issues ("Idea from <tracker>: ...") against a high bar - verify every claim in the issue against the code, say whether it extends an existing surface or adds new ones, count the surfaces and the recurring costs, and end in a take / smaller cut / park / decline verdict per idea. Use when the user says /idea-review, "review the ideas", "review issue N", or asks whether a proposed feature is worth taking.
---

Review feature ideas for Cheap Seats the same way every time. The product is feature rich; the
default answer is no. An idea earns a yes when it makes a number the user already has
actionable, or closes a loop the user cannot close today, and does it mostly from data the app
already stores. Positioning against a competitor is a motive, not user value.

Arguments: issue numbers (`/idea-review 82 83`). With none, review every open issue whose title
starts with `Idea from`. `--comment` posts each verdict as a comment on its issue; without it,
nothing leaves the terminal. Never close or label an issue; that is the user's call.

## For each issue

1. **Read it.** `gh issue view N --json title,body`. Split the body into three lists: what it
   says the app already does (these lower the cost), what it says the app lacks, and the sketch
   (tables, config keys, packages, routes, MCP tools, pages).

2. **Check every claim against the code.** Issues written from the outside are often wrong
   about what exists and where. Grep, do not trust. The usual places:
   - stored data: `internal/store/schema.sql` and `internal/store/0*.sql` (what is kept, for
     how long, keyed how)
   - config: `internal/config/config.go`, set files in `internal/sets/sets.go`
   - what a run already computes: `internal/report/report.go` (a "join" claim is true only if
     one loop already sees both sides)
   - method boundary: `internal/run/regrade.go` `GradeInputs`; `grade.System` in
     `internal/grade/grade.go`; the golden set in `internal/grade/testdata/golden`
   - spend: `internal/budget`, `spend_events`
   - fetching and its cache: `internal/audit/citations.go` (`page_fetches`, robots, text cap)
   - statistics the app already has: `internal/app/stats.go`, `docs/statistics.md`
   - surfaces: `internal/server` (pages, `/api`, MCP tools), `cmd/`, `docs/`
   Record each claim as right, wrong or partly right, with a `file:line`. A wrong "the app
   throws this away" usually means the idea is cheaper than written; a wrong "the app already
   has this" usually means it is dearer.

3. **Classify: extends or adds.** Extends: a field on an existing struct, a column in an existing
   table on a page, a line in an existing prompt, a parameter on a call already made. Adds: any
   of these, each counted once:
   migration table · config key · CLI command · API route · MCP tool · dashboard page or section
   · chart element · provider call or spend kind · scheduled pass · text heuristic (anything
   that pattern-matches page or answer text) · a statistical procedure not in docs/statistics.md.
   Report the count. Four or more new surfaces is a product, not a feature.

4. **The costly flags.** Say yes or no to each, with the reason:
   - **Method change**: touches `GradeInputs`, the grader or answer prompt, or the search
     policy. Breaks trends and open attempts; needs the golden set, release notes, a regrade.
   - **Migration**: needs `internal/app/upgrade_test.go`, a compare on a real-data copy, and
     the monthly `upgrade-real-data` agent carries it forever.
   - **Spend**: new model or search calls per run or per action; needs a budget kind, a cost
     column in the product ledger, and a cap in config.
   - **English-assuming**: a heuristic over text (folding, whole-word matching, page-type rules,
     source kinds) that an idea about language or markets turns into a per-language bug.
   - **Fetching**: new pages fetched, so robots, caps, user agent and politeness apply.

5. **Value, concretely.** Who sees it, on which page, after which action, and what they do
   next that they could not do before. Is the data already stored (a join) or does it need new
   calls? Does the issue's own evidence hold up (quotes from competitors describe their feature,
   not our users' need)? If the value depends on data accumulating (outcomes, versions), say how
   long a typical deployment waits before it says anything.

6. **Ongoing cost.** Every surface from step 3 carries: a test that fails when it is wrong, a
   docs/ page kept as spec, a JSON shape decoded leniently forever, a template, an MCP digest
   trimmed like the others, and a place in the weekly fleet's audits. Name the ones that recur
   (a retention policy, a second cache of the same data, a procedure that needs its own
   statistics section) rather than listing all of them.

7. **Verdict**, one of:
   - **take**: build as sketched, or with named trims
   - **smaller cut**: name the one piece that carries most of the value with the fewest surfaces,
     and what it drops
   - **park**: worth it only when a named condition holds (a user asks, enough outcomes exist);
     say the condition
   - **decline**: say what is cheaper and already answers the need
   State the condition or the cut in one sentence a future reader can act on.

## Output

Per idea, in this order: verdict in one line; what the issue got right and wrong about the code
(bullets with `file:line`); extends or adds, with the surface count; the flags; value; ongoing
cost; the cut or the condition. Then one table across all ideas: issue, verdict, new surfaces,
flags, one-line reason. With `--comment`, post the per-idea section as the comment, verdict
first, and end it with the attribution line this session uses for PR bodies.

## Rules

- Read-only. Do not open a branch or change code; a verdict of take becomes work later.
- Quote the code, not the issue, for what exists. A claim you did not verify is marked unverified.
- Do not grade an idea up for coming from a well-known tracker, or down for coming from a
  competitor. The product's own rules (CLAUDE.md, docs/) are the lens.
- Keep the review free of customer names; the issues are public.
