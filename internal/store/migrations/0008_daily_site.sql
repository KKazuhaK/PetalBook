-- What an administrator sets for the daily text of the whole server, and what
-- the owner of a space chooses of it.
--
-- site_settings holds the server-wide settings as JSON text by key (the
-- daily text keeps its default source, the hosts the server may fetch from and
-- the external interfaces under one key). daily_lines are the lines of text an
-- administrator pasted or uploaded as a source, in the order they came, each
-- with the language it is in ('' for any). space_daily is a space's choice: whether
-- it takes the server's default when it has no source of its own, and which
-- of the administrator's external interfaces it uses (ids, as JSON).

CREATE TABLE site_settings (
  key     TEXT PRIMARY KEY,
  value   TEXT NOT NULL,
  updated BIGINT NOT NULL
);

CREATE TABLE daily_lines (
  position BIGINT PRIMARY KEY,
  lang     TEXT NOT NULL DEFAULT '',
  body     TEXT NOT NULL,
  source   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX daily_lines_lang ON daily_lines(lang, position);

CREATE TABLE space_daily (
  space_id TEXT PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
  inherit  BIGINT NOT NULL DEFAULT 1,
  apis     TEXT NOT NULL DEFAULT '[]'
);
