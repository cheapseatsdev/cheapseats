---
name: cut-dead-surface
description: Finds and removes unused code and surface in the Go app - unreachable functions, unused exports and flags, orphan templates and CSS, docs for things that no longer exist - and opens one net-negative PR. Weekly.
---

You remove what nothing uses. The PR you open deletes more than it adds or you do not open it.

## Method

1. Candidates, each with evidence:
   - `go run golang.org/x/tools/cmd/deadcode@v0.52.0 -test ./...` for unreachable functions
     (a pinned version, so a new release is a reviewed edit here, not whatever `@latest` serves).
   - `golangci-lint run --no-config --disable-all --enable unused,unparam ./...`.
   - Exported identifiers in `internal/` with one reference (their definition): `gopls` or a
     grep over the module. `internal/` has no external callers, so exported-but-unused is dead.
   - CLI flags and env vars defined but never read; config keys parsed but never used.
   - Templates in `internal/server/templates` no handler executes; CSS classes in
     `internal/server/static/cheapseats.css` no template or JS mentions; JS functions nothing
     calls.
   - Docs pages, README lines and `docs/` sections that describe a command, flag or page that
     no longer exists.
   - Items the retro handed you (`items.dead_surface`), verified the same way.
2. For each candidate, prove it is dead: show the search that found no use. Something only tests
   use is dead too, and the test goes with it.
3. Delete. Run the pre-push checks. If a test fails, the thing was not dead; put it back and note
   why in the summary.

## Guardrails

- Never remove a migration, a stored-JSON field decoder, or anything under `internal/store`
  that an old database might still need. Lenient decoding of old shapes is deliberate.
- Never remove an MCP tool, CLI command or config key without a deprecation note in the PR body;
  deployments depend on them. Prefer listing those as "candidates for a human decision".
- One PR. If the week's harvest is large, take the clearest third and list the rest.

## Output

Final text is JSON: `{"pr": "<url or null>", "loc_delta": <negative int>, "removed":
["<what>"], "kept_back": ["<candidate and why not>"], "for_human": ["<public surface that
looks dead>"]}`.
