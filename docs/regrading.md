# Re-grading

A change to what the grader reads (its prompt or model, the brand's name, aliases or `about`, a
set's competitors) starts a new method, and trends start over with it. Re-grading grades the
answers older runs already have again, as they would be graded today, so those runs join the new
method's trend from their own dates on. Answers are never asked again.

```bash
popcorn regrade <set-id|run-id> [-since 28d]          # a dry run: what it would do and cost
popcorn regrade <set-id|run-id> -since 90d -dry-run=false
```

- **What's re-graded**: a set's finished runs from the last `-since` (`28d`, `72h`, or `all`;
  default 28 days), or the one run named. A run whose grading inputs already match today's is
  skipped: each run is fingerprinted by the grader prompt and model, the brand's name, aliases and
  about, and the competitors' names and aliases (`run.GradeInputs`). Unfinished and running runs
  are left alone, and so are runs asked under another answer prompt or search policy: those are
  part of the method too, and a re-grade can't change them, so it couldn't join them to today's
  trend.
- **Cost**: one grader call per answer. On `provider = "claude-code"` it's $0, on your
  subscription. Through the API the dry run estimates it from token counts (about four characters
  a token, plus the grader's thinking) and `[pricing]`; the real calls go through the same budget
  as runs, and a run the day's remaining budget can't cover isn't started.
- **All or nothing per run**: every answer of a run is graded first, then the new grades are
  written together, only over the run as it was read: if something else changed it meanwhile
  (another `popcorn regrade`, a retry), nothing is written. A run whose grading fails, or is cut
  off, is left exactly as it was.
- **History stays honest**: the run's snapshot takes today's grading inputs (how its answers were
  asked stays as it was), so its method is recomputed from it like any run's and it trends with
  today's runs. Each replaced grade is kept with its answer (`results.grade_history`), and each
  re-grade with the run (`regrades` in `/api/runs/<id>`): when, how many answers, the cost, and
  the method and grader it had before. The run page says "re-graded <date> (was method X)".
  Re-grading only some of a set's runs moves the trend's break to before the oldest re-graded run.
- **Not rebuilt**: the run's fix list and themes stay as they were; rebuild them with
  `popcorn fixes <run-id> -rebuild` and `popcorn themes <run-id> -rebuild` if you want them to
  follow the new grades.

In the web UI, a set's page links to **Re-grade stored answers**, which shows the dry run (runs,
answers, method change, estimated cost) and a button to start it in the background. Over MCP,
`regrade` takes a set or run as `target`, `since_days` (default 28, 0 for all; negative is an error) and `dry_run`,
which is true unless set to false.
