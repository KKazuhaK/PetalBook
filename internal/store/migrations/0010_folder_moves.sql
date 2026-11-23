-- Folder moves whose favourites have not been made to follow yet. A row is
-- written before a folder of a space is moved and removed once everybody's
-- favourites of it (and of its saved views) follow; one left by a crash or a
-- failure is finished, or dropped when the folder did not move, the next time
-- the favourites of the space are read.

CREATE TABLE folder_moves (
  space_id  TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  from_path TEXT NOT NULL,
  to_path   TEXT NOT NULL,
  created   BIGINT NOT NULL,
  PRIMARY KEY (space_id, from_path, to_path)
);

-- The answers of the external interfaces, newest first by interface, for
-- keeping only so many of each.
CREATE INDEX daily_cache_api ON daily_cache(api_key, fetched);
