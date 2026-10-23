-- The optional providers (docs/optional-providers.md). Report pages read these tables and never
-- call a provider themselves.
--
-- name_decisions: whether a product name the grader wrote is another name for a known product,
-- decided after a run by TypeSafe's Jev (source 'jev') or by hand ('manual'), so the
-- share-of-voice board can fold the two onto one row. confidence is Jev's probability for the
-- option it chose (for a match, that the two names are one product); the threshold that makes it
-- a fold is read from the config, so changing it re-reads the decisions and asks nothing again. A variant decided 'none' (candidate '') is a product of
-- its own, and isn't asked about again until a new candidate appears.
CREATE TABLE name_decisions (
    variant TEXT NOT NULL,         -- the name as the grader wrote it, lower-cased
    candidate TEXT NOT NULL,       -- the known product it was matched to, lower-cased, or ''
    display TEXT NOT NULL,         -- the candidate as it is displayed
    confidence REAL NOT NULL,
    source TEXT NOT NULL,          -- jev | manual
    decided_at TEXT NOT NULL,
    PRIMARY KEY (variant, candidate)
);

-- coverage: which of the brand's own pages a neutral web index (Exa) finds for each prompt of a
-- run, so a miss can be told apart as "no page answers this" from "a page does, and the
-- assistants didn't read it".
CREATE TABLE coverage (
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    prompt_idx INTEGER NOT NULL,
    pages TEXT NOT NULL,           -- JSON: []exa.Result
    cost_usd REAL NOT NULL DEFAULT 0,
    checked_at TEXT NOT NULL,
    PRIMARY KEY (run_id, prompt_idx)
);
