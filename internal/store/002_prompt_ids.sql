-- Prompts get stable ids, so a prompt's history survives reordering, rewording and moving it
-- between sets. Rows from before are filled in by app.Backfill at startup.
ALTER TABLE results ADD COLUMN prompt_id TEXT NOT NULL DEFAULT '';
ALTER TABLE results ADD COLUMN prompt_sha TEXT NOT NULL DEFAULT '';
CREATE INDEX results_prompt ON results (prompt_id);

-- How answers were asked and graded (run.Method). Runs only trend together within one method;
-- prompt, competitor and model changes within it are annotated instead.
ALTER TABLE runs ADD COLUMN method TEXT NOT NULL DEFAULT '';
