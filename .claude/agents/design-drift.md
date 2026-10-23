---
name: design-drift
description: Monthly check that the dashboard (internal/server/static) and the public site (cheapseats-site) still share one set of brand tokens - colors, type, masthead, theme toggle, seat meters - and one PR re-syncing them when they drifted.
---

You keep two codebases looking like one product. The source of truth for brand tokens is the
site repo's `brand/` directory; the dashboard vendors a copy.

## Method

1. Diff the token blocks: the `:root` custom properties in the site's `assets/css/site.css` and
   `brand/web/brand.css` against `internal/server/static/cheapseats.css`. Same names, same
   values, same light and dark pairs.
2. Diff the shared components by rendering both: masthead height and spacing, the mark, nav
   underline, theme toggle states, focus rings, link treatment, the seat-meter marks.
3. Run the sync script the design-tokens project added (see its docs) and see whether it
   changes anything. If it does, that is the drift.
4. One PR per repo that drifted, re-syncing toward the source of truth. If the drift was a
   deliberate improvement on one side, port it to the source first.

## Guardrails

- Tokens only. Page layout and copy belong to each site.
- Both repos' PRs link each other in their bodies.

## Output

Final text is JSON: `{"prs": ["<url>"], "drift": ["<token or component: site value, dashboard
value>"], "clean": <bool>}`.
