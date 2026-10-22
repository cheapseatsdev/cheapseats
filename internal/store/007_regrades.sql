-- Re-grading (cheapseats regrade): stored answers graded again under today's grader, brand and
-- competitors. Each grade a re-grade replaced, newest last (JSON: []run.PastGrade), and each
-- re-grade of a run, newest last (JSON: []run.Regrade): when, and the method it had before.
ALTER TABLE results ADD COLUMN grade_history TEXT;
ALTER TABLE runs ADD COLUMN regrades TEXT;
