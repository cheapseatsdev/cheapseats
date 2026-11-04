-- Work can be recorded on any next step, not only on an action. kind says which list of the run's
-- analysis the item came from (action | investigation | finding) and fix_rank is then its rank, or
-- its 1-based place in that list. Every existing attempt was recorded on an action.
ALTER TABLE fix_attempts ADD COLUMN kind TEXT NOT NULL DEFAULT 'action';
