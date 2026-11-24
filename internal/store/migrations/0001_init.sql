-- One schema for SQLite and PostgreSQL: TEXT and BIGINT columns only, times as
-- Unix milliseconds, booleans as 0/1. Statements are separated by semicolons at
-- the end of a line; keep semicolons out of everything else in this file.

CREATE TABLE users (
  id       TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  name     TEXT NOT NULL,
  email    TEXT NOT NULL DEFAULT '',
  role     TEXT NOT NULL,
  disabled BIGINT NOT NULL DEFAULT 0,
  created  BIGINT NOT NULL
);
CREATE INDEX users_created ON users(created);
CREATE INDEX users_role ON users(role, disabled);

CREATE TABLE passwords (
  user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  hash    TEXT NOT NULL,
  updated BIGINT NOT NULL
);

CREATE TABLE identities (
  id      TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  issuer  TEXT NOT NULL,
  subject TEXT NOT NULL,
  label   TEXT NOT NULL DEFAULT '',
  created BIGINT NOT NULL,
  UNIQUE (issuer, subject)
);
CREATE INDEX identities_user ON identities(user_id);

CREATE TABLE sessions (
  id        TEXT PRIMARY KEY,
  user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created   BIGINT NOT NULL,
  last_used BIGINT NOT NULL
);
CREATE INDEX sessions_user ON sessions(user_id, last_used);
CREATE INDEX sessions_last_used ON sessions(last_used);
CREATE INDEX sessions_created ON sessions(created);

-- Why a session ended without the person signing out (the sign-in page says so
-- when they come back). One row is kept for 30 days with the SHA-256 of the
-- session cookie value (never the value), the reason and the time. It is looked
-- up only by the hash of a cookie the browser sent, so it tells nothing to a
-- cookie that was never a session. user_id is whose it was, so that enabling a
-- person (or their tenant) again can forget their `disabled` records. No foreign
-- key: the record of a deleted person goes with the daily sweep like any other.
CREATE TABLE session_ends (
  id      TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  reason  TEXT NOT NULL,
  ended   BIGINT NOT NULL
);
CREATE INDEX session_ends_ended ON session_ends(ended);
CREATE INDEX session_ends_user ON session_ends(user_id);

CREATE TABLE invites (
  id         TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  role       TEXT NOT NULL,
  created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created    BIGINT NOT NULL,
  expires    BIGINT NOT NULL,
  used       BIGINT NOT NULL DEFAULT 0,
  used_by    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX invites_expires ON invites(expires);

CREATE TABLE spaces (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  personal_of TEXT UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  created     BIGINT NOT NULL
);

CREATE TABLE space_members (
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role     TEXT NOT NULL,
  created  BIGINT NOT NULL,
  PRIMARY KEY (space_id, user_id)
);
CREATE INDEX space_members_user ON space_members(user_id);

CREATE TABLE drafts (
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  page_id  TEXT NOT NULL,
  user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  base_rev TEXT NOT NULL,
  meta     TEXT NOT NULL,
  body     TEXT NOT NULL,
  created  BIGINT NOT NULL,
  updated  BIGINT NOT NULL,
  held     BIGINT NOT NULL DEFAULT 0,
  due      BIGINT NOT NULL,
  PRIMARY KEY (space_id, page_id, user_id)
);
CREATE INDEX drafts_due ON drafts(held, due);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
