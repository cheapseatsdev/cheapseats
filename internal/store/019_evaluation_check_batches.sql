CREATE TABLE evaluation_check_batches (
    id TEXT PRIMARY KEY,
    plan TEXT NOT NULL,
    actor TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('ready','running','done','failed')),
    lease TEXT NOT NULL DEFAULT '',
    lease_until TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE evaluation_check_batch_items (
    batch_id TEXT NOT NULL REFERENCES evaluation_check_batches(id),
    case_id TEXT NOT NULL REFERENCES evaluation_cases(id),
    check_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('ready','running','done','failed')),
    error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(batch_id,case_id)
);
