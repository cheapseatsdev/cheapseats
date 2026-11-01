-- Whether a cached page's text may stop short of the page: its response passed the read limit
-- (audit.MaxBody) or its text passed the text limit. NULL is a row cached before this was
-- recorded, which can't say, so it reads as cut.
ALTER TABLE page_fetches ADD COLUMN cut INTEGER;
