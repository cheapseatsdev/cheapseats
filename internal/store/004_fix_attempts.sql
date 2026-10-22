-- Work submitted for a fix: the commit or pull request, proposed, then accepted or withdrawn. The fix
-- is copied as it was, since fix lists are rebuilt; target_key finds later fixes for the same page
-- (app.TargetKey), which stay open and show this attempt's outcome.
CREATE TABLE fix_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,              -- the run whose fix list it came from
    set_id TEXT NOT NULL,
    fix_rank INTEGER NOT NULL,
    fix TEXT NOT NULL,                 -- JSON: analysis.Fix
    target_key TEXT NOT NULL,
    url TEXT NOT NULL,                 -- the commit or pull request
    state TEXT NOT NULL,               -- proposed | accepted | withdrawn
    note TEXT NOT NULL DEFAULT '',
    marked_by TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX fix_attempts_target ON fix_attempts (target_key);
CREATE INDEX fix_attempts_set ON fix_attempts (set_id, created_at);
