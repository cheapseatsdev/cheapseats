# What the assistants searched

Before a search-track answer, the assistant types its own queries into the web search tool. The
searches page shows what it typed, and what came back: `/runs/<id>/searches` for one run, with
`?pool=1` for that run and up to six of its set's earlier finished runs that were asked and graded
the same way (a change of method never mixes in). `/sets/<id>/searches` opens the pooled view
for the set's latest finished run. Everything is worked out from the stored answers; the page
costs nothing.

The run page and the set page link to it. The same data is at `/api/runs/<id>/searches`, and over
MCP as `get_search_queries` (`run`, optional `pool` and `limit`), which keeps each list to its top
rows.

## On the page

- **Answers searched**: search-track answers that ran at least one search, of all of them.
- **Searches**: searches run. **Distinct queries**: different queries once normalised: lowercase,
  quotes stripped, whitespace collapsed, so `"Peec AI"  vs Profound` and `peec ai vs profound`
  count as one.
- **Top queries**, most answers first, with the prompts that ran each and, for each query:
  - **Brand's sites**: of its runs, those whose results included a page on one of the brand's
    sites (`[brand].sites` and `domains`, matched as the rest of Popcorn matches them).
  - **Competitors in results**: the set's competitors (and the shared ones) named in a result's
    title, or whose own site a result is on (the name or an alias squeezed to letters and digits
    equals the host's, so `Peec AI` matches `peec.ai`), by how many runs.
  - **Named brand**: of the graded answers that ran it, those that named the brand.
  - A `+Name` chip when the query names a product its prompt didn't.
- **Names the assistants added**: the brand or a competitor, by name or alias as a whole word, in a
  search whose prompt didn't name it: the assistant added it to the search. Counted in searches,
  answers and prompts, with examples.
- **How the prompts were rewritten**: a search's words against its prompt's. Words are lowercase
  runs of letters and digits, stopwords and one-letter words dropped, plurals folded.
  - **From prompt**: the share of a search's words that were in its prompt. Low means the
    assistant searched in its own words.
  - **Prompt kept**: the share of the prompt's words still in the search. Low means it searched for
    a narrow piece of the question.
  - **Added**: words searches add, across the set, by searches and prompts: the vocabulary the
    assistants search with (years, "best", "alternatives", "reddit"). Names count under names
    added, not here. **Dropped**: prompt words the searches left out.
- **Add as prompt**: queries whose results named competitors and never reached the brand's sites,
  as a `[[prompts]]` entry to paste into the set's file in the config repo. Popcorn doesn't write
  it. Reword it as the question a buyer would ask before you merge it; a query naming the brand is
  offered only in head-to-heads, the one set that may name it.
- **Prompt by prompt**: answers that searched, searches per answer (over every answer), distinct
  queries, both overlap scores, and each prompt's queries with how many answers ran them.
