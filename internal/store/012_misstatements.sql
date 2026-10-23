-- The fact check (docs/misstatements.md). After a run, each answer that names the brand is read
-- against brand.facts by the grader model, and the claims that contradict a listed fact are kept.
-- A pass after a run, or cheapseats misstatements <run-id>, writes these; pages, the API and MCP
-- read them and never call a model. Nothing here is part of a run's method: the grades, and so
-- the trends, don't change.

-- fact_checks: that an answer was checked, and under which facts list (facts_hash, a digest of
-- brand.facts as it read), so the pass skips it until the facts change.
CREATE TABLE fact_checks (
    result_id INTEGER PRIMARY KEY REFERENCES results (id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    facts_hash TEXT NOT NULL,
    model TEXT NOT NULL,
    cost_usd REAL NOT NULL DEFAULT 0,
    checked_at TEXT NOT NULL
);
CREATE INDEX fact_checks_run ON fact_checks (run_id);

-- misstatements: one row per fact an answer contradicts. fact is the fact as it read when checked
-- and fact_idx its position then; claim is what the answer got wrong, in the checker's words;
-- quote is the answer's own span, checked to be in it (grade.Quote).
CREATE TABLE misstatements (
    result_id INTEGER NOT NULL REFERENCES results (id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    fact_idx INTEGER NOT NULL,
    fact TEXT NOT NULL,
    claim TEXT NOT NULL DEFAULT '',
    quote TEXT NOT NULL,
    decided_at TEXT NOT NULL,
    PRIMARY KEY (result_id, fact_idx)
);
CREATE INDEX misstatements_run ON misstatements (run_id);
