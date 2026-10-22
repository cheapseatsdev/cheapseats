-- The theme pass: one grader call after a run is graded that groups its objections, praise and
-- reasons for losing into a few named themes (JSON: opinion.Themes, including any error).
ALTER TABLE runs ADD COLUMN themes TEXT;
