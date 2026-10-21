-- Visibility runs: one prompt set, every answerer, both tracks, N samples each.
CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    set_id TEXT NOT NULL,
    set_version TEXT NOT NULL,
    name TEXT NOT NULL,
    trigger TEXT NOT NULL,             -- manual | schedule | api
    status TEXT NOT NULL,              -- queued | running | done | failed | cancelled | budget
    snapshot TEXT NOT NULL,            -- JSON: the set, answerers, models and brand as they were
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    error TEXT,
    fixes TEXT,                        -- JSON: ranked fix list ([]analysis.Fix)
    fixes_at TEXT,
    fixes_error TEXT,
    insights TEXT,                     -- markdown report from the analyst
    analysis_cost_usd REAL NOT NULL DEFAULT 0,
    profile TEXT NOT NULL DEFAULT '',
    design TEXT NOT NULL DEFAULT ''
);
CREATE INDEX runs_set ON runs (set_id, created_at);

CREATE TABLE results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    prompt_idx INTEGER NOT NULL,
    prompt TEXT NOT NULL,
    answerer TEXT NOT NULL,
    model TEXT NOT NULL,
    track TEXT NOT NULL,               -- search | recall
    sample INTEGER NOT NULL,
    status TEXT NOT NULL,              -- pending | running | done | error | skipped
    answer TEXT,                       -- JSON: answer.Answer
    grade TEXT,                        -- JSON: grade.Grade
    verdict TEXT,
    top_pick INTEGER,                  -- brand was the top pick
    error TEXT,
    answer_cost_usd REAL NOT NULL DEFAULT 0,
    grade_cost_usd REAL NOT NULL DEFAULT 0,
    started_at TEXT,
    finished_at TEXT,
    UNIQUE (run_id, prompt_idx, answerer, track, sample)
);
CREATE INDEX results_run ON results (run_id);
CREATE INDEX results_finished ON results (finished_at);

-- Every billable attempt: reserved on admission, then settled (or left unknown).
CREATE TABLE spend_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL,
    kind TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    amount_usd REAL NOT NULL,
    state TEXT NOT NULL                -- reserved | settled | unknown
);
CREATE INDEX spend_day ON spend_events (created_at);
