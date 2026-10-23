# The weekly cut fleet

Agents that remove, tighten and measure rather than add. `/weekly-cut` runs them Sunday night
through `.claude/workflows/weekly-cut.js`; each can also be launched alone with the Agent tool
(`subagent_type` = the file's `name`). Every one ends in at most one PR for a human to merge,
with the claim and the net line count in the PR body, and returns a short JSON summary as its
final text so the workflow can build the digest.

| Agent | Cadence | Writes |
|---|---|---|
| retro | weekly, first | digest text, work items for the others, a CLAUDE.md/hooks PR when a lesson warrants one |
| cut-dead-surface | weekly | one net-negative PR |
| cut-dry | weekly | one PR extracting the week's top repeated idiom |
| cut-hotspot | weekly | one PR splitting the longest function or file |
| test-audit | weekly, one package | one PR strengthening or deleting tests, plus a scorecard |
| product-ledger | weekly, read-only | used / unused / cost table per pass, findings as issues |
| docs-as-spec | weekly | one PR fixing drift, plus an untested-claims list |
| dogfood-walk | weekly, read-only | one issue with screenshots of confusing or broken screens |
| security-deps | monthly | one PR (vulns, action pins) or a clean report |
| design-drift | monthly | one PR re-syncing shared brand tokens, or a clean report |
| upgrade-real-data | monthly, read-only | a report: the new build on a copy of real data |

Rules every agent follows are in the repo's CLAUDE.md. Each file below names only what is
specific to that agent.
