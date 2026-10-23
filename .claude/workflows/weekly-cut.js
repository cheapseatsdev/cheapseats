export const meta = {
  name: 'weekly-cut',
  description: 'Sunday cut: retro first, then the cut, test, product, docs and dogfood agents, each ending in one PR or issue; a digest issue closes the week',
  whenToUse: 'The weekly code-cutting pass. Run through /weekly-cut, which pulls the data snapshot and passes args {week, snapshot, monthly}.',
  phases: [
    { title: 'Retro', detail: 'commits, CI and sessions of the last seven days' },
    { title: 'Cut', detail: 'dead surface, DRY, hotspot, test audit' },
    { title: 'Observe', detail: 'product ledger, docs as spec, dogfood walk' },
    { title: 'Monthly', detail: 'security and deps, design drift, upgrade on real data' },
    { title: 'Digest', detail: 'one issue linking the week\'s PRs and findings' },
  ],
}

// args: { week: "2026-W44", snapshot: "/abs/path/to/snapshot.db" | null, monthly: bool }
const week = (args && args.week) || 'this week'
const snapshot = (args && args.snapshot) || null
const monthly = Boolean(args && args.monthly)
const str = (x) => (x == null ? '' : typeof x === 'string' ? x : JSON.stringify(x, null, 1))

const RETRO = {
  type: 'object',
  properties: {
    digest: { type: 'string' },
    pr: { type: ['string', 'null'] },
    items: {
      type: 'object',
      properties: {
        dead_surface: { type: 'array', items: { type: 'string' } },
        dry: { type: 'array', items: { type: 'string' } },
        hotspot: { type: 'array', items: { type: 'string' } },
        test_audit: { type: 'object', properties: { package: { type: 'string' }, why: { type: 'string' } } },
        product: { type: 'array', items: { type: 'string' } },
        docs: { type: 'array', items: { type: 'string' } },
        dogfood: { type: 'array', items: { type: 'string' } },
      },
    },
  },
  required: ['digest', 'items'],
}

phase('Retro')
log(`Weekly cut for ${week}: retro first`)
const retro = await agent(
  `Run the weekly retrospective for ${week}: the last seven days of commits on main, CI runs, and Claude session transcripts. Produce the digest and the work items for the other agents as your JSON output. Open the CLAUDE.md/hooks PR only if a lesson repeated.`,
  { label: 'retro', phase: 'Retro', agentType: 'retro', schema: RETRO },
)
if (!retro) {
  log('Retro returned nothing; the other agents run without work items')
}
const items = (retro && retro.items) || {}
const handoff = (key) => {
  const v = items[key]
  return v && (Array.isArray(v) ? v.length : Object.keys(v).length)
    ? `\n\nThe retro handed you these items for ${week}; verify them yourself before acting:\n${str(v)}`
    : `\n\nThe retro handed you no items for ${week}; find your own.`
}
const snapshotNote = snapshot
  ? `\n\nA consistent database snapshot is at ${snapshot}. Use it for any behavior compare; never query a live database.`
  : `\n\nNo database snapshot is available this week; skip behavior compares that need one and say so in your output.`

// Cut and Observe run as one fan-out: nothing in Observe needs a Cut result.
const WORK = [
  { key: 'dead', agentType: 'cut-dead-surface', phase: 'Cut', worktree: true,
    prompt: `Remove unused code and surface in the Go app for ${week}, one net-negative PR.` + handoff('dead_surface') + snapshotNote },
  { key: 'dry', agentType: 'cut-dry', phase: 'Cut', worktree: true,
    prompt: `Extract the single most repeated idiom in the Go app for ${week}, one PR, every occurrence replaced.` + handoff('dry') + snapshotNote },
  { key: 'hotspot', agentType: 'cut-hotspot', phase: 'Cut', worktree: true,
    prompt: `Split the longest, most churned function or file in the Go app for ${week} along one responsibility seam, one PR, no behavior change.` + handoff('hotspot') + snapshotNote },
  { key: 'tests', agentType: 'test-audit', phase: 'Cut', worktree: true,
    prompt: `Audit one package's tests for ${week}: mutation probe, classify, strengthen or delete, one PR with the scorecard.` + handoff('test_audit') },
  { key: 'product', agentType: 'product-ledger', phase: 'Observe', worktree: false,
    prompt: `Build the product ledger for ${week}: per set, per pass, the fix loop, spend, and what last week's fleet PRs claimed and did.` + handoff('product') + snapshotNote },
  { key: 'docs', agentType: 'docs-as-spec', phase: 'Observe', worktree: true,
    prompt: `Check the docs against the code for ${week}; fix drift in one PR and list untested claims.` + handoff('docs') },
  { key: 'dogfood', agentType: 'dogfood-walk', phase: 'Observe', worktree: false,
    prompt: `Walk the dashboard and the public site as a new user for ${week}; file one issue.` + handoff('dogfood') + snapshotNote },
]
if (monthly) {
  WORK.push(
    { key: 'security', agentType: 'security-deps', phase: 'Monthly', worktree: true,
      prompt: `Monthly security and dependency pass for ${week}: govulncheck, module bumps, action pins, image base, new routes and MCP tools. One PR or a clean report.` + snapshotNote },
    { key: 'design', agentType: 'design-drift', phase: 'Monthly', worktree: true,
      prompt: `Monthly design drift check for ${week} between internal/server/static and ../cheapseats-site. Re-sync toward the site's brand/ tokens in one PR per drifted repo, or report clean.` },
    { key: 'upgrade', agentType: 'upgrade-real-data', phase: 'Monthly', worktree: false,
      prompt: `Monthly upgrade proof for ${week}: last release tag versus main on two copies of the snapshot. Report, do not merge.` + snapshotNote },
  )
}

log(`Fanning out ${WORK.length} agents`)
const results = await pipeline(
  WORK,
  (w) => agent(w.prompt, {
    label: w.key,
    phase: w.phase,
    agentType: w.agentType,
    ...(w.worktree ? { isolation: 'worktree' } : {}),
  }).then((r) => ({ key: w.key, result: r })),
)
const done = results.filter(Boolean)
const skipped = WORK.map((w) => w.key).filter((k) => !done.some((d) => d.key === k))
if (skipped.length) log(`No result from: ${skipped.join(', ')}`)

phase('Digest')
const digest = await agent(
  `Write the weekly cut digest for ${week} and file it as one GitHub issue in cheapseatsdev/cheapseats titled "Weekly cut ${week}" with label "weekly-cut" (create the label if missing). Sections: the retro's numbers and lessons; one line per agent with its PR or issue link, its claim and net lines (or why it opened nothing); the product ledger's findings; the untested claims; what to feed next week's retro. Under 600 words, brand-neutral, no customer or deployment names, no raw database rows. Agents that returned nothing: ${skipped.join(', ') || 'none'}.

Retro digest:
${str(retro && retro.digest)}

Retro PR: ${str(retro && retro.pr) || 'none'}

Agent results (JSON, one per agent):
${done.map((d) => `--- ${d.key}\n${str(d.result)}`).join('\n\n')}`,
  { label: 'digest', phase: 'Digest' },
)

return { week, retro_pr: retro && retro.pr, digest, results: done, skipped }
