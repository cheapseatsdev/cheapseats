-- Read-only links to a run's report, for people without a sign-in (/share/<token>). Only the
-- token's SHA-256 is kept: the link is shown once, when it's made. A link stops working when it
-- expires or is revoked; the row stays, so the run page can list it.
CREATE TABLE shares (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE,   -- hex SHA-256 of the token
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    include_answers INTEGER NOT NULL DEFAULT 0, -- show the answers' text for the prompts it highlights
    created_by TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT
);
CREATE INDEX shares_run ON shares (run_id);
