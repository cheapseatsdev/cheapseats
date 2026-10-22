# Reading trends and outcomes

A prompt gets a handful of answers a run (answerers × tracks × samples), so its rate moves a lot
by chance: with six answers, one prompt out of seven rising between two runs is close to certain
even when nothing changed. Popcorn tests every comparison against that noise. It computes all of
it from answers already stored, so there are no extra model calls.

## The building blocks

Prompts are the units. A rate is the mean over prompts of each prompt's own rate, so a prompt
with more answers doesn't count for more. A cell is one prompt's `h` hits out of `n` answers.

- **Cell variance**: `p~ (1 − p~) / n`, with `p~ = (h + 0.5) / (n + 1)`. The smoothing stops a
  cell of 0/6 or 6/6 from claiming it has no noise.
- **Change**: `D = mean over prompts k of (p_after,k − p_before,k)`, with
  `Var(D) = Σ_k (var_after,k + var_before,k) / K²` and a 95% margin of `1.96 √Var(D)`. A change is
  **within noise** when `D ± margin` covers 0.
- **Recommended and top pick** are out of the answers to prompts that ask for a choice. Info
  prompts count toward named only.

## Rankings

- **Vs day before** pairs the two days prompt by prompt, over the prompts both asked (worded the
  same) of the answerers both had. The page greys out a change that's within noise, and shows
  `±X pts` beside it and on hover.
- **Rank moves** are tested the same way. Rank changes when another product crosses the brand,
  so for each product that crossed, Popcorn tests the paired change in the gap (brand share minus
  that product's share of a prompt's answers). The variance is the sum of all four cells'; the
  covariance between the two products is ignored. If no crossing is significant in the direction
  the rank moved, the table says **within noise** and draws no arrow.
- **Rolling window**: 7 days by default, or 14 or 28 (`?window=` on the page,
  `/api/rankings?window=`, MCP `get_rankings` `window`). The window stops at a change of method;
  a [re-graded](regrading.md) run counts as the method it was re-graded under.
- **Two intervals**, for named, recommended and top pick:
  - *Band, across prompts* (the shaded band): the rolling mean ± 1.96 · s / √m, where s is the
    standard deviation of the m prompt-day rates in the window. It answers "questions like
    these": a different sample of prompts would land in about this range.
  - *This set, today* (on hover, and as a tooltip in the table): the day's mean
    ± 1.96 · √(Σ_k var_k) / K. It treats the set's prompts as fixed and covers only the noise of
    sampling answers, so it's narrower.
- **Fix markers**: a triangle under the chart on the day work on a fix was proposed (gold) or
  accepted (green). It links to the commit or pull request.

Pick the rate to draw with `?metric=named|recommended|top_pick`.

## A set's prompts

The set page pools each prompt's answers over the window (7, 14 or 28 days, `?window=`), back from
the newest day that asked it. Pooling stops at a change of method, wording or intent. Named and
recommended each come with a Wilson 95% interval.

**Sample advice.** When some answerer takes more than one sample per prompt, Popcorn splits the
run-to-run variance of each prompt's named rate as `V = S/n + D` (Profound's decomposition). `S` is
the variance of one answer within a run, `n` is the samples, and `D` is the drift between runs
that more samples can't remove. A cell is prompt × answerer × track, over the same-method runs
of the last 28 days. Per cell, `S = mean_r n/(n−1) · p_r (1 − p_r)` and
`D = max(0, var_r(p_r) − S/n)`. These are averaged over cells. Going down to one sample would
widen a run's interval by `w = √((S + D) / (S/n + D))`.

- `w < 1.25`: "Extra samples are buying little precision: between-run drift dominates; samples=1
  would give about the same band."
- Otherwise: "The samples are paying off … samples=1 would make the band about w× as wide."

The line needs at least 4 runs and 6 cells.

## Citation stability

How much the hosts a set's answers cite move from one run to the next, per track, over the same
daily runs the trends use (each day's newest finished run, the last 30 days). A run's citations are
a vector of host **citation shares** `s_h` (a host's citations over all the run's citations, so
`Σ s_h = 1`). For each run against the one before it:

- **Similarity** (Bray–Curtis × 100): `100 · (1 − Σ|a_h − b_h| / Σ(a_h + b_h))`, which for shares
  is `100 · Σ min(a_h, b_h)`, the share of citations the two runs have in common by host. 100 when
  every host kept its share; 0 when no host was cited in both.
- **Overlap** (Jaccard × 100): hosts cited in both over hosts cited in either.
- **New** and **gone**: hosts cited this run and not the one before, and the other way round,
  largest share first.

The chain goes back to a change of method (the run that changed it is the oldest compared). A run
that cited nothing (an outage, or the recall track, which has no search) is skipped rather than
read as every host leaving. A transition across a change of prompts is flagged `prompts_changed`:
some of its churn is the questions.

The headline **stability score** is the mean similarity over the transitions (with the mean
overlap beside it), shown only once there are **3 transitions**: one or two can be a fluke. A high
score says the answers lean on the same sources run after run, so a host entering or leaving
means something. A low one says each run's sources table is a sample.

Where it shows: the set page (the score, the newest transition's new and gone hosts, and each
transition's similarity), `GET /api/sets/{id}/citation-stability?track=&lastDays=` (every
transition in full) and MCP `get_citation_stability` (`set`, optional `track`, `last_days`; the
newest transition's hosts only, ten of each).

## Fix outcomes

An attempt's outcome pools answers on each side, per target prompt, and only counts runs asked
and graded the same way as the fix's run (the same method), with each prompt worded as that
run had it.

- **Before**: the fix's run plus the same-method runs up to 28 days before it, at most 5 runs in
  all.
- **After**: every same-method run since the attempt, within 28 days of the newest.

Popcorn tests the change in named and recommended with `D ± margin` as above, and gives one of
these statuses:

| Status | When |
|---|---|
| pending | the set hasn't run since the attempt |
| not comparable | it has, but only with a different method |
| too early | the newest run is less than **7 days** after the attempt, or the target prompts average fewer than **4** answers after it (Gauge waits 14 days) |
| moved | named or recommended rose: the interval is above 0, and neither fell |
| worse | one fell beyond its margin, and neither rose |
| mixed | one rose and the other fell |
| no detectable change (±X pts) | both intervals cover 0. X is named's margin: a smaller real effect can still be hiding |

The fix's page appearing in searches, fetches or citations after the attempt, when it didn't
before, is shown as *newly surfaced*. That's supporting evidence and not by itself "moved". The
same goes for the target's host: `host h cited→not cited (left the cited set)` or `not
cited→cited (entered the cited set)`, from whether any answer on each side cited a page on it
(`target_host`, `host_cited_before`, `host_cited_after`). The same one-line summary goes to the
analyst who writes the next fix list. The analyst also sees each prompt's `named h/n, recommended h/n` and is told that counts this small are noisy.

The API's attempt `outcome` keeps `run_id`, `prompts` (now pooled, with `choice`, the recommended
denominator), `target_seen_before` and `target_seen_after`. It adds `status`, `summary`, `named` and
`recommended` (`{d, margin, prompts}`), `before_runs`, `after_runs`, `days` and `incomparable`.

## Quick wins

Fixes are ranked by expected gain: `speed × mean over the fix's prompts of (1 − p)`. Speed is
prompts per working day. `p` is the prompt's recommended rate (named, for an info prompt), pooled
over the set's same-method runs of the last 14 days. A prompt with no data there counts as 1. A
prompt already won in most answers adds little. The API returns it as `gain`, next to `speed`.
