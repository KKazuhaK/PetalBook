-- What the external interfaces of the daily text said, kept so that a restart
-- (or another process) does not ask them again. One row for an interface (its
-- id and a digest of how it is asked), a language, a refresh period and a skip;
-- body is the text, source, author and link as JSON. Rows older than seven days
-- are removed.

CREATE TABLE daily_cache (
  api_key TEXT NOT NULL,
  lang    TEXT NOT NULL,
  period  TEXT NOT NULL,
  skip    BIGINT NOT NULL,
  body    TEXT NOT NULL,
  fetched BIGINT NOT NULL,
  PRIMARY KEY (api_key, lang, period, skip)
);
CREATE INDEX daily_cache_latest ON daily_cache(api_key, lang, fetched);
CREATE INDEX daily_cache_fetched ON daily_cache(fetched);
