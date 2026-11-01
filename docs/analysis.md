# From run evidence to next steps

A finished run produces three kinds of advice:

- **Actions:** a material remaining change supported by current artifacts and the product brief.
- **Investigations:** a bounded check needed before recommending work, with the decision it changes.
- **Market findings:** what the answers teach, including legitimate reasons to choose another product and intentional limits worth keeping.

Zero actions is a valid result. A brand missing from an answer does not establish a missing page, a bad claim, or a feature to build. A returned search result is not proof that the assistant read its page; a citation does not establish what caused the recommendation. Results from memory do not establish what a website says today.

## Keep the intended market explicit

`[brand.advisory]` supplies positioning, goals and boundaries for advice. These fields constrain proposed work; they do not change how buyer answers are asked or graded. Keep public capability claims in `brand.facts`. A supported capability does not imply that expanding it is an intended goal.

The analyst also receives the audience, category and prompt-set description. A valid competitor recommendation can remain a finding. For example, a product intentionally covering only two model families need not add every surface a broader competitor measures.

## Generate candidates, then verify

The first analyst call reads graded results with raw answer excerpts and result IDs. It keeps search queries, returned results, fetched URLs and citations distinct. It suggests at most eight candidates and can instead return investigations or findings. The limit is in the prompt rather than the output schema, which the Claude API can't constrain by list length; if the analyst returns more, the eight best ranked are kept. Each candidate must earn its place with evidence, a bounded read-only check naming the artifact and specific unknown, and a concrete business decision that the answer could change. Weak ideas are omitted with a short explanation; they do not become research work by default.

A candidate's target is one page, as one absolute URL (a product candidate's target is a short name of the capability instead), and its check must contain that URL. The comparison ignores the scheme, the case of the host, a leading www. and one trailing slash; the path, query and fragment must match exactly, so /Docs and /docs are different pages. A URL the check wraps in a delimiter pair, such as <…>, (…) or quotes, is taken exactly as written inside it. After a bare URL that ends a sentence or list item, sentence punctuation and closing quotes are not part of it, nor is a closing parenthesis the URL didn't open. The target is compared as written. A check that doesn't name its target URL is omitted with that reason. A target is used as the analyst wrote it. One that isn't a single absolute URL is not omitted for that, and is never read as a page, so verification can only make it an investigation, even when it names a page read for another reason, such as a brand site. A product target is never read as a page either.

Before a candidate becomes work, the app's reader checks a bounded shortlist of current targets and brand pages. Analysis requests at most 12 pages and supplies at most 12,000 characters of each page to a separate verification call. Verification reads each page fresh rather than from the citation check's day cache, and marks a page truncated when its text is incomplete: clipped to 12,000 characters, or cut by the reader because the response passed the 2 MiB read limit or ended before its declared length, its visible text passed 200,000 bytes, the optional Exa reader's text reached that cap, or the page is empty without JavaScript and no reader rendered it. Page text goes to the configured analyst provider as evidence. A failed, blocked or empty read remains unknown.

The verification checks:

| Check | What it must establish |
| --- | --- |
| Existing work | What the current relevant artifact already supplies |
| Remaining change | The specific material difference still needed |
| Capability | The claims are supported without blurring collection methods or operating costs |
| Strategy | The change fits the stated advisory goals and boundaries |
| Eligibility | Submission rules and qualifications permit the proposed outreach; or why these do not apply to owned work |
| Completion | The team can complete and check the work without requiring a future recommendation or another publisher's acceptance |

Each passing check needs a source, an exact quote and a reason specific to the candidate. The app checks that the source was actually supplied and that its quote occurs in that source. Existing-page and outreach changes require a readable current target. The strategy check must cite the advisory brief; a product change must quote an explicit goal, rather than general positioning or a boundary. A new-page proposal stays an investigation until the relevant existing-content inventory has been checked; a few sampled pages cannot establish that no equivalent content exists.

These checks support an editorial decision; they do not prove that a proposed change will improve visibility. A verifier can still misinterpret quoted evidence. Inspect its reasons before taking a consequential action.

Only verified actions receive an implementation handoff. It includes the product brief and verification evidence, keeps the change within scope and asks the implementer to check that it is still needed. Outreach handoffs prepare drafts; they do not send messages or open third-party pull requests.

If verification cannot complete, only candidates with a useful specific check and decision remain investigations. Their cards retain that exact check, the decision, the reason it matters and supporting evidence. There is no generic “read the page and promote if useful” fallback. Standalone investigations also require a rationale, check, decision and evidence. If work already exists, or the proposal conflicts with an accepted boundary, the result can be a finding.

