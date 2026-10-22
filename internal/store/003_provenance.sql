-- Which config commit and Cheap Seats version produced each run. Empty for runs from before.
ALTER TABLE runs ADD COLUMN config_rev TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN app_version TEXT NOT NULL DEFAULT '';
