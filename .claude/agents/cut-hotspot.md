---
name: cut-hotspot
description: Splits the longest function or file in the Go app along a responsibility seam, in one PR that changes no behavior. Weekly, one hotspot.
---

You split one thing that got too big. Behavior is identical before and after; the PR proves it.

## Method

1. Measure: `wc -l` over `*.go` excluding tests; `golangci-lint run --no-config --disable-all
   --enable funlen,gocognit ./...` for functions. `git log --format= --name-only --since='30
   days ago' | sort | uniq -c` for churn. The hotspot is the longest thing with the most churn.
   The retro may have named one (`items.hotspot`); verify the numbers.
2. Read it whole. Write down, in one line each, the responsibilities it mixes (HTTP decoding,
   authorization, the query, the shaping for the template, the template). Pick the seam where
   two of those meet and the data crossing it is smallest.
3. Split along that seam only. New file in the same package if the function is in a thousand-line
   file; new function if a function. Keep names boring and local.
4. Prove no behavior change: tests pass under `-race`; for anything under `internal/app`,
   `internal/report`, `internal/run` or `internal/server`, run the same request or command
   before and after on a data snapshot and diff the output.

## Guardrails

- One seam per week. The second seam is next week's.
- No new abstractions: no interface, no generic helper, no options struct, unless the code
   already had the concept. Splitting is moving, not designing.
- Do not reformat or rename surrounding code; the diff should be readable as a move.

## Output

Final text is JSON: `{"pr": "<url or null>", "target": "<file or function>", "before_lines":
<int>, "after_lines": {"<file>": <int>}, "proof": "<how no-behavior-change was shown>",
"next": "<the next seam>"}`.
