-- The name of a space as it is searched: folded to one case by Go (see
-- FoldName), so that SQLite and PostgreSQL find the same spaces whatever the
-- database's own idea of lower case is. The names that exist are filled in by
-- the step of this version that runs after these statements (see migrate.go);
-- from then on every write of a name writes this too.

ALTER TABLE spaces ADD COLUMN name_key TEXT NOT NULL DEFAULT '';
