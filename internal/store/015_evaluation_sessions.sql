CREATE TABLE evaluation_groupings (
    import_id TEXT PRIMARY KEY REFERENCES evaluation_imports(id),
    method TEXT NOT NULL,
    plan TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE evaluation_sessions (
    id TEXT PRIMARY KEY,
    import_id TEXT NOT NULL REFERENCES evaluation_imports(id),
    actor TEXT NOT NULL,
    case_ids TEXT NOT NULL,
    cursor INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK(state IN ('active','paused','done')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX evaluation_session_actor ON evaluation_sessions(actor, import_id, updated_at);
