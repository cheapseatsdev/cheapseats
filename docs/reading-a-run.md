# Reading a run

Run reports use stored answers and grades without model calls. The same metrics are in the JSON
export (`/runs/<id>/export.json`, under `Summary`), `cheapseats report -json <run-id>` and MCP
`get_run`. MCP keeps top rows and omits the snapshot and implementation prompts; `get_next_steps`
includes implementation prompts for verified actions. [Run and result states](states.md).

## Next steps

The dashboard's **Next steps** page (`/next-steps`) keeps three tabs together:

- **Actions:** verified, in-scope changes with supporting evidence, completion criteria and an
  implementation prompt. Existing work status and attempt history remain attached to the action.
- **Investigations:** bounded read-only checks to resolve a premise, eligibility question or
  decision before proposing work. Historical suggestions without recorded verification appear
  here with their prior work status, rather than an implementation prompt.
- **Market findings:** supported observations about fit, alternatives or tradeoffs. An intentional
  product boundary can call for no change.

Run reports show the same three categories for that run, including historical reports. When no
verified action is available, Next steps opens Investigations if any exist. A completion criterion
is a check of work under the team's control; later model answers and citations remain observations.

`GET /api/next-steps` and MCP `get_next_steps` return `actions`, `investigations` and
`market_findings` from each set's latest completed analysis. Both accept `set`; `kind` selects
`actions`, `investigations` or `market_findings`, and `limit` caps each category. `brief=true`
omits action implementation prompts. Action status and priority filters do not filter findings
or investigations. The JSON API also accepts the existing action `gap` and `site` filters.
`omitted=true` adds `omitted`: the candidates each analysis proposed that did not become
actions, with the stage that stopped each and why ([candidates not promoted](analysis.md#candidates-not-promoted)).
It follows `set` and `limit`, whatever `kind` is. A run report lists its own under **Candidates
not promoted**, collapsed, and `/api/runs/{id}` always returns them.

Both also return `analysis_errors`: for each set whose latest finished run's analysis failed, the
`run_id`, a readable `error`, the provider's `request_id` when it gave one, `shown_run_id`, the
run whose analysis the other lists hold for that set, and `shown`, which analysis that is. A
failed build never replaces an analysis the run already has, so `shown` is one of:

- `earlier`: the failed run's own earlier analysis, which the failed rebuild left in place.
- `unverified`: the failed run had no analysis, and its verification failed, so its candidates
  were stored with the error as investigations, unverified (marked `verification_failed`).
- `older-run`: an older run's analysis; the failed run has none.
- `none`: no analysis of the set is available, and `shown_run_id` is absent.

`verification_failed` is true when the shown analysis is itself a failed verification's
candidates, unverified: always with `unverified`, and with `older-run` when that run's
verification failed too, which the message then says.

`set` filters these too; `kind` does not hide them. The Next steps page and the dashboard show
the same message above the advice. A run report, `GET /api/runs/{id}`,
`/api/runs/{id}/fixes` and MCP `get_run` give the run's own error the same way, in
`analysis_error`, and the report shows it with the request id instead of the provider's raw
reply. MCP `get_quick_wins` keeps its list and, when a set it covers has an error, adds a second
text block holding `analysis_errors`.

`/quick-wins` remains a route alias. `/api/quick-wins` and MCP `get_quick_wins` remain
compatibility views of verified actions only. They do not expose unverified historical suggestions
as ready work. `get_run` and `GET /api/runs/{id}` include investigations and market findings alongside
verified actions; the existing `fixes` view contains verified actions only.

MCP `get_started` does not include the `advisory` positioning, goals or boundaries: they are internal
strategy, and an MCP server can be shared or public. The advisory still guides the analyst that builds
Next steps. With no open verified actions, `get_started` points to investigations or market findings
and allows a no-action conclusion.

## Scores

- **Named**: graded answers that substantively name the brand, of all graded answers.
- **Recommended**, **top pick**: of the answers to prompts that ask for a choice (info prompts
  only count toward named).
- **Branded prompts** name the brand or one of its aliases, as a whole word ("Is Acme any
  good?"). Being named in their answers proves little, so the track cards lead with the rates
  without them (`unbranded` in JSON) and show the rates with them underneath. The prompt list marks
  each branded prompt. Rankings and trends still use the overall rates.
- **Errors** counts each answerer's failed answers on a track, and says why when Cheap Seats
  worded the failure itself: an Errors cell reads `11 (9 answered without searching, 2 other)`. The reasons are
  answered without searching, empty answer, grading failed, and other for anything a provider or
  CLI reported (timeouts, sign-in, rate limits; each answer shows its own message). `cheapseats
  report` prints the same; its JSON and MCP `get_run` give it per slice as `error_kinds`.
  A failed call's message never carries what came back verbatim, since that can quote the prompt,
  a page or a credential. An API error shows the provider's own error message and request id, with
  anything shaped like a key or token masked as `[REDACTED]`, cut to 300 characters. A CLI error
  says how the CLI ended (`claude: exit status 1`, `codex: exit status 1: not logged in`,
  `claude: error_max_turns`), not what it printed; to see that, run the CLI by hand as its user.
  **Answered without searching** means a search-track answer came back with no recorded searches
  twice, the second time after being told to search. It usually means the answerer's CLI or
  provider stopped exposing its searches: check its version.

**Evidence** is the answer span supporting a grade, highlighted in the answer and exported as
`evidence` (JSON, CSV, MCP `get_result`). Cheap Seats checks the grader's copied quote against the
answer, ignoring case, spacing, Markdown emphasis and curly quotes, and keeps the answer's own
text. An unmatched quote is dropped. Absent answers have no evidence; older grades may lack it.

## Share of voice

| Column | What it is | JSON |
| --- | --- | --- |
| Named | answers naming the product, of all graded answers; rates can sum above 100% | `named_rate` (formerly `share`, still written) |
| Share of voice | the product's mentions, of all products' mentions, each product counted at most once per answer: the column adds up to 100% | `share_of_voice` |
| Avg place | mean recorded position, in the grader's product-list order; the brand's grade position is a fallback only when the list omitted it | `avg_position` |
| First, top 3 | answers naming it first, or in the first three, of the answers to prompts that ask for a choice (with 95% intervals in JSON) | `named_first`, `top3` |
| Weighted | (N − place + 1) / N where N = max(products named, recorded place); 0 without a position, averaged over all graded answers | `position_weighted` |

**Head to head** sets the brand against each product on the board: answers naming both, and of
those which led (the top pick if it's one of the two, otherwise whichever was named first),
answers naming only one of them, and answers naming neither (`head_to_head`).

**Objections** counts the concerns raised in answers that name the brand, folded (lowercase,
punctuation collapsed) so "Pricing unclear." and "pricing, unclear" count once, with an example
prompt for each (`objections`).

**Pages behind it** associates an objection or praise with the pages cited by answers raising
it: up to three, ordered by answer count, with host, page type and owned/earned status. It also
counts recall answers ("2 from memory"). These are leads to inspect, not proof a page caused the
claim or that a later edit will change it. Recall means no tools were used for that answer.

`GET /api/runs/{id}` carries `sources` (`url`, `host`, `page_type`, `owned`, `answers`) and
`recall_answers` on each `objections` row and each `opinion.sentiment` praise/objection. MCP
`get_run` keeps two pages per row. The analyst gets the leading page for each of the eight most
frequent objections. This uses stored citations and changes no grade.

## Sources

Every page an answer cited, or its searches returned, is sorted into a kind of site:

| Kind | What |
| --- | --- |
| `own` | the brand's sites (`[brand]` domains and sites) |
| `competitor` | a competitor's `domains` |
| `ugc` | forums, Q&A, video and social: Reddit, YouTube, Stack Overflow and Stack Exchange, Hacker News, Quora, Medium, dev.to, X, LinkedIn, GitHub issues and discussions |
| `review` | G2, Capterra, TrustRadius, Product Hunt, Gartner Peer Insights, GetApp, Trustpilot and the like |
| `reference` | Wikipedia, docs.* hosts, package registries, the rest of GitHub |
| `institutional` | .gov, .edu, .mil, .int and their country forms (gov.uk, ac.uk) |
| `editorial` | everything else |

`[sources]` in cheapseats.toml moves hosts between the last five (see below).

For each host and each page:

- **Retrieved**: answers whose searches returned it, of the answers that could reach a page: the search track, plus any answer that searched, fetched or cited (`retrieved_rate`). Recall answers have no tools, so they are left out rather than diluting every rate.
- **Cited**: answers that linked it, of the same (`cited_rate`).
- **Share**: page citations divided by all page citations, or host citations divided by all host
  citations; each URL or host counts once per answer. Each board sums to 100% (`citation_share`).
- **Cited when retrieved**: of the answers that retrieved it, those that also linked it
  (`cited_when_surfaced`).

Search returns and citations are separate observations:

| Search returned the URL | Answer cited it | What is recorded |
| --- | --- | --- |
| Yes | Yes | Returned and linked |
| Yes | No | Returned, without a citation |
| No | Yes | Linked without a recorded search return |
| No | No | Neither observed |

Neither observation proves the assistant read the full page; a citation is not an endorsement.
Source-class and page-type answer counts also overlap: one answer can cite several kinds.
Do not add their answer counts as a funnel.

A non-brand host gets a **bucket** from its citing answers: **missing** (competitors, never the
brand), **unique** (brand, no competitors), **strong** (brand more often than every competitor),
or **shared** (brand and a competitor at least as often). Neither named means no bucket. Only
configured competitors count when any are configured; otherwise every other product does.

**Cited without the brand** (`outreach`) lists the missing hosts that aren't a competitor's own,
ranked by distinct competitors named × answers citing it: where to get the brand mentioned.

**Found but not cited** (`found_not_cited`) lists the brand's pages that searches returned and no
answer linked.

**Named when cited** is shown for each of the brand's own pages in Pages referenced: of the answers
citing the page to prompts that don't name the brand (`cited_unbranded`), those that named it
(`cited_named`). **Recommended when cited** follows it: of those answers asked for a choice
(`cited_choice`), those that recommended the brand (`cited_recommended`); info answers count only
toward named, as in the scores. Each answer counts once however often it links the page. Branded
prompts are left out because an answer to a prompt that names the brand names it trivially. A page
cited in nine answers and named in two of nine was read and not credited: a fix for the page's
copy. Other sites' pages show –, and get_run's `top_pages` carries the same fields.

**Cited pages that changed** (`cited_page_changes`) appears when this run and an earlier run of the
set both had a [citation check](audit.md#cited-pages-that-changed): the pages that now name the
brand or a competitor, or stopped, since that check.

### Page types

The kind of site says whose page it is; the **page type** (`page_type` on each page, rolled up in
`page_types` with cited, retrieved, pages and share of citations) says what the page does, from its
URL and the title the search result or citation gave it. The first rule that matches wins:

| Type | When |
| --- | --- |
| `forum` | Reddit, Quora, Lobsters, Discord; Hacker News items; Stack Overflow and Stack Exchange questions; GitHub or GitLab issues, pull requests and discussions; forum.*, community.*, discuss.* hosts |
| `repo` | a repository's front page: github.com/owner/repo (and GitLab, Codeberg, Bitbucket) |
| `docs` | docs.* hosts, a `/docs/`, `/documentation/` or `/reference/` path, Read the Docs, pkg.go.dev, docs.rs, MDN, Microsoft Learn |
| `comparison` | the path or title says vs, versus, compare or comparison, or "alternative to" |
| `review` | Known review hosts: G2, Capterra, TrustRadius, GetApp, Software Advice, Trustpilot, PeerSpot, Gartner reviews, Product Hunt product pages |
| `listicle` | best (not "best practices"), top N, N best, alternatives, tools/apps/software/platforms for |
| `review` | Other pages whose path or title says review or reviews |
| `article` | a `/blog/`, `/news/`, `/articles/`, `/posts/` path, a dated path (`/2026/05/`), blog.* and news.* hosts, Medium, dev.to, Substack, Hashnode |
| `product` | the home page, `/pricing`, `/plans`, `/features`, `/product`, `/solutions`, `/integrations` |
| `other` | everything else (Wikipedia, a repository's files) |

So `/blog/notion-vs-obsidian` is a comparison and `/blog/best-note-apps` a listicle: what the page
is beats where it's filed. The run page lists the types after the kinds of site; MCP `get_run`
carries `page_types` and each top page's `page_type`.

### Opportunities

`opportunities` scores each source class and each page type within it. Rows carry citation share,
distinct citing answers, brand and competitor presence among those answers, competitors named,
leading hosts and prompt IDs. **Gap = citation share × (1 − brand presence)**: a frequently cited
kind whose citing answers rarely name the brand ranks higher. This measures answer content, not
whether the cited pages themselves name the brand; use a [citation check](audit.md#citation-check)
for that.

Rows are owned (`own`) or earned (other classes). Cells need at least three citations to appear;
smaller cells still contribute to the class total. Class rows include their page-type rows, so
do not add both. The analyst receives leading opportunities. No model call or trend change occurs.

## Configuring it

Competitors' sites, in `[[competitors]]` in cheapseats.toml or a prompt set:

```toml
[[competitors]]
name = "Gauge"
aliases = ["withgauge"]
domains = ["withgauge.com", "github.com/withgauge"]
```

A domain is a host (covering its subdomains) or a host/path prefix. Like the brand's sites,
domains only decide whose pages are whose: they're read from today's config for every run, and
adding or changing them changes no trend or set version.

Moving hosts between kinds:

```toml
[sources]
ugc = ["community.example.com"]
editorial = ["medium.com"]   # instead of ugc
review = ["example.com/reviews"]
```

Classes are `ugc`, `review`, `reference`, `institutional` and `editorial`; `[sources]` wins over
the built-in list, the most specific entry wins, and a host may be in only one class.
