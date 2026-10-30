-- An approved version is a complete immutable snapshot, never a live join.
CREATE TABLE evaluation_datasets (
    id TEXT PRIMARY KEY,
    import_id TEXT NOT NULL REFERENCES evaluation_imports(id),
    snapshot TEXT NOT NULL,
    actor TEXT NOT NULL,
    created_at TEXT NOT NULL
);
