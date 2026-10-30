-- runs.fixes now also accepts a versioned Next steps envelope. No existing row
-- is rewritten. This version guard backs up schema 12 before first use and
-- prevents older binaries, which only read fix arrays, from opening new data.
SELECT 1;
