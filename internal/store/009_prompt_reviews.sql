-- When each set's prompts were last reviewed (cheapseats review, /sets/{id}/review, review_prompts),
-- so get_started can suggest a review once a set has four weeks of runs and none in the last 30
-- days. A review is computed on demand from stored answers; nothing else is kept.
CREATE TABLE prompt_reviews (
    set_id TEXT PRIMARY KEY,
    reviewed_at TEXT NOT NULL
);
