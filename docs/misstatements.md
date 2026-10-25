# Got wrong about you

The fact check finds answer claims that contradict your `[brand] facts`. After a run, the grader
model checks eligible answers against that list. **Got wrong about you** groups contradictions by
fact, with verified answer quotes; the fix list receives them as evidence. Grades stay unchanged.

```toml
[brand]
facts = [
  "Cheap Seats is MIT licensed and runs as a single Docker container with a SQLite database.",
  "Answers come from Claude and OpenAI models through their APIs, or local Claude Code and Codex CLIs.",
]

[grader]
check_facts = false   # off; on by default when facts are listed
```

## What it checks, and what it never does

- Only answers whose verdict is recommended or mentioned, and that name the brand itself (not a
  namesake the grader marked as another product).
- Only contradictions of a listed fact: the claim and the fact can't both be true. A fact an
  answer leaves out, a vaguer or lesser version of one, an answer that says it can't verify you,
  a claim about a competitor, and an opinion are not misstatements. The checker is told that an
  empty list is the usual answer.
- Each misstatement names the fact, says in a sentence what the answer got wrong, and quotes the
  answer's span. The quote is checked to be in the answer (the same check as the grader's
  evidence); one the model didn't copy is dropped, and so is a fact number off the list.

Checks are stored separately in `fact_checks` and `misstatements`. Facts are sent to the checker
and analyst, not the answer grader, and do not change a run's grading method or trends. Treat
facts as public product context; do not put secrets in them. [Provider boundaries](optional-providers.md#the-shape-that-keeps-them-optional).

## Cost, and when it runs again

Each eligible, unchecked answer takes one grader call. `provider = "claude-code"` uses your
subscription allowance without an API charge; API calls use the same daily budget as runs.
Completed checks are cached per answer and facts-list hash. A changed, added or removed fact
makes eligible answers due for checking again; the next pass or `cheapseats misstatements <run-id>`
replaces their findings. Until then, the page counts checks made under an older list.

[Re-grading](regrading.md) doesn't run it: the grades change, the answers don't. Rerun it with the command if the
verdicts moved enough that different answers now name you.

## Where it shows

- **Run page**: *Got wrong about you* lists each contradicted fact with the answers that did,
  most answers first; the prompt grid marks prompts with a misstatement; each answer shows its
  own under its grade.
- **Fix list**: the analyst gets each contradicted fact, the prompts and one quote, as evidence
  for a `positioning` or `fix-page` fix on the page that should state the fact, with a
  `done_when` like "no answer to prompts 3, 7 contradicts fact 2".
- **API and MCP**: `misstatements` in `/api/runs/<id>` and `get_run`: answers checked, and the
  facts contradicted with their answers, claims and quotes.
- **CLI**: `cheapseats misstatements <run-id>` runs it on a finished run and prints the facts.
