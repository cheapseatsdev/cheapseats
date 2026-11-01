# Next steps

Next steps (`/next-steps`) keeps three kinds of advice together. Opening the page reads stored
analyses and makes no model or page-fetch calls.

| View | What earns a place | Handoff |
| --- | --- | --- |
| Actions | A specific, in-scope change with current evidence, a remaining gap, eligibility and a controllable completion check | Implementation prompt and work status |
| Investigations | An unresolved check whose answer would change a worthwhile decision | Bounded, read-only check and decision |
| Market findings | Supported learning about buyers, competitors or positioning, including accepted product limits | Evidence and implication; no implementation prompt |

The analyst generates candidates, verifies them against current artifacts and the brand brief,
then ranks eligible actions. Zero actions is valid. Missing retrieval does not establish that a
page is missing or unindexed. Unreadable pages and unconfirmed eligibility remain unknown.
Informational answers need not name a product. A competitor feature need not become a task.

Current-page reads use the audit fetcher's public-address guard, robots policy and daily cache.
Optional rendered-content fallback follows the existing Exa configuration and accounting.
Generation and verification use the configured analyst and admission budget. Stored checks
retain source quotes and reasons. Reads are evidence, not proof of future recommendations.
New-page proposals remain investigations until equivalent existing content can be checked;
bounded page reads do not establish a complete inventory.

## Brand brief

Keep identity and public claims in `brand.about` and `brand.facts`. Record advisory priorities
and deliberate limits separately:

```toml
[brand.advisory]
positioning = "A starter workflow for small teams with a technical owner."
goals = ["Understand when buyers move beyond manual checks."]
boundaries = ["A hosted service is outside the current product scope."]
```

This optional brief guides advice and question curation. Buyer answerers and graders do not
receive it; it does not change measurement comparability. Advice uses the current brief,
including when rebuilding an old run. A relevant loss outside the product scope can remain a
market finding. Do not use boundaries to suppress evidence.

A saved action stays verified until something it was judged against changes:

- `about`, `audience`, `category` or the advisory brief: every saved action is verified again.
- A fact an action's checks quoted: that action is verified again when the whole fact the quote
  came from is edited (a "no longer" in front, an "except" after) or removed, even if the quoted
  words survive in another fact. If the quoted words occur in several facts, editing any of them
  counts. Adding a fact, reordering facts or editing one the action didn't quote changes nothing,
  unless the new or edited fact repeats the quoted words.
- A site or domain: an action is verified again when its target or a page its checks quoted
  stops being on any configured site or domain, or starts being on one (owned work and outreach
  are judged differently). Other site and domain edits change nothing.

The name, aliases and owners change nothing: handoffs, sites and owners are rebuilt from the
current config whenever an action is shown. The set description, which the verifier reads as the
set's purpose, is not checked: editing it leaves verified actions as they are until the analysis is
rebuilt. Verifications saved before this rule stay verified until any brand edit, then need a
rebuild.

## History and compatibility

Work status (open, proposed, accepted, dismissed) is separate from verification readiness.
Earlier fix lists remain readable as investigations with their work history. Their verification
was not recorded; an accepted PR does not establish readiness or a visibility improvement.
Rebuild analysis to apply the new checks to stored answers.

The existing SQLite column holds a versioned analysis envelope; historical fix arrays are read
without rewriting them. Schema 13 backs up schema 12 before enabling this format and prevents
older binaries from opening the newer data. Rollback requires the backup. `/quick-wins` remains
an alias. `/api/next-steps` and MCP `get_next_steps`
expose all three views. `/api/quick-wins` and `get_quick_wins` retain their action-list shape and
return only verified actions. `cheapseats next-steps <run-id>` prints the three views; `fixes`
remains an alias. Run reports show their own analysis; the hub uses the latest completed
analysis of each active set.
