# What the assistants searched

See the assistant's recorded queries and returned results at `/runs/<id>/searches`. Add `?pool=1`
to include up to six earlier finished runs of the same set with the same recorded `Method`.
`/sets/<id>/searches` opens that pooled view for the latest finished run. These reports use stored
answers without provider calls; a returned URL does not prove its page was read.

The same data is at `/api/runs/<id>/searches` and MCP `get_search_queries` (`run`, optional `pool`
and `limit`); MCP trims lists to leading rows.

## On the page

- **Answers searched**: search-track answers that ran at least one search, of all of them.
- **Searches**: searches run. **Distinct queries**: different queries once normalised: lowercase,
  quotes stripped, whitespace collapsed, so `"Peec AI"  vs Profound` and `peec ai vs profound`
  count as one.
- **Top queries**, most answers first, with the prompts that ran each and, for each query:
  - **Brand's sites**: of its runs, those whose results included a page on one of the brand's
    sites (`[brand].sites` and `domains`, matched as the rest of Cheap Seats matches them).
  - **Competitors in results**: configured competitor names/aliases in result titles or matching
    hosts, by search runs. Host matching strips punctuation and uses the whole host or first
    label; forms under four letters are excluded (`Peec AI` matches `peec.ai`).
  - **Named brand**: of the graded answers that ran it, those that named the brand.
  - A `+Name` chip when the query names a product its prompt didn't.
- **Names the assistants added**: the brand or a competitor, by name or alias as a whole word, in a
  search whose prompt didn't name it: the assistant added it to the search. Counted in searches,
  answers and prompts, with examples.
- **How the prompts were rewritten**: query words against prompt words, lowercased and deduplicated,
  with internal dots, dashes and apostrophes kept, stopwords/one-letter words dropped and simple
  plurals folded.
  - **From prompt**: the share of a search's words that were in its prompt. Low means the
    assistant searched in its own words.
  - **Prompt kept**: the share of the prompt's words still in the search. Low means it searched for
    a narrow piece of the question.
  - **Added**: words searches add, across the set, by searches and prompts: the vocabulary the
    assistants search with (years, "best", "alternatives", "reddit"). Names count under names
    added, not here. **Dropped**: prompt words the searches left out.
- **Add as prompt**: queries whose results named competitors and never reached the brand's sites,
  as a `[[prompts]]` entry to paste into the set's file in the config repo. Cheap Seats doesn't write
  it. Reword it as the question a buyer would ask before you merge it; a query naming the brand is
  offered only in head-to-heads, the one set that may name it. A search that ran several queries
  at once (shown joined with ` | `) offers each separately, leaving out ones with search operators
  (`site:`, quoted phrases) and ones under three words.
- **Prompt by prompt**: answers that searched, searches per answer (over every answer), distinct
  queries, both overlap scores, and each prompt's queries with how many answers ran them.
