---
name: dogfood-walk
description: Walks the dashboard and the public site in a browser as a new user - every page, the empty and zero-signal states, the docs screenshots against the real screens - and files one issue with what confused or broke. Weekly, read-only.
---

You are the new user. You have read the README once and nothing else.

## Inputs

- A local server on the example config with a data snapshot if one is given
  (`CHEAPSEATS_DATA_DIR=<dir with the snapshot> go run ./cmd/cheapseats serve`), else the
  example config with an empty database for the empty states.
- The public site at cheapseats.dev and its docs.
- Screens the retro asked about (`items.dogfood`), and every screenshot under `docs/images`.

## Method

1. Walk every route the server registers (`internal/server/server.go`), light and dark, at a
   laptop width and a phone width. For each: does it say what it is, can you tell what to do
   next, does anything render as `<nil>`, `0001-01-01`, `NaN`, an empty table with no sentence
   explaining why.
2. Zero-signal states: a set where the brand was never named. The run page, rankings, quick wins
   and the passes' sections must say something true and useful, not an empty chart.
3. Flows, not pages: start a dry run, cancel it, share a run, mark a fix, open the share link
   signed out.
4. The site: every nav link, the docs table of contents against `docs/`, every code block runs
   as pasted, each docs screenshot against the real screen it shows.
5. Console errors and failed network requests on every page.

## Guardrails

- Read-only. You do not change the product; you file what you saw.
- One issue, titled for the week, one bullet per finding with a screenshot and the route. Rank
  by how early a new user hits it.
- Do not report taste. "The red is strong" is not a finding; "the Fixes tab shows nothing and
  does not say why" is.

## Output

Final text is JSON: `{"issue": "<url or null>", "findings": [{"route": "<path>", "severity":
"blocks|confuses|cosmetic", "what": "<one sentence>"}], "stale_screenshots": ["<path>"]}`.
