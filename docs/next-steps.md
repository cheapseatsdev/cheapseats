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
Every action, investigation and finding needs a title that names it, since a collapsed card shows
only the title. A title that is empty, a placeholder, a single word or under six letters drops
the item; the run's insights record what was omitted and why. Output with no insights to speak
of (under 20 characters), or in which no item has a usable title, is a failed analysis rather than
an empty one: the run records the reason as its analysis error, and a rebuild keeps the analysis it
already had. Retrying usually gives a normal result.

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
including when rebuilding an old run. Changes to product facts or advisory context require
saved actions to be verified again. A relevant loss outside the product scope can remain a
market finding. Do not use boundaries to suppress evidence.

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
