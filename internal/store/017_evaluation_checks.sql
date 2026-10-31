CREATE TABLE evaluation_checks (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES evaluation_cases(id),
    plan TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('ready','running','done','failed')),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease TEXT NOT NULL DEFAULT '',
    lease_until TEXT NOT NULL DEFAULT '',
    result TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    actor TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX evaluation_check_case ON evaluation_checks(case_id,created_at);
CREATE TABLE evaluation_resolutions (
    check_id TEXT NOT NULL REFERENCES evaluation_checks(id),
    revision INTEGER NOT NULL,
    case_id TEXT NOT NULL REFERENCES evaluation_cases(id),
    action TEXT NOT NULL CHECK(action IN ('keep','change')),
    explanation TEXT NOT NULL,
    actor TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(check_id,revision),
    FOREIGN KEY(case_id,revision) REFERENCES evaluation_events(case_id,revision)
);
