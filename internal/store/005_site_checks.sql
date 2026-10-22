-- Site audits (audit.Result as JSON), newest last: the audit page shows the newest, and the fix
-- list reads its errors and warnings. Only the last few are kept.
CREATE TABLE site_audits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL,
    result TEXT NOT NULL
);

-- Third-party pages fetched for the citation check, once per URL per UTC day: a second check the
-- same day reads the text from here. Kept for a month.
CREATE TABLE page_fetches (
    url TEXT NOT NULL,
    day TEXT NOT NULL,
    status INTEGER NOT NULL,           -- 0 when it couldn't be fetched, or robots.txt said no
    error TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL DEFAULT '',     -- visible text, capped
    fetched_at TEXT NOT NULL,
    PRIMARY KEY (url, day)
);

-- The latest citation check of each run (audit.CitationCheck as JSON).
CREATE TABLE citation_checks (
    run_id TEXT PRIMARY KEY REFERENCES runs (id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    result TEXT NOT NULL
);

-- Imported access logs: AI crawler fetches and visits referred by an assistant, per page per day.
-- class is assistant | search | training | engine | referral; bot is the crawler's token, or the
-- referring assistant. By user agent and Referer alone: anyone can send them.
CREATE TABLE crawl_daily (
    day TEXT NOT NULL,
    host TEXT NOT NULL,
    path TEXT NOT NULL,
    class TEXT NOT NULL,
    bot TEXT NOT NULL,
    hits INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0, -- answered 4xx or 5xx
    PRIMARY KEY (day, host, path, class, bot)
);

-- Each counted log line's hash (time, client, host, path, user agent), so importing a log again,
-- or one that overlaps it, counts nothing twice.
CREATE TABLE crawl_seen (
    hash TEXT PRIMARY KEY,
    day TEXT NOT NULL
) WITHOUT ROWID;
