-- The optional providers, the rest of the list (docs/optional-providers.md). Every table here is
-- written by a pass after a run or on demand, and read by pages, the API and MCP, which never call
-- a provider. Each Jev decision stores the probability for the option it chose, under confidence;
-- the threshold that makes it take effect (typesafe.min_confidence) is read when it is used, so
-- changing it re-reads the rows and asks nothing again. source is jev, rule or manual.

-- phrase_clusters: whether an objection or praise phrase the grader wrote says the same thing as
-- one already counted. cluster is the folded phrase it joins, '' for a phrase that is its own
-- cluster; display is the cluster's phrase as first written.
CREATE TABLE phrase_clusters (
    kind TEXT NOT NULL,            -- objection | praise
    phrase TEXT NOT NULL,          -- grade.Fold of the phrase
    cluster TEXT NOT NULL,         -- grade.Fold of the cluster's phrase, or ''
    display TEXT NOT NULL,
    confidence REAL NOT NULL,
    source TEXT NOT NULL,
    decided_at TEXT NOT NULL,
    PRIMARY KEY (kind, phrase, cluster)
);

-- grade_opinions: a second reading of each graded answer, its verdict and the sentence that most
-- supports it, beside what the grader said at the time. Compared with the result's current grade
-- when read, so a re-grade changes the comparison and not the row.
CREATE TABLE grade_opinions (
    result_id INTEGER PRIMARY KEY REFERENCES results (id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    verdict TEXT NOT NULL,         -- recommended | mentioned | absent
    verdict_confidence REAL NOT NULL,
    probabilities TEXT NOT NULL,   -- JSON: map[verdict]probability
    evidence TEXT NOT NULL DEFAULT '',
    evidence_confidence REAL NOT NULL DEFAULT 0,
    grader_verdict TEXT NOT NULL,
    grader_evidence TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL,
    cost_usd REAL NOT NULL DEFAULT 0,
    decided_at TEXT NOT NULL
);
CREATE INDEX grade_opinions_run ON grade_opinions (run_id);

-- prompt_judgments: what Jev makes of the prompts themselves. key is a hash of what the question
-- saw (the prompt text or pair of texts, and for branded the brand's names), so a reworded prompt
-- is asked again and nothing else is.
CREATE TABLE prompt_judgments (
    kind TEXT NOT NULL,            -- duplicate | off_target | intent | branded
    key TEXT NOT NULL,
    set_id TEXT NOT NULL,
    prompt_ids TEXT NOT NULL,      -- JSON: []string, one or two ids
    choice TEXT NOT NULL,          -- yes | no, selection | info
    confidence REAL NOT NULL,
    source TEXT NOT NULL,
    decided_at TEXT NOT NULL,
    PRIMARY KEY (kind, key)
);
CREATE INDEX prompt_judgments_set ON prompt_judgments (set_id);

-- fix_matches: whether two fixes from different sets' fix lists ask for the same page. key is a
-- hash of the two fixes' signatures (type, target key, title), so rebuilt lists with the same
-- wording ask nothing.
CREATE TABLE fix_matches (
    key TEXT PRIMARY KEY,
    fix_a TEXT NOT NULL,           -- the signature, lexically first
    fix_b TEXT NOT NULL,
    confidence REAL NOT NULL,
    source TEXT NOT NULL,
    decided_at TEXT NOT NULL
);

-- page_judgments: a page's type (key: the normalized URL) or a host's source class (key: the
-- host) where the rule lists gave the default, decided once ever per key.
CREATE TABLE page_judgments (
    kind TEXT NOT NULL,            -- page_type | source_class
    key TEXT NOT NULL,
    choice TEXT NOT NULL,
    confidence REAL NOT NULL,
    source TEXT NOT NULL,
    decided_at TEXT NOT NULL,
    PRIMARY KEY (kind, key)
);

-- prospects: pages a neutral index finds similar to the third-party pages that shaped a run's
-- answers, and that no answer cited: outreach the run didn't know about. in_category is Jev's
-- probability that the page is a roundup or comparison in the brand's category (NULL when
-- [typesafe] is off); names_brand and competitors come from reading the page.
CREATE TABLE prospects (
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    url TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    seed TEXT NOT NULL,            -- the cited page it was found from
    in_category REAL,
    names_brand INTEGER,           -- NULL unread, 0 or 1
    competitors TEXT NOT NULL DEFAULT '[]',  -- JSON: []string
    cost_usd REAL NOT NULL DEFAULT 0,
    found_at TEXT NOT NULL,
    PRIMARY KEY (run_id, url)
);

-- retrieval_baseline: what a neutral index returns for each prompt with no domain filter, the
-- shape of coverage, so a competitor topping the web can be told from one the assistants' own
-- queries find.
CREATE TABLE retrieval_baseline (
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    prompt_idx INTEGER NOT NULL,
    pages TEXT NOT NULL,           -- JSON: []exa.Result
    cost_usd REAL NOT NULL DEFAULT 0,
    checked_at TEXT NOT NULL,
    PRIMARY KEY (run_id, prompt_idx)
);

-- competitor_suggestions: hosts similar to the configured competitors' and the brand's home
-- pages, as suggestions for [[competitors]], never an automatic edit. Refreshed on demand;
-- dismissed survives a refresh.
CREATE TABLE competitor_suggestions (
    host TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL,
    seeds TEXT NOT NULL,           -- JSON: []string, the home pages it was similar to
    on_board INTEGER NOT NULL DEFAULT 0,  -- answers naming it on the latest runs, when unconfigured
    in_category REAL,
    dismissed INTEGER NOT NULL DEFAULT 0,
    found_at TEXT NOT NULL
);

-- exa_contents: a page's text read through Exa when plain HTTP got an empty shell, kept a day
-- so the audit and the citation check read a page at most once a day.
CREATE TABLE exa_contents (
    url TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL,
    cost_usd REAL NOT NULL DEFAULT 0,
    fetched_at TEXT NOT NULL
);
