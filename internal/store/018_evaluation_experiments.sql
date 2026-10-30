CREATE TABLE evaluation_experiments (
    id TEXT PRIMARY KEY,
    dataset_id TEXT NOT NULL REFERENCES evaluation_datasets(id),
    plan TEXT NOT NULL,
    actor TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE evaluation_predictions (
    experiment_id TEXT NOT NULL REFERENCES evaluation_experiments(id),
    case_id TEXT NOT NULL REFERENCES evaluation_cases(id),
    variant INTEGER NOT NULL,
    repetition INTEGER NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('ready','running','done','failed')),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease TEXT NOT NULL DEFAULT '',
    lease_until TEXT NOT NULL DEFAULT '',
    result TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    cost_usd REAL NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(experiment_id,case_id,variant,repetition)
);
CREATE TABLE evaluation_evidence_exposures (
    signature TEXT PRIMARY KEY,
    experiment_id TEXT NOT NULL REFERENCES evaluation_experiments(id)
);
CREATE TABLE evaluation_experiment_reviews (
    experiment_id TEXT NOT NULL REFERENCES evaluation_experiments(id),
    case_id TEXT NOT NULL REFERENCES evaluation_cases(id),
    actor TEXT NOT NULL,
    revision INTEGER NOT NULL,
    judgment TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('reviewed','unresolved')),
    created_at TEXT NOT NULL,
    PRIMARY KEY(experiment_id,case_id,actor,revision)
);
