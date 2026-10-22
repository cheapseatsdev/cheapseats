# Reading a run

Everything on a run page is worked out from the answers and grades already stored: no model is
called to build it. The same numbers are in the JSON export (`/runs/<id>/export.json`, under
`Summary`), `popcorn report <run-id> -json`, and MCP `get_run` (cut to the top rows).

## Scores

- **Named**: answers that name the brand, of all answers.
- **Recommended**, **top pick**: of the answers to prompts that ask for a choice (info prompts
  only count toward named).
- **Branded prompts** name the brand or one of its aliases, as a whole word ("Is Acme any
  good?"). Being named in their answers proves little, so the track cards lead with the rates
  without them (`unbranded` in JSON) and show the rates with them underneath. The prompt list marks
  each branded prompt. Rankings and trends still use the overall rates.

## Share of voice

| Column | What it is | JSON |
| --- | --- | --- |
| Named | answers naming the product, of all answers: each product's own rate, so the column adds up to more than 100% | `named_rate` (formerly `share`, still written) |
| Share of voice | the product's mentions, of all products' mentions, each product counted at most once per answer: the column adds up to 100% | `share_of_voice` |
| Avg place | its mean position among the products an answer names, over the answers that name it, in the order the grader listed the products (for the brand, its grade's position only when the list left it out) | `avg_position` |
| First, top 3 | answers naming it first, or in the first three, of the answers to prompts that ask for a choice (with 95% intervals in JSON) | `named_first`, `top3` |
| Weighted | position-weighted visibility, as SE Ranking defines it: (N − place + 1) / N in an answer naming N products, 0 where it isn't named, averaged over every answer | `position_weighted` |

**Head to head** sets the brand against each product on the board: answers naming both, and of
those which led (the top pick if it's one of the two, otherwise whichever was named first),
answers naming only one of them, and answers naming neither (`head_to_head`).

**Objections** counts the concerns raised in answers that name the brand, folded (lowercase,
punctuation collapsed) so "Pricing unclear." and "pricing, unclear" count once, with an example
prompt for each (`objections`).

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

`[sources]` in popcorn.toml moves hosts between the last five (see below).

For each host and each page:

- **Retrieved**: answers whose searches returned it, of the answers that could reach a page: the search track, plus any answer that searched, fetched or cited (`retrieved_rate`). Recall answers have no tools, so they are left out rather than diluting every rate.
- **Cited**: answers that linked it, of the same (`cited_rate`).
- **Share**: its citations, of all the run's citations: adds up to 100% (`citation_share`).
- **Cited when retrieved**: of the answers that retrieved it, those that also linked it
  (`cited_when_surfaced`).

A host that isn't the brand's also gets a **bucket**, from the products named in the answers that
cite it: **missing** (they name competitors, never the brand), **shared** (both), **strong** (the
brand more often than any competitor) or **unique** (only the brand). With competitors configured,
only they count as competitors; with none, every other product does.

**Cited without the brand** (`outreach`) lists the missing hosts that aren't a competitor's own,
ranked by distinct competitors named × answers citing it: where to get the brand mentioned.

**Found but not cited** (`found_not_cited`) lists the brand's pages that searches returned and no
answer linked.

## Configuring it

Competitors' sites, in `[[competitors]]` in popcorn.toml or a prompt set:

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