Verification fails when its call fails or when none of the current pages could be read (the site was down, refused the reader or returned no text). Either way the run records the failure as its analysis error: it is logged, the run page shows it, `/api/runs/{id}/fixes` returns it in `error`, and the deployment's watch script reports it as a failed fix list. A failed build never replaces an analysis the run already has: a rebuild that fails, in verification or earlier, keeps the earlier analysis and its time and records only the error and its cost. Only a run with no analysis yet stores the candidates as investigations, marked `verification_failed`, together with the error. A clean rebuild replaces either and clears the error. The error is recorded only against the analysis the build started from: if another process stored an analysis meanwhile, that one stands, without the error.

A deployment with no advisory brief is not a failed verification. No candidate can pass the strategy check without one, on this run or any retry, so every candidate is an investigation that says the brief is missing, no pages are read, and the analysis is complete, with no error.

When a rebuild fails and keeps the earlier analysis, `cheapseats fixes -rebuild <run-id>` prints the analysis error with "Showing the run's earlier analysis, which the failed rebuild left in place.", then the kept analysis, then the error, and exits 1. The sentence is printed once: the closing error repeats it only when no analysis error above said it. `POST /api/runs/{id}/fixes` answers 500 with `error`, that sentence in `showing`, and in `analysis` the body `GET /api/runs/{id}/fixes` would return, which holds the kept analysis. Any other failure is a 500 with `error` alone. A first build whose verification failed stored its result, so both answer as for a clean build, with the error in the analysis.

### Candidates not promoted

Every candidate the analyst proposes ends as an action or in the analysis's `omitted` list, which records its title, type, target, `stage`, `outcome` and `reason`:

| Stage | Outcome | Reason |
| --- | --- | --- |
| `filter` | `dropped` | The title, plan, empty-field or type test it failed: the plan test's own sentence (for example, "its check does not name its target URL."), the fields it left empty, or its unknown type |
| `cap` | `dropped` | Its rank, beyond the eight best ranked |
| `review` | `rejected` | The reviewer's `reject` and summary |
| `review` | `finding` | The reviewer's `already-done` or `boundary` and summary; the market finding is in its own list |
| `review` | `investigation` | The gate verification failed at, the reviewer's status and summary (or that a `verified` came with no summary), or why review did not run (no advisory brief, no readable page, a failed verification call); the investigation is in its own list |

A reason is cut to 300 characters. The run page lists them under **Candidates not promoted**, collapsed, below Market findings; `/api/runs/{id}` and `/api/runs/{id}/fixes` return them as `omitted`, and `/api/next-steps` and MCP `get_next_steps` add them when asked (`omitted=true`). An analysis stored before this has none.

Building an analysis logs one `analysis built` line with the run, the candidates proposed, how many the filters dropped (`filtered`, and `filtered_by` counting each reason), the cap cut, reached review, were verified and were rejected, the analysis's findings and investigations, and the cost. A degraded build logs it with `verify_error`, the failure's kind (`timeout`, `http_529`, `decode`, `cli_exit`, `no_pages`, `call_failed` and so on) rather than its message, which can carry a reply's text or a secret; an omission whose verification call failed names the same kind. Every reason `filtered_by` counts is a fixed sentence.

`omitted` was added to the stored analysis envelope without a version change or a schema migration: it lives in the existing `runs.fixes` JSON, an older build reading a newer analysis ignores it, and a newer build reading an older one finds none.

### Why verification reads pages fresh

- The alternative was to record whether each reading was cut in a `cut` column on `page_fetches`, the day cache. That is a schema 20 migration, and an older build refuses an upgraded database, so it breaks rollback.
- Fresh reads avoid a stored-format change for a feature that is still being measured. They cost at most 12 extra page fetches per analysis.
- Revisit this if the volume or latency of verification fetches becomes a problem.

## Separate completion from later results

An action's completion check describes the work itself: for example, a corrected link passes a local check, or a factual submission draft meets the published requirements. Later runs can show whether an answer found the page or recommended the product. Those observations are separate from completion and do not establish that the change caused them.

Verification readiness is separate from the existing work states (`open`, `proposed`, `accepted`, `dismissed`). A recorded pull request does not prove improved visibility. Changing the advisory brief, `about`, `audience` or `category`, a fact an action's checks quoted, or whether its pages are on a configured site invalidates an earlier verification without calling a model from a report page ([what invalidates it](next-steps.md#brand-brief)).

## Historical results and cost

Advice is stored as a versioned envelope containing actions, investigations and market findings. On read, a ready action must retain all six distinct checks, their quotes and reasons, recorded source URLs and product context. A status or digest alone does not establish readiness; incomplete records stay unverified without fetching pages again. Old fix arrays remain readable as historical, unverified suggestions; they do not acquire executable handoffs just because an old prompt was stored with them. Rebuilding advice does not re-ask buyer questions or change stored grades.

The analyst uses its configured API or subscription provider. A run with candidates and readable current artifacts normally makes two analyst calls, generation and verification. No-candidate runs, missing advisory briefs and unreadable evidence can stop before the second call. Both calls use the existing accounting context; subscription usage still consumes the plan's allowance.
