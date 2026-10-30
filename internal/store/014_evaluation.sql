-- Evaluation records and human decisions are separate from production results.
-- Imports retain invalid rows; immutable snapshots never inherit later labels.
CREATE TABLE evaluation_rubrics (
    id TEXT PRIMARY KEY,
    system TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE evaluation_cases (
    id TEXT PRIMARY KEY,
    record TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE evaluation_imports (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    digest TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE evaluation_import_rows (
    import_id TEXT NOT NULL REFERENCES evaluation_imports(id),
    line INTEGER NOT NULL,
    raw TEXT NOT NULL,
    case_id TEXT REFERENCES evaluation_cases(id),
    error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(import_id, line)
);
CREATE TABLE evaluation_reviews (
    case_id TEXT PRIMARY KEY REFERENCES evaluation_cases(id),
    revision INTEGER NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('draft','reviewed','unresolved')),
    judgment TEXT NOT NULL,
    actor TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE evaluation_events (
    case_id TEXT NOT NULL REFERENCES evaluation_cases(id),
    revision INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('draft','judgment','unresolved')),
    judgment TEXT NOT NULL,
    actor TEXT NOT NULL,
    rubric TEXT NOT NULL REFERENCES evaluation_rubrics(id),
    created_at TEXT NOT NULL,
    PRIMARY KEY(case_id, revision)
);
