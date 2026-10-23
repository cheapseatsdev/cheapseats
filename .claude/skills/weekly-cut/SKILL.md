---
name: weekly-cut
description: Run the Sunday code-cutting fleet - retro, dead surface, DRY, hotspot, test audit, product ledger, docs as spec, dogfood walk, and on the first Sunday of a month the security, design-drift and upgrade checks. Use when the user says /weekly-cut, "run the weekly cut", "cut week", or asks for the Sunday fleet.
---

Run the weekly cut for Cheap Seats. The fleet is defined in `.claude/agents/`; the orchestration
is the saved workflow `weekly-cut` (`.claude/workflows/weekly-cut.js`). Each agent ends in at
most one PR or issue for the user to merge; nothing merges itself.

Steps:

1. `git fetch origin && git status --short`. Run from a clean checkout of `main`; the agents that
   edit use their own worktrees.
2. The ISO week: `date +%G-W%V` → `week`. First Sunday of the month (`[ "$(date +%d)" -le 7 ]`)
   → `monthly: true`, unless the user said otherwise.
3. The data snapshot: `deploy/snapshot-db.sh <vm>` (the VM name is in the user's deploy notes;
   ask if you do not know it). It writes `data/snapshots/<vm>-<date>.db` and prints the path →
   `snapshot`. If the permission gate denies the script, give the user the exact line to run
   with the `!` prefix and continue only after the file exists; if they decline, pass
   `snapshot: null` and the agents skip behavior compares.
4. Call the Workflow tool with `name: "weekly-cut"` and `args: {week, snapshot, monthly}`. The
   user invoked this skill, which is the opt-in the Workflow tool needs.
5. When it finishes, relay the digest issue link and one line per PR opened, with each PR's
   claim and net line count. Say plainly which agents opened nothing and why.

Do not merge anything. Do not launch the agents by hand when the workflow is available; the
workflow carries the retro's work items to them.
